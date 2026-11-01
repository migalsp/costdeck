package scaling

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
)

// sharedWindowCases is testdata/windows.json, which the dashboard's schedule.test.ts
// evaluates too: the week grid in the UI must match what the operator does.
type sharedWindowCases struct {
	Cases []struct {
		Name        string                     `json:"name"`
		LeadMinutes int                        `json:"leadMinutes"`
		Schedules   []finopsv1.ScalingSchedule `json:"schedules"`
		Up          []string                   `json:"up"`
		Down        []string                   `json:"down"`
	} `json:"cases"`
}

// atWeekMinute parses "Mon 08:00" into a time in UTC in the week of Sunday 2026-10-18.
func atWeekMinute(t *testing.T, s string) time.Time {
	t.Helper()
	var day string
	var h, m int
	if _, err := fmt.Sscanf(s, "%s %d:%d", &day, &h, &m); err != nil {
		t.Fatalf("bad time %q: %v", s, err)
	}
	idx := strings.Index("SunMonTueWedThuFriSat", day)
	if idx < 0 || idx%3 != 0 {
		t.Fatalf("bad weekday in %q", s)
	}
	return time.Date(2026, 10, 18+idx/3, h, m, 0, 0, time.UTC)
}

func TestSharedWindowCases(t *testing.T) {
	raw, err := os.ReadFile("testdata/windows.json")
	if err != nil {
		t.Fatal(err)
	}
	var data sharedWindowCases
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	for _, c := range data.Cases {
		t.Run(c.Name, func(t *testing.T) {
			for i := range c.Schedules {
				c.Schedules[i].Timezone = "UTC"
			}
			tt := Timetable{Schedules: c.Schedules, Lead: time.Duration(c.LeadMinutes) * time.Minute}
			for _, at := range c.Up {
				if !tt.ActiveAt(atWeekMinute(t, at)) {
					t.Errorf("%s: want up, got down", at)
				}
			}
			for _, at := range c.Down {
				if tt.ActiveAt(atWeekMinute(t, at)) {
					t.Errorf("%s: want down, got up", at)
				}
			}
		})
	}
}
