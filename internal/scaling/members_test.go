package scaling

import (
	"slices"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
)

func ns(name string, labels map[string]string) corev1.Namespace {
	return corev1.Namespace{Name: name, Labels: labels}
}

func TestSelectNamespaces(t *testing.T) {
	gone := ns("pr-9", map[string]string{"env": "preview"})
	gone.DeletionTimestamp = &metav1.Time{Time: time.Now()}
	all := []corev1.Namespace{
		ns("pr-12", map[string]string{"env": "preview", "team": "shop"}),
		ns("pr-11", map[string]string{"env": "preview", "team": "pay"}),
		ns("pr-10", map[string]string{"env": "preview", finopsv1.OptOutLabel: "off"}),
		gone,
		ns("costdeck", map[string]string{"env": "preview"}),
		ns("kube-system", map[string]string{"env": "preview"}),
		ns("prod", map[string]string{"env": "prod"}),
	}
	cases := []struct {
		name     string
		selector *metav1.LabelSelector
		want     []string
		err      bool
	}{
		{name: "no selector selects nothing"},
		{name: "matchLabels, sorted, without opted-out, terminating, kube-* or operator namespaces",
			selector: &metav1.LabelSelector{MatchLabels: map[string]string{"env": "preview"}}, want: []string{"pr-11", "pr-12"}},
		{name: "expressions",
			selector: &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{{Key: "team", Operator: metav1.LabelSelectorOpIn, Values: []string{"shop"}}}},
			want:     []string{"pr-12"}},
		{name: "an empty selector would take the whole cluster, so it selects nothing", selector: &metav1.LabelSelector{}, err: true},
		{name: "a selector that does not exclude anything still skips kube-* and the operator",
			selector: &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{{Key: "env", Operator: metav1.LabelSelectorOpExists}}},
			want:     []string{"pr-11", "pr-12", "prod"}},
		{name: "an invalid selector is an error",
			selector: &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{{Key: "team", Operator: metav1.LabelSelectorOpIn}}}, err: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := SelectNamespaces(c.selector, all, "costdeck")
			if (err != nil) != c.err {
				t.Fatalf("err = %v", err)
			}
			if !slices.Equal(got, c.want) {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}

func TestMembers(t *testing.T) {
	g := finopsv1.ScalingGroup{
		Spec:   finopsv1.ScalingGroupSpec{Namespaces: []string{"b", "a"}, NamespaceSelector: &metav1.LabelSelector{}},
		Status: finopsv1.ScalingGroupStatus{SelectedNamespaces: []string{"c", "a"}},
	}
	if got := g.Members(); !slices.Equal(got, []string{"a", "b", "c"}) {
		t.Errorf("members = %v", got)
	}
	// Without a selector, a stale status no longer counts.
	g.Spec.NamespaceSelector = nil
	if got := g.Members(); !slices.Equal(got, []string{"a", "b"}) {
		t.Errorf("members without a selector = %v", got)
	}
}

// A group listing a namespace by name owns it over a group selecting it, even an older one.
func TestOwnershipPrefersListedOverSelected(t *testing.T) {
	older := metav1.NewTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	newer := metav1.NewTime(older.Add(time.Hour))
	groups := []finopsv1.ScalingGroup{
		{Name: "previews", CreationTimestamp: older,
			Spec:   finopsv1.ScalingGroupSpec{NamespaceSelector: &metav1.LabelSelector{}},
			Status: finopsv1.ScalingGroupStatus{SelectedNamespaces: []string{"pr-1", "shop"}}},
		{Name: "shop", CreationTimestamp: newer,
			Spec: finopsv1.ScalingGroupSpec{Namespaces: []string{"shop"}}},
		{Name: "late-previews", CreationTimestamp: newer,
			Spec:   finopsv1.ScalingGroupSpec{NamespaceSelector: &metav1.LabelSelector{}},
			Status: finopsv1.ScalingGroupStatus{SelectedNamespaces: []string{"pr-1"}}},
	}
	plans := (&Engine{}).PlanGroups(time.Now(), groups, nil)
	if p := plans["previews"]; !slices.Equal(p.ConflictingNamespaces, []string{"shop"}) || p.NamespaceOwners["shop"] != "shop" {
		t.Errorf("previews = %+v, want shop owned by the group listing it", p)
	}
	if p := plans["shop"]; len(p.ConflictingNamespaces) != 0 {
		t.Errorf("shop = %+v", p)
	}
	if p := plans["late-previews"]; !slices.Equal(p.ConflictingNamespaces, []string{"pr-1"}) {
		t.Errorf("late-previews = %+v, want pr-1 left to the older selector", p)
	}
}

// A dependency in dry run leaves its workloads as they are, so dependents do not wait.
func TestDryRunDependencyCountsAsUp(t *testing.T) {
	groups := []finopsv1.ScalingGroup{
		{Name: "platform", Spec: finopsv1.ScalingGroupSpec{Namespaces: []string{"p"}, DryRun: true},
			Status: finopsv1.ScalingGroupStatus{Phase: PhaseDryRun}},
		{Name: "env", Spec: finopsv1.ScalingGroupSpec{Namespaces: []string{"e"}, DependsOn: []string{"platform"}}},
	}
	if p := (&Engine{}).PlanGroups(time.Now(), groups, nil)["env"]; len(p.WaitingFor) != 0 || p.BlockedOnDependencies() {
		t.Errorf("env waits for a dependency in dry run: %+v", p)
	}
}
