package scaling

import (
	"fmt"
	"slices"
	"sort"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
)

// A schedule is suggested from a namespace's CPU usage history: the hours of the week it
// is busy, plus an hour on either side, become its windows.

// Reasons of a suggestion.
const (
	SuggestPattern      = "pattern"      // busy at some hours, idle at others: windows suggested
	SuggestSteady       = "steady"       // as busy at every hour: a schedule would cut into use
	SuggestIdle         = "idle"         // no activity at all
	SuggestInsufficient = "insufficient" // not enough history to tell
)

const (
	// minSuggestPoints is a day of hourly history.
	minSuggestPoints = 24
	// idleCores is usage below which a namespace does nothing (5 millicores).
	idleCores = 0.005
	// minBusySpan is how far busy hours must stand out from idle ones (10 millicores).
	minBusySpan = 0.01
	// busyShare places the busy threshold between the idle level (10th percentile) and the
	// busiest hour, so that even a short daily job counts as activity.
	busyShare = 0.25
	// steadyUpShare is the share of the week above which a schedule saves too little.
	steadyUpShare = 0.85
)

// Suggestion is a schedule worked out from usage.
type Suggestion struct {
	Reason    string                     `json:"reason"`
	Schedules []finopsv1.ScalingSchedule `json:"schedules"`
	// UpShare is the share of the week the suggested schedule is up.
	UpShare float64 `json:"upShare"`
	// Busy marks the hours of the week (Sunday first, in the timezone) with activity, Up
	// those the schedule keeps up.
	Busy [7][24]bool `json:"busy"`
	Up   [7][24]bool `json:"up"`
	// Days is how many days of history it rests on.
	Days     int    `json:"days"`
	Timezone string `json:"timezone"`
}

// SuggestSchedule works out working hours from CPU usage points, read in loc. A point
// averages the step before its timestamp, so it counts for the hour that step began in.
func SuggestSchedule(points []finopsv1.MetricDataPoint, step time.Duration, loc *time.Location) Suggestion {
	s := Suggestion{Reason: SuggestInsufficient, Schedules: []finopsv1.ScalingSchedule{}, Timezone: loc.String()}
	type sample struct {
		at    time.Time
		cores float64
	}
	var samples []sample
	for _, p := range points {
		q, err := resource.ParseQuantity(p.CPU.Usage)
		if err != nil {
			continue
		}
		samples = append(samples, sample{p.Timestamp.Add(-step).In(loc), q.AsApproximateFloat64()})
	}
	if len(samples) < minSuggestPoints {
		return s
	}
	s.Days = int(samples[len(samples)-1].at.Sub(samples[0].at).Hours()/24 + 0.5)

	values := make([]float64, len(samples))
	for i, smp := range samples {
		values[i] = smp.cores
	}
	sort.Float64s(values)
	low, high := percentile(values, 0.10), values[len(values)-1]
	switch {
	case high < idleCores:
		s.Reason = SuggestIdle
		return s
	case high-low < minBusySpan:
		s.Reason, s.UpShare = SuggestSteady, 1
		return s
	}
	threshold := low + busyShare*(high-low)
	for _, smp := range samples {
		if smp.cores > threshold {
			s.Busy[smp.at.Weekday()][smp.at.Hour()] = true
		}
	}
	// An hour of margin on either side: up before the first request, down after the last.
	up := 0
	for d := range 7 {
		for h := range 24 {
			hour := d*24 + h
			before, after := (hour+167)%168, (hour+1)%168
			if s.Busy[d][h] || s.Busy[before/24][before%24] || s.Busy[after/24][after%24] {
				s.Up[d][h] = true
				up++
			}
		}
	}
	s.UpShare = float64(up) / 168
	if s.UpShare > steadyUpShare {
		s.Reason = SuggestSteady
		return s
	}
	s.Reason = SuggestPattern
	s.Schedules = windows(s.Up, loc.String())
	return s
}

// windows turns the up hours of each day into schedules, one per distinct set of hours,
// shared by the days that have it.
func windows(up [7][24]bool, tz string) []finopsv1.ScalingSchedule {
	type span struct{ from, to int } // hours, to exclusive
	days := map[string][]int{}
	spans := map[string][]span{}
	var keys []string
	for d := range 7 {
		var day []span
		for h := 0; h < 24; h++ {
			if !up[d][h] {
				continue
			}
			start := h
			for h < 24 && up[d][h] {
				h++
			}
			day = append(day, span{start, h})
		}
		if len(day) == 0 {
			continue
		}
		key := fmt.Sprint(day)
		if _, ok := spans[key]; !ok {
			keys = append(keys, key)
			spans[key] = day
		}
		days[key] = append(days[key], d)
	}
	out := make([]finopsv1.ScalingSchedule, 0, len(keys))
	for _, key := range keys {
		for _, sp := range spans[key] {
			end := fmt.Sprintf("%02d:00", sp.to)
			if sp.to == 24 {
				end = "23:59"
			}
			out = append(out, finopsv1.ScalingSchedule{Days: slices.Clone(days[key]), StartTime: fmt.Sprintf("%02d:00", sp.from), EndTime: end, Timezone: tz})
		}
	}
	return out
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	i := int(p * float64(len(sorted)-1))
	return sorted[i]
}
