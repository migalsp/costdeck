package scaling

import (
	"fmt"
	"slices"
	"strings"
	"time"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
)

const (
	exceptionDate     = "2006-01-02"
	exceptionDateTime = "2006-01-02T15:04"
	// defaultExceptionZone applies when neither the exception nor a schedule names one.
	defaultExceptionZone = "UTC"
)

// Exception is a dated override of the weekly schedules resolved to the half-open
// interval [From, To): a holiday the target stays down, or a release weekend it stays up.
type Exception struct {
	Name   string
	From   time.Time
	To     time.Time
	Active bool
}

// ResolveExceptions turns a group's or config's exceptions and the days of the calendars
// it names into intervals. Entries without a timezone count in defaultZone (the first
// schedule's), else UTC. What cannot be resolved (a missing calendar, an end before the
// start) comes back as a problem for the status, and the rest still applies. Calendar
// days come first and the object's own exceptions after them, so on equal starts its own
// exception wins.
func ResolveExceptions(own []finopsv1.ScheduleException, names []string, calendars []finopsv1.HolidayCalendar, defaultZone string) ([]Exception, []string) {
	var out []Exception
	var problems []string
	for _, name := range names {
		i := slices.IndexFunc(calendars, func(c finopsv1.HolidayCalendar) bool { return c.Name == name })
		if i < 0 {
			problems = append(problems, fmt.Sprintf("calendar %q does not exist", name))
			continue
		}
		cal := calendars[i]
		state := cal.State
		if state == "" {
			state = StateDown
		}
		for _, day := range cal.Days {
			label := day.Name
			if label == "" {
				label = day.Date
			}
			ex, err := resolveException(finopsv1.ScheduleException{Name: label, Start: day.Date, State: state, Timezone: cal.Timezone}, defaultZone)
			if err != nil {
				problems = append(problems, fmt.Sprintf("calendar %q: %v", name, err))
				continue
			}
			out = append(out, ex)
		}
	}
	for _, se := range own {
		ex, err := resolveException(se, defaultZone)
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		out = append(out, ex)
	}
	return out, problems
}

// ExceptionZone is the timezone exceptions default to: the first schedule's, else UTC.
func ExceptionZone(schedules []finopsv1.ScalingSchedule) string {
	for _, s := range schedules {
		if s.Timezone != "" {
			return s.Timezone
		}
	}
	return defaultExceptionZone
}

func resolveException(se finopsv1.ScheduleException, defaultZone string) (Exception, error) {
	label := se.Name
	if label == "" {
		label = se.Start
	}
	zone := se.Timezone
	if zone == "" {
		zone = defaultZone
	}
	if zone == "" {
		zone = defaultExceptionZone
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return Exception{}, fmt.Errorf("exception %q: unknown timezone %q", label, zone)
	}
	from, _, err := parseExceptionTime(se.Start, loc)
	if err != nil {
		return Exception{}, fmt.Errorf("exception %q: start %q is not a date or date and time", label, se.Start)
	}
	// Without an end the exception lasts to the end of its start day; an end date
	// includes that whole day.
	endOfDay := func(t time.Time) time.Time {
		y, m, d := t.Date()
		return time.Date(y, m, d+1, 0, 0, 0, 0, loc)
	}
	to := endOfDay(from)
	if end := strings.TrimSpace(se.End); end != "" {
		t, isDate, err := parseExceptionTime(end, loc)
		if err != nil {
			return Exception{}, fmt.Errorf("exception %q: end %q is not a date or date and time", label, se.End)
		}
		to = t
		if isDate {
			to = endOfDay(t)
		}
	}
	if !to.After(from) {
		return Exception{}, fmt.Errorf("exception %q: ends before it starts", label)
	}
	var active bool
	switch se.State {
	case StateUp:
		active = true
	case StateDown:
	default:
		return Exception{}, fmt.Errorf("exception %q: state must be Up or Down, not %q", label, se.State)
	}
	return Exception{Name: label, From: from, To: to, Active: active}, nil
}

