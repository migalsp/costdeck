package controller

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/scaling"
)

func TestTrackWakeUp(t *testing.T) {
	start := time.Date(2026, 11, 2, 8, 0, 0, 0, time.UTC)
	var st finopsv1.ScheduleStatus
	trackWakeUp(&st, scaling.PhaseScaledDown, scaling.PhaseWaitingForDependencies, start)
	trackWakeUp(&st, scaling.PhaseWaitingForDependencies, scaling.PhaseScalingUp, start.Add(30*time.Second))
	trackWakeUp(&st, scaling.PhaseScalingUp, scaling.PhaseScaledUp, start.Add(150*time.Second))
	if st.WakeUpStartedAt != nil || len(st.RecentWakeUps) != 1 || st.RecentWakeUps[0].Seconds != 150 || !st.RecentWakeUps[0].StartedAt.Equal(&metav1.Time{Time: start}) {
		t.Fatalf("status = %+v, want one wake-up of 150s counted from leaving ScaledDown", st)
	}

	trackWakeUp(&st, scaling.PhaseScaledDown, scaling.PhaseScalingUp, start.Add(time.Hour))
	trackWakeUp(&st, scaling.PhaseScalingUp, scaling.PhaseScalingDown, start.Add(time.Hour+time.Minute))
	trackWakeUp(&st, scaling.PhaseScalingDown, scaling.PhaseScaledUp, start.Add(2*time.Hour))
	if len(st.RecentWakeUps) != 1 || st.WakeUpStartedAt != nil {
		t.Fatalf("status = %+v: a scale-up that turned back down before it was ready is no wake-up", st)
	}

	for i := range 6 {
		at := start.Add(time.Duration(i+3) * time.Hour)
		trackWakeUp(&st, scaling.PhaseScaledDown, scaling.PhaseScalingUp, at)
		trackWakeUp(&st, scaling.PhaseScalingUp, scaling.PhaseScaledUp, at.Add(time.Duration(i+1)*time.Minute))
	}
	if len(st.RecentWakeUps) != recentWakeUps || st.RecentWakeUps[0].Seconds != 360 || st.RecentWakeUps[4].Seconds != 120 {
		t.Errorf("recent wake-ups = %+v, want the last five, newest first", st.RecentWakeUps)
	}

	var direct finopsv1.ScheduleStatus
	trackWakeUp(&direct, scaling.PhaseScaledDown, scaling.PhaseScaledUp, start)
	if len(direct.RecentWakeUps) != 0 {
		t.Errorf("a scale-up never seen in progress is not measured, got %+v", direct.RecentWakeUps)
	}
}
