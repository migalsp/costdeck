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
	var log transitionLog
	announce := func(old, phase string) {
		log.announce(ctx, n, "uid-pps1", "Group", "pps1", old, phase, st, nil)
	}
	announce("", scaling.PhaseScaledUp)                          // first reconcile
	announce(scaling.PhaseScalingDown, scaling.PhaseScalingDown) // no change
	announce(scaling.PhaseScaledUp, scaling.PhaseScalingDown)    // not finished
	announce(scaling.PhaseScalingDown, scaling.PhaseScaledDown)
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

// A group that is up and whose pod keeps restarting goes ScaledUp, ScalingUp, ScaledUp
// over and over; it is still the group that was announced as up, so nothing is posted.
func TestTransitionLogAnnouncesOnlyRealChanges(t *testing.T) {
	up, down := scaling.PhaseScaledUp, scaling.PhaseScaledDown
	goingUp, goingDown := scaling.PhaseScalingUp, scaling.PhaseScalingDown
	for _, tc := range []struct {
		name  string
		steps [][2]string // old phase, new phase
		want  []bool
	}{
		{"a pod restarting in a group that is up",
			[][2]string{{up, goingUp}, {goingUp, up}, {up, goingUp}, {goingUp, up}},
			[]bool{false, false, false, false}},
		{"scaling down and back up",
			[][2]string{{up, goingDown}, {goingDown, down}, {down, goingUp}, {goingUp, up}},
			[]bool{false, true, false, true}},
		{"a hiccup while down, then a real start",
			[][2]string{{down, goingDown}, {goingDown, down}, {down, scaling.PhaseWaitingForDependencies}, {scaling.PhaseWaitingForDependencies, goingUp}, {goingUp, up}},
			[]bool{false, false, false, false, true}},
		{"a transition the operator restarted in the middle of",
			[][2]string{{goingUp, up}},
			[]bool{true}},
		{"a new object that first has to scale", [][2]string{{"", goingDown}, {goingDown, down}}, []bool{false, true}},
		{"a new object already in place", [][2]string{{"", up}, {up, up}}, []bool{false, false}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var log transitionLog
			for i, s := range tc.steps {
				if got := log.changed("uid", s[0], s[1]); got != tc.want[i] {
					t.Errorf("step %d %s -> %s: announce = %v, want %v", i, s[0], s[1], got, tc.want[i])
				}
			}
		})
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
