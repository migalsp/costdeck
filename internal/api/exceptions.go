package api

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/config"
	"github.com/migalsp/costdeck-operator/internal/scaling"
)

// calendars returns the shared holiday calendars; none when the config cannot be read.
func (s *Server) calendars(ctx context.Context) []finopsv1.HolidayCalendar {
	cfg, err := config.Get(ctx, s.Client)
	if err != nil {
		return nil
	}
	return cfg.Spec.Calendars
}

// scheduleOf returns the fields of a group or config that decide when it runs.
func scheduleOf(obj client.Object) (schedules []finopsv1.ScalingSchedule, exceptions []finopsv1.ScheduleException, calendars []string) {
	switch o := obj.(type) {
	case *finopsv1.ScalingGroup:
		return o.Spec.Schedules, o.Spec.Exceptions, o.Spec.Calendars
	case *finopsv1.ScalingConfig:
		return o.Spec.Schedules, o.Spec.Exceptions, o.Spec.Calendars
	}
	panic(fmt.Sprintf("unexpected schedule owner %T", obj))
}

// timetableOf resolves when a group or config runs on its own: its schedules plus its
// exceptions and calendar days. Exceptions that cannot be applied are left out, as the
// controllers do.
func (s *Server) timetableOf(ctx context.Context, obj client.Object) scaling.Timetable {
	schedules, own, names := scheduleOf(obj)
	exceptions, _ := scaling.ResolveExceptions(own, names, s.calendars(ctx), scaling.ExceptionZone(schedules))
	return scaling.Timetable{Schedules: schedules, Exceptions: exceptions}
}

// validateSchedule rejects exceptions and calendar names that would not apply, so a typo
// is caught when saving instead of surfacing later in a status condition.
func (s *Server) validateSchedule(ctx context.Context, obj client.Object) error {
	if g, ok := obj.(*finopsv1.ScalingGroup); ok && g.Spec.NamespaceSelector != nil {
		// Matching against no namespaces checks the selector itself.
		if _, err := scaling.SelectNamespaces(g.Spec.NamespaceSelector, nil, ""); err != nil {
			return err
		}
	}
	schedules, own, names := scheduleOf(obj)
	if _, problems := scaling.ResolveExceptions(own, names, s.calendars(ctx), scaling.ExceptionZone(schedules)); len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

// validateCalendars checks the holiday calendars of a settings update and sorts their
// days, so the settings page and the API store the same thing.
func validateCalendars(calendars []finopsv1.HolidayCalendar) error {
	seen := map[string]bool{}
	for i := range calendars {
		c := &calendars[i]
		switch {
		case !budgetName.MatchString(c.Name) || len(c.Name) > 63:
			return badRequestf("calendar name %q must be lowercase letters, digits and dashes", c.Name)
		case seen[c.Name]:
			return badRequestf("there are two calendars named %q", c.Name)
		case c.State != "" && c.State != scaling.StateUp && c.State != scaling.StateDown:
			return badRequestf("calendar %q: state must be Up or Down", c.Name)
		case len(c.Days) > 400:
			return badRequestf("calendar %q: at most 400 days", c.Name)
		}
		if c.Timezone != "" {
			if _, err := time.LoadLocation(c.Timezone); err != nil {
				return badRequestf("calendar %q: unknown timezone %q", c.Name, c.Timezone)
			}
		}
		dates := map[string]bool{}
		for _, d := range c.Days {
			if _, err := time.Parse("2006-01-02", d.Date); err != nil {
				return badRequestf("calendar %q: %q is not a date like 2026-12-25", c.Name, d.Date)
			}
			if dates[d.Date] {
				return badRequestf("calendar %q lists %s twice", c.Name, d.Date)
			}
			if len(d.Name) > 100 {
				return badRequestf("calendar %q: the name of %s is too long", c.Name, d.Date)
			}
			dates[d.Date] = true
		}
		slices.SortFunc(c.Days, func(a, b finopsv1.CalendarDay) int { return strings.Compare(a.Date, b.Date) })
		seen[c.Name] = true
	}
	return nil
}

func applyCalendarSettings(_ context.Context, cfg *finopsv1.CostDeckConfig, req *SettingsUpdateRequest) error {
	if req.Calendars == nil {
		return nil
	}
	if err := validateCalendars(*req.Calendars); err != nil {
		return err
	}
	cfg.Spec.Calendars = *req.Calendars
	return nil
}

// changeExceptions edits the exceptions of a group (target "group") or of a namespace's
// config (target "namespace") and saves them if they would all apply.
func (s *Server) changeExceptions(ctx context.Context, target, name string, change func([]finopsv1.ScheduleException) ([]finopsv1.ScheduleException, string, error)) (string, error) {
	var obj client.Object
	switch target {
	case "group":
		obj = &finopsv1.ScalingGroup{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: config.OperatorNamespace()}}
	case "namespace":
		cfg, err := s.configForNamespace(ctx, name)
		if err != nil {
			return "", err
		}
		obj = cfg
	default:
		return "", errBadOverride{fmt.Errorf("target must be group or namespace")}
	}
	var done string
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := s.Client.Get(ctx, client.ObjectKeyFromObject(obj), obj); err != nil {
			return err
		}
		field := exceptionsField(obj)
		updated, msg, err := change(*field)
		if err != nil {
			return err
		}
		*field, done = updated, msg
		if err := s.validateSchedule(ctx, obj); err != nil {
			return errBadOverride{err}
		}
		return s.Client.Update(ctx, obj)
	})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s %s: %s.", target, name, done), nil
}

func exceptionsField(obj client.Object) *[]finopsv1.ScheduleException {
	switch o := obj.(type) {
	case *finopsv1.ScalingGroup:
		return &o.Spec.Exceptions
	case *finopsv1.ScalingConfig:
		return &o.Spec.Exceptions
	}
	panic(fmt.Sprintf("unexpected schedule owner %T", obj))
}

// describeExceptionDates says when an exception applies, for tool results.
func describeExceptionDates(ex finopsv1.ScheduleException) string {
	when := "on " + ex.Start
	if ex.End != "" && ex.End != ex.Start {
		when = "from " + ex.Start + " to " + ex.End
	}
	if ex.Name != "" {
		when += " (" + ex.Name + ")"
	}
	return when
}

// toolListCalendars lists the shared holiday calendars with their days still to come.
func (s *Server) toolListCalendars(ctx context.Context) (string, error) {
	today := time.Now().UTC().Format("2006-01-02")
	calendars := s.calendars(ctx)
	out := make([]map[string]any, 0, len(calendars))
	for _, c := range calendars {
		state := c.State
		if state == "" {
			state = scaling.StateDown
		}
		upcoming := []finopsv1.CalendarDay{}
		for _, d := range c.Days {
			if d.Date >= today {
				upcoming = append(upcoming, d)
			}
		}
		out = append(out, map[string]any{"name": c.Name, "timezone": c.Timezone, "keeps": state, "days": len(c.Days), "upcoming": upcoming})
	}
	return toJSON(out)
}
