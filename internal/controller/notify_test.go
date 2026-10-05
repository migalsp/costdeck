package controller

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/scaling"
)

type recordingNotifier struct {
	mu   sync.Mutex
	msgs []string
	done chan struct{}
}

func (r *recordingNotifier) Notify(_ context.Context, m string) {
	r.mu.Lock()
	r.msgs = append(r.msgs, m)
	r.mu.Unlock()
	r.done <- struct{}{}
}

func TestAnnounceTransitionOnlyForFinishedScaling(t *testing.T) {
	n := &recordingNotifier{done: make(chan struct{}, 4)}
	st := finopsv1.ScheduleStatus{
		Mode:                   scaling.ModeSchedule,
		NextTransition:         &finopsv1.ScheduledTransition{Time: metav1.NewTime(time.Date(2026, 10, 5, 8, 0, 0, 0, time.UTC)), DesiredState: "Up"},
		EstimatedHourlySavings: "1.2400",
		Currency:               "USD",
	}
	ctx := context.Background()
	announceTransition(ctx, n, "Group", "pps1", "", scaling.PhaseScaledDown, st, nil)                        // first reconcile
	announceTransition(ctx, n, "Group", "pps1", scaling.PhaseScalingDown, scaling.PhaseScalingDown, st, nil) // no change
	announceTransition(ctx, n, "Group", "pps1", scaling.PhaseScaledUp, scaling.PhaseScalingDown, st, nil)    // not finished
	announceTransition(ctx, n, "Group", "pps1", scaling.PhaseScalingDown, scaling.PhaseScaledDown, st, nil)
	select {
	case <-n.done:
	case <-time.After(2 * time.Second):
		t.Fatal("no notification for a finished scale-down")
	}
	select {
	case <-n.done:
		t.Fatal("only the finished transition may be announced")
	case <-time.After(100 * time.Millisecond):
	}
	msg := n.msgs[0]
	for _, want := range []string{"Group `pps1` is scaled down by its schedule", "up at Mon 08:00 UTC", "Saving ~1.24 USD/h"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q does not contain %q", msg, want)
		}
	}
}

func TestTransitionMessageForDependencyAndOverride(t *testing.T) {
	until := metav1.NewTime(time.Date(2026, 10, 6, 18, 0, 0, 0, time.UTC))
	msg := transitionMessage("Group", "platform", scaling.PhaseScaledUp, finopsv1.ScheduleStatus{Mode: scaling.ModeDependency}, []string{"pps1", "stag1"})
	if !strings.Contains(msg, "is up for pps1, stag1") {
		t.Errorf("dependency message = %q", msg)
	}
	msg = transitionMessage("Namespace", "qa", scaling.PhaseScaledUp, finopsv1.ScheduleStatus{Mode: scaling.ModeManualUp, OverrideExpiresAt: &until}, nil)
	if !strings.Contains(msg, "manual override until Tue 18:00 UTC") {
		t.Errorf("override message = %q", msg)
	}
}
