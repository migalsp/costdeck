package scaling

import (
	"slices"
	"sort"
	"time"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
)

// ModeOnDemand: an OnDemand ScalingGroup that no dependent currently needs, so it is down.
const ModeOnDemand = "OnDemand"

// PhaseWaitingForDependencies marks a group that wants to be up but holds back until
// every group it depends on reports ScaledUp.
const PhaseWaitingForDependencies = "WaitingForDependencies"

// GroupPlan is the resolved desired state of one ScalingGroup within the dependency graph
// of all groups in its namespace.
type GroupPlan struct {
	// Decision is the final desired state. Its Mode is ModeDependency when the group is
	// only up because dependents need it.
	Decision Decision
	// RequiredBy lists the dependents that currently keep this group up.
	RequiredBy []string
	// WaitingFor lists dependencies that exist but are not ScaledUp yet.
	WaitingFor []string
	// MissingDependencies lists dependsOn entries that name no existing group.
	MissingDependencies []string
	// InCycle is true when the group is part of a dependency cycle. Its dependsOn is then
	// ignored entirely, so a cycle can neither deadlock nor pin groups up.
	InCycle bool
	// ConflictingNamespaces lists namespaces owned by an older group, which this group
	// must not touch.
	ConflictingNamespaces []string
	// NamespaceOwners maps each conflicting namespace to the group that owns it.
	NamespaceOwners map[string]string
}

// BlockedOnDependencies reports whether the group wants to be up but must wait.
func (p GroupPlan) BlockedOnDependencies() bool {
	return p.Decision.Active && (len(p.WaitingFor) > 0 || len(p.MissingDependencies) > 0)
}

// PlanGroups resolves the desired state of every ScalingGroup in one namespace, taking
// dependsOn edges into account:
//
//   - a group's own desire comes from its override or schedules (nothing for OnDemand);
//   - a dependency is brought up while any dependent wants to be up, and a running
//     dependency is kept up until its dependents are fully down, so it stops after them;
//     a dependent on its way down never starts a dependency that is already down;
//   - a manual override on the dependency itself always wins.
func (e *Engine) PlanGroups(now time.Time, groups []finopsv1.ScalingGroup) map[string]GroupPlan {
	byName := make(map[string]*finopsv1.ScalingGroup, len(groups))
	for i := range groups {
		byName[groups[i].Name] = &groups[i]
	}
	inCycle := findCycles(byName)

	dependents := make(map[string][]string)
	for name, g := range byName {
		if inCycle[name] {
			continue
		}
		for _, dep := range g.Spec.DependsOn {
			if _, ok := byName[dep]; ok && dep != name && !inCycle[dep] {
				dependents[dep] = append(dependents[dep], name)
			}
		}
	}

	plans := make(map[string]GroupPlan, len(byName))
	var resolve func(name string) GroupPlan
	resolve = func(name string) GroupPlan {
		if p, ok := plans[name]; ok {
			return p
		}
		g := byName[name]
		plan := GroupPlan{Decision: e.ownDecision(now, g), InCycle: inCycle[name]}
		for _, d := range dependents[name] {
			dp := resolve(d)
			// A dependent that wants to be up needs this group up. One that is still on its
			// way down only holds this group while it is running, so that it stops after its
			// dependents; it never brings a group that is down, or going down, back up.
			goingDown := !dp.Decision.Active && byName[d].Status.Phase != PhaseScaledDown
			if dp.Decision.Active || (goingDown && isRunning(g.Status.Phase)) {
				plan.RequiredBy = append(plan.RequiredBy, d)
			}
		}
		sort.Strings(plan.RequiredBy)

		manual := plan.Decision.Mode == ModeManualUp || plan.Decision.Mode == ModeManualDown
		if !manual && !plan.Decision.Active && len(plan.RequiredBy) > 0 {
			plan.Decision = Decision{Active: true, Mode: ModeDependency}
		}
		plans[name] = plan
		return plan
	}
	for name := range byName {
		resolve(name)
	}

	owners := namespaceOwners(groups)
	for name, g := range byName {
		plan := plans[name]
		if !plan.InCycle {
			for _, dep := range g.Spec.DependsOn {
				dg, ok := byName[dep]
				switch {
				case !ok:
					plan.MissingDependencies = append(plan.MissingDependencies, dep)
				case dg.Status.Phase != PhaseScaledUp:
					plan.WaitingFor = append(plan.WaitingFor, dep)
				}
			}
		}
		for _, ns := range g.Spec.Namespaces {
			if owner := owners[ns]; owner != name {
				plan.ConflictingNamespaces = append(plan.ConflictingNamespaces, ns)
				if plan.NamespaceOwners == nil {
					plan.NamespaceOwners = map[string]string{}
				}
				plan.NamespaceOwners[ns] = owner
			}
		}
		plans[name] = plan
	}
	return plans
}

// isRunning reports whether a group's workloads are up or coming up.
func isRunning(phase string) bool {
	return phase == PhaseScaledUp || phase == PhaseScalingUp
}

// ownDecision is the group's desired state before dependents are considered.
func (e *Engine) ownDecision(now time.Time, g *finopsv1.ScalingGroup) Decision {
	d := e.Decide(now, g.Spec.Schedules, g.Spec.Active, g.Spec.ActiveUntil, g.Annotations)
	if g.Spec.Activation == finopsv1.ActivationOnDemand && d.Mode != ModeManualUp && d.Mode != ModeManualDown {
		return Decision{Active: false, Mode: ModeOnDemand}
	}
	return d
}

// namespaceOwners assigns every namespace to the oldest group that lists it (ties broken
// by name), so a namespace shared by mistake is scaled by exactly one group.
func namespaceOwners(groups []finopsv1.ScalingGroup) map[string]string {
	ordered := make([]*finopsv1.ScalingGroup, len(groups))
	for i := range groups {
		ordered[i] = &groups[i]
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		ti, tj := ordered[i].CreationTimestamp, ordered[j].CreationTimestamp
		if !ti.Equal(&tj) {
			return ti.Before(&tj)
		}
		return ordered[i].Name < ordered[j].Name
	})
	owners := make(map[string]string)
	for _, g := range ordered {
		for _, ns := range g.Spec.Namespaces {
			if _, taken := owners[ns]; !taken {
				owners[ns] = g.Name
			}
		}
	}
	return owners
}

// findCycles returns the groups that sit on a dependsOn cycle (including self-references).
func findCycles(byName map[string]*finopsv1.ScalingGroup) map[string]bool {
	const (
		unvisited = iota
		visiting
		done
	)
	state := make(map[string]int, len(byName))
	inCycle := make(map[string]bool)
	var stack []string

	var visit func(name string)
	visit = func(name string) {
		state[name] = visiting
		stack = append(stack, name)
		for _, dep := range byName[name].Spec.DependsOn {
			if _, ok := byName[dep]; !ok {
				continue
			}
			switch state[dep] {
			case unvisited:
				visit(dep)
			case visiting:
				// Everything on the stack from dep onwards forms the cycle.
				for _, member := range stack[slices.Index(stack, dep):] {
					inCycle[member] = true
				}
			}
		}
		stack = stack[:len(stack)-1]
		state[name] = done
	}

	names := make([]string, 0, len(byName))
	for name := range byName {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if state[name] == unvisited {
			visit(name)
		}
	}
	return inCycle
}
