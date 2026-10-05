package scaling

import (
	"slices"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
)

func group(name string, created time.Time, spec finopsv1.ScalingGroupSpec, phase string) finopsv1.ScalingGroup {
	return finopsv1.ScalingGroup{
		ObjectMeta: metav1.ObjectMeta{Name: name, CreationTimestamp: metav1.NewTime(created)},
		Spec:       spec,
		Status:     finopsv1.ScalingGroupStatus{Phase: phase},
	}
}

// greenZone models the feature request: a shared platform that no environment can run
// without, and environments on very different schedules.
func greenZone(platformPhase, ppsPhase, stagPhase, e2ePhase string) []finopsv1.ScalingGroup {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	return []finopsv1.ScalingGroup{
		group("platform", t0, finopsv1.ScalingGroupSpec{
			Namespaces: []string{"logging", "postgresql", "kafka-streaming"},
			Activation: finopsv1.ActivationOnDemand,
		}, platformPhase),
		// pps1: non-stop Monday to Friday.
		group("pps1", t0.Add(time.Minute), finopsv1.ScalingGroupSpec{
			Namespaces: []string{"pps1-boss", "pps1-genai"},
			DependsOn:  []string{"platform"},
			Schedules: []finopsv1.ScalingSchedule{{
				StartDay: intPtr(1), StartTime: "00:00", EndDay: intPtr(5), EndTime: "23:59", Timezone: "UTC",
			}},
		}, ppsPhase),
		// stag1: business hours, seven days a week.
		group("stag1", t0.Add(2*time.Minute), finopsv1.ScalingGroupSpec{
			Namespaces: []string{"stag1-boss"},
			DependsOn:  []string{"platform"},
			Schedules:  []finopsv1.ScalingSchedule{{Days: []int{0, 1, 2, 3, 4, 5, 6}, StartTime: "09:00", EndTime: "18:00", Timezone: "UTC"}},
		}, stagPhase),
		// e2e1: non-stop three days a week.
		group("e2e1", t0.Add(3*time.Minute), finopsv1.ScalingGroupSpec{
			Namespaces: []string{"e2e1-boss"},
			DependsOn:  []string{"platform"},
			Schedules:  []finopsv1.ScalingSchedule{{Days: []int{1, 3, 5}, StartTime: "00:00", EndTime: "23:59", Timezone: "UTC"}},
		}, e2ePhase),
	}
}

func TestPlanGroupsPlatformFollowsItsDependents(t *testing.T) {
	e := &Engine{}

	t.Run("weekend night: nothing needs the platform", func(t *testing.T) {
		plans := e.PlanGroups(at(t, time.Saturday, "03:00"), greenZone(PhaseScaledUp, PhaseScaledDown, PhaseScaledDown, PhaseScaledDown))
		p := plans["platform"]
		if p.Decision.Active || p.Decision.Mode != ModeOnDemand || len(p.RequiredBy) != 0 {
			t.Errorf("platform plan = %+v, want down and idle", p)
		}
	})

	t.Run("platform waits for a dependent that is still scaling down", func(t *testing.T) {
		plans := e.PlanGroups(at(t, time.Saturday, "00:05"), greenZone(PhaseScaledUp, PhaseScalingDown, PhaseScaledDown, PhaseScaledDown))
		p := plans["platform"]
		if !p.Decision.Active || !slices.Equal(p.RequiredBy, []string{"pps1"}) {
			t.Errorf("platform plan = %+v, want it kept up for pps1 until pps1 is fully down", p)
		}
	})

	t.Run("weekday business hours: platform up for every running environment", func(t *testing.T) {
		plans := e.PlanGroups(at(t, time.Wednesday, "10:00"), greenZone(PhaseScaledDown, PhaseScaledDown, PhaseScaledDown, PhaseScaledDown))
		p := plans["platform"]
		if !p.Decision.Active || p.Decision.Mode != ModeDependency {
			t.Fatalf("platform plan = %+v, want up because of its dependents", p)
		}
		if want := []string{"e2e1", "pps1", "stag1"}; !slices.Equal(p.RequiredBy, want) {
			t.Errorf("RequiredBy = %v, want %v", p.RequiredBy, want)
		}
		// Environments must not start before the platform is fully up.
		for _, env := range []string{"pps1", "stag1", "e2e1"} {
			if !plans[env].BlockedOnDependencies() || !slices.Equal(plans[env].WaitingFor, []string{"platform"}) {
				t.Errorf("%s plan = %+v, want it waiting for the platform", env, plans[env])
			}
		}
	})

	t.Run("an environment still scaling down after hours keeps the platform", func(t *testing.T) {
		plans := e.PlanGroups(at(t, time.Tuesday, "18:02"), greenZone(PhaseScaledUp, PhaseScaledUp, PhaseScalingDown, PhaseScaledDown))
		if got := plans["platform"].RequiredBy; !slices.Equal(got, []string{"pps1", "stag1"}) {
			t.Errorf("RequiredBy = %v, want [pps1 stag1]", got)
		}
	})

	t.Run("weekday night: only pps1 keeps the platform up", func(t *testing.T) {
		plans := e.PlanGroups(at(t, time.Tuesday, "23:00"), greenZone(PhaseScaledUp, PhaseScaledUp, PhaseScaledDown, PhaseScaledDown))
		if got := plans["platform"].RequiredBy; !slices.Equal(got, []string{"pps1"}) {
			t.Errorf("RequiredBy = %v, want [pps1]", got)
		}
		if plans["pps1"].BlockedOnDependencies() {
			t.Error("pps1 must not wait once the platform is ScaledUp")
		}
		if plans["stag1"].Decision.Active {
			t.Error("stag1 is outside business hours")
		}
	})
}

