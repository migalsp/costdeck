package scaling

import (
	"time"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
)

// Idle auto-sleep takes a namespace down inside its schedule's hours once nobody has used
// it for a while: no requests through gateways or the mesh for the idle period, counted
// only after it has been up that long. It stays down until the schedule's next change,
// unless someone wakes it with a manual override.

// ModeIdle is a schedule's hours with the namespaces put to sleep for being unused.
const ModeIdle = "Idle"

// ApplyIdle turns an up decision of the schedule into down while idleUntil lasts. Manual
// overrides, exceptions, dependencies and the fail-safe keep their say. at tells what the
// timetable decides at a given time: the namespaces next come up when the sleep ends, or,
// if the schedule is down by then, when it next starts.
func ApplyIdle(d Decision, idleUntil *time.Time, now time.Time, at func(time.Time) Decision) Decision {
	if idleUntil == nil || !now.Before(*idleUntil) || d.Mode != ModeSchedule || !d.Active {
		return d
	}
	until := *idleUntil
	next := &until
	if then := at(until); !then.Active {
		next = then.NextTransition
	}
	return Decision{Active: false, Mode: ModeIdle, NextTransition: next}
}

// IdleUntil reads when a status's idle sleep ends, nil when it is not asleep.
func IdleUntil(st *finopsv1.ScheduleStatus) *time.Time {
	if st.IdleUntil == nil {
		return nil
	}
	t := st.IdleUntil.Time
	return &t
}

// IdleFor reports whether a namespace up since upSince has been unused long enough to
// sleep: up for the whole period with no requests in it. Unknown traffic never counts as
// idle.
func IdleFor(period time.Duration, upSince, now time.Time, requests float64, known bool) bool {
	return period > 0 && known && requests == 0 && !upSince.IsZero() && now.Sub(upSince) >= period
}