// parseExceptionTime reads "2026-12-24" or "2026-12-24T18:00" in loc and says which it was.
func parseExceptionTime(s string, loc *time.Location) (time.Time, bool, error) {
	s = strings.TrimSpace(s)
	if t, err := time.ParseInLocation(exceptionDate, s, loc); err == nil {
		return t, true, nil
	}
	t, err := time.ParseInLocation(exceptionDateTime, s, loc)
	return t, false, err
}

// Timetable is when a target runs on its own: the weekly schedules and the dated
// exceptions to them. Manual overrides and dependencies are decided on top of it.
type Timetable struct {
	Schedules  []finopsv1.ScalingSchedule
	Exceptions []Exception
}

// exceptionAt returns the exception in force at t: of those covering t, the one that
// began last (later in the list on equal starts), or nil.
func (tt Timetable) exceptionAt(t time.Time) *Exception {
	var found *Exception
	for i := range tt.Exceptions {
		ex := &tt.Exceptions[i]
		if t.Before(ex.From) || !t.Before(ex.To) {
			continue
		}
		if found == nil || !ex.From.Before(found.From) {
			found = ex
		}
	}
	return found
}

// ActiveAt says whether the target should be up at t.
func (tt Timetable) ActiveAt(t time.Time) bool {
	if ex := tt.exceptionAt(t); ex != nil {
		return ex.Active
	}
	return (&Engine{}).IsActiveAt(t, tt.Schedules, nil)
}

// NextChange is the first minute after t at which ActiveAt flips, or nil if it never
// does. The weekly schedules repeat, so a change they cause shows within a week; beyond
// that only the start or end of an exception can still change the state.
func (tt Timetable) NextChange(t time.Time) *time.Time {
	usable := hasUsableSchedule(tt.Schedules)
	if !usable && len(tt.Exceptions) == 0 {
		return nil
	}
	current := tt.ActiveAt(t)
	if usable {
		at := t.Truncate(time.Minute)
		for range scanHorizon {
			at = at.Add(time.Minute)
			if tt.ActiveAt(at) != current {
				return &at
			}
		}
	}
	var bounds []time.Time
	for _, ex := range tt.Exceptions {
		for _, b := range []time.Time{ex.From, ex.To} {
			if b.After(t) {
				bounds = append(bounds, b)
			}
		}
	}
	slices.SortFunc(bounds, func(a, b time.Time) int { return a.Compare(b) })
	for _, b := range bounds {
		if tt.ActiveAt(b) != current {
			return &b
		}
	}
	return nil
}

// Interval is a stretch of time in which a timetable keeps a target in one state, and why:
// "schedule" or the name of the exception in force.
type Interval struct {
	From   time.Time `json:"from"`
	To     time.Time `json:"to"`
	Active bool      `json:"active"`
	Reason string    `json:"reason"`
}

// maxIntervals bounds Intervals for timetables that change every minute.
const maxIntervals = 2000

// Intervals splits [from, to) into stretches of one state and one reason, for previews.
func (tt Timetable) Intervals(from, to time.Time) []Interval {
	reason := func(t time.Time) string {
		if ex := tt.exceptionAt(t); ex != nil {
			return ex.Name
		}
		return "schedule"
	}
	var out []Interval
	start := from
	for start.Before(to) && len(out) < maxIntervals {
		active, why := tt.ActiveAt(start), reason(start)
		end := to
		// The state changes at NextChange; the reason can also change at an exception
		// boundary that keeps the state (a holiday on a day that is down anyway).
		if next := tt.NextChange(start); next != nil && next.Before(end) {
			end = *next
		}
		for _, ex := range tt.Exceptions {
			for _, b := range []time.Time{ex.From, ex.To} {
				if b.After(start) && b.Before(end) && reason(b) != why {
					end = b
				}
			}
		}
		out = append(out, Interval{From: start, To: end, Active: active, Reason: why})
		start = end
	}
	return out
}

// ActiveShare is the share of [from, to) in which the timetable keeps the target up.
func (tt Timetable) ActiveShare(from, to time.Time) float64 {
	total := to.Sub(from)
	if total <= 0 {
		return 0
	}
	var up time.Duration
	for _, iv := range tt.Intervals(from, to) {
		if iv.Active {
			up += iv.To.Sub(iv.From)
		}
	}
	return float64(up) / float64(total)
}
