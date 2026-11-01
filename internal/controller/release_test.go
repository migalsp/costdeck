package controller

import (
	"testing"
	"time"
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