func TestPlanGroupsManualOverrideOnDependencyWins(t *testing.T) {
	groups := greenZone(PhaseScaledUp, PhaseScaledUp, PhaseScaledDown, PhaseScaledDown)
	down := false
	groups[0].Spec.Active = &down

	p := (&Engine{}).PlanGroups(at(t, time.Wednesday, "10:00"), groups)["platform"]
	if p.Decision.Active || p.Decision.Mode != ModeManualDown {
		t.Errorf("platform plan = %+v, a manual override must beat dependent demand", p)
	}
}

func TestPlanGroupsMissingDependencyBlocks(t *testing.T) {
	groups := []finopsv1.ScalingGroup{group("app", time.Now(), finopsv1.ScalingGroupSpec{
		Namespaces: []string{"app"}, DependsOn: []string{"platform-typo"},
	}, "")}
	p := (&Engine{}).PlanGroups(time.Now(), groups)["app"]
	if !p.BlockedOnDependencies() || !slices.Equal(p.MissingDependencies, []string{"platform-typo"}) {
		t.Errorf("plan = %+v, want it blocked on the missing dependency", p)
	}
}

func TestPlanGroupsCyclesAreIgnored(t *testing.T) {
	now := time.Now()
	groups := []finopsv1.ScalingGroup{
		group("a", now, finopsv1.ScalingGroupSpec{Namespaces: []string{"a"}, DependsOn: []string{"b"}, Activation: finopsv1.ActivationOnDemand}, PhaseScaledDown),
		group("b", now, finopsv1.ScalingGroupSpec{Namespaces: []string{"b"}, DependsOn: []string{"a"}}, PhaseScaledDown),
		group("self", now, finopsv1.ScalingGroupSpec{Namespaces: []string{"s"}, DependsOn: []string{"self"}}, PhaseScaledDown),
	}
	plans := (&Engine{}).PlanGroups(now, groups)
	for _, name := range []string{"a", "b", "self"} {
		if !plans[name].InCycle {
			t.Errorf("%s must be reported as part of a cycle", name)
		}
		if plans[name].BlockedOnDependencies() {
			t.Errorf("%s must not deadlock on a cycle", name)
		}
	}
	if plans["a"].Decision.Active {
		t.Error("a cycle must not pin an OnDemand group up")
	}
}

func TestPlanGroupsNamespaceConflicts(t *testing.T) {
	t0 := time.Now()
	groups := []finopsv1.ScalingGroup{
		group("newer", t0.Add(time.Hour), finopsv1.ScalingGroupSpec{Namespaces: []string{"shared", "own"}}, ""),
		group("older", t0, finopsv1.ScalingGroupSpec{Namespaces: []string{"shared"}}, ""),
	}
	plans := (&Engine{}).PlanGroups(t0, groups)
	if got := plans["newer"].ConflictingNamespaces; !slices.Equal(got, []string{"shared"}) {
		t.Errorf("newer conflicts = %v, want [shared]", got)
	}
	if plans["newer"].NamespaceOwners["shared"] != "older" {
		t.Errorf("owner of shared = %q, want older", plans["newer"].NamespaceOwners["shared"])
	}
	if len(plans["older"].ConflictingNamespaces) != 0 {
		t.Errorf("the older group keeps its namespaces, got conflicts %v", plans["older"].ConflictingNamespaces)
	}
}
