package controller

import (
	"context"
	"slices"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/scaling"
)

func TestReleaseGivesUpOnlyAfterTenMinutes(t *testing.T) {
	deleted := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	for _, tt := range []struct {
		after time.Duration
		want  bool
	}{
		{0, false},
		{9*time.Minute + 59*time.Second, false},
		{10 * time.Minute, true},
		{time.Hour, true},
	} {
		if got := giveUpOnRelease(deleted, deleted.Add(tt.after)); got != tt.want {
			t.Errorf("%s after the deletion: give up = %v, want %v", tt.after, got, tt.want)
		}
	}
}

func TestGroupChangesReachTheConfigsAtOnce(t *testing.T) {
	scheme := runtime.NewScheme()
	utilruntime.Must(finopsv1.AddToScheme(scheme))
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		&finopsv1.ScalingConfig{Name: "config-a", Namespace: "costdeck", Spec: finopsv1.ScalingConfigSpec{TargetNamespace: "a"}},
		&finopsv1.ScalingConfig{Name: "config-b", Namespace: "costdeck", Spec: finopsv1.ScalingConfigSpec{TargetNamespace: "b"}},
		&finopsv1.ScalingConfig{Name: "elsewhere", Namespace: "other", Spec: finopsv1.ScalingConfigSpec{TargetNamespace: "c"}},
	).Build()
	r := &ScalingConfigReconciler{Client: c}
	group := &finopsv1.ScalingGroup{Name: "g", Namespace: "costdeck", Spec: finopsv1.ScalingGroupSpec{Namespaces: []string{"a"}}}
	queued := r.configsBesideGroup(context.Background(), group)
	names := make([]string, 0, len(queued))
	for _, req := range queued {
		names = append(names, req.Name)
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"config-a", "config-b"}) {
		t.Errorf("configs queued = %v; every config of the namespace, since a namespace that left the group is no longer in it", names)
	}

	before := group.DeepCopy()
	after := group.DeepCopy()
	after.Status.SelectedNamespaces = []string{"b"}
	if !selectionChanged().Update(event.UpdateEvent{ObjectOld: before, ObjectNew: after}) {
		t.Error("a selector that took a namespace did not reach the configs")
	}
	after.Status.SelectedNamespaces, after.Status.Phase = nil, "ScaledDown"
	if selectionChanged().Update(event.UpdateEvent{ObjectOld: before, ObjectNew: after}) {
		t.Error("every status write of a group queued all configs")
	}
}

func TestTheTransitionLogForgetsDeletedSchedules(t *testing.T) {
	var l transitionLog
	l.changed("uid-1", scaling.PhaseScaledUp, scaling.PhaseScaledDown)
	l.changed("uid-2", scaling.PhaseScaledUp, scaling.PhaseScaledDown)
	l.forget("uid-1")
	if _, kept := l.last["uid-1"]; kept || len(l.last) != 1 {
		t.Errorf("log = %v; a deleted schedule's entry must go", l.last)
	}
}
