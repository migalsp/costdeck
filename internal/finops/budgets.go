package finops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/config"
	"github.com/migalsp/costdeck-operator/internal/pricing"
	"github.com/migalsp/costdeck-operator/internal/telemetry"
)

// Budgets turn the ledger into accountability: what a team or namespace may spend in a
// month, how far it is, and where the month is heading. Alerts fire once per threshold
// per month, so a budget pages at 80% and at 100%, not every ten minutes.

// Budget scopes and states.
const (
	ScopeCluster     = "cluster"
	ScopeNamespace   = "namespace"
	ScopeTeam        = "team"
	ScopeEnvironment = "environment"

	BudgetOK     = "ok"
	BudgetAtRisk = "at-risk"
	BudgetOver   = "over"
)

const (
	AlertsConfigMap = "costdeck-alerts"
	alertStateKey   = "state.json"
	alertEventsKey  = "events.json"
	maxAlertEvents  = 100
	// firedRetention bounds how long fired alert keys are remembered.
	firedRetention = 62 * 24 * time.Hour

	defaultAnomalyPercent = 30
	anomalyBaselineDays   = 7
	anomalyMinBaseline    = 3
	completeDayHours      = 23
	alertInterval         = 10 * time.Minute
)

var defaultThresholds = []int{80, 100}

// BudgetStatus is one budget against the current month.
type BudgetStatus struct {
	Budget   finopsv1.Budget `json:"budget"`
	Limit    float64         `json:"limit"`
	Spent    float64         `json:"spent"`
	Forecast float64         `json:"forecast"`
	// HourlyNow is what the scope costs per hour at the moment.
	HourlyNow  float64  `json:"hourlyNow"`
	State      string   `json:"state"`
	Namespaces []string `json:"namespaces"`
	// Partial is true when the cost history does not cover the whole month so far.
	Partial bool `json:"partial"`
}

// SpentRatio is spending as a share of the limit.
func (b BudgetStatus) SpentRatio() float64 {
	if b.Limit <= 0 {
		return 0
	}
	return b.Spent / b.Limit
}

// Thresholds returns the budget's alert thresholds, sorted.
func Thresholds(b finopsv1.Budget) []int {
	t := b.Thresholds
	if len(t) == 0 {
		t = defaultThresholds
	}
	out := append([]int(nil), t...)
	sort.Ints(out)
	return out
}

// matcher says which namespaces a budget covers.
func matcher(b finopsv1.Budget, s Snapshot) func(name string) bool {
	labels := map[string]map[string]string{}
	for _, ns := range s.Namespaces {
		labels[ns.Name] = ns.Labels
	}
	switch b.Scope {
	case ScopeNamespace:
		return func(name string) bool { return name == b.Value }
	case ScopeTeam:
		return func(name string) bool { l, ok := labels[name]; return ok && Team(l) == b.Value }
	case ScopeEnvironment:
		return func(name string) bool { return Environment(name, labels[name]) == b.Value }
	}
	return func(string) bool { return true }
}

// EvaluateBudgets measures every budget against the month so far and forecasts the rest
// of the month at the current hourly cost.
func EvaluateBudgets(budgets []finopsv1.Budget, s Snapshot, l *Ledger) []BudgetStatus {
	now := s.At.UTC()
	start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	remaining := start.AddDate(0, 1, 0).Sub(now).Hours()
	prefix := start.Format("2006-01") + "-"
	var hours float64
	if l != nil {
		for _, d := range l.Days {
			if d.Date >= prefix && d.Date < prefix+"99" {
				hours += d.Hours
			}
		}
	}
	out := make([]BudgetStatus, 0, len(budgets))
	for _, b := range budgets {
		st := BudgetStatus{Budget: b, Namespaces: []string{}, Partial: hours < now.Sub(start).Hours()-1}
		st.Limit, _ = strconv.ParseFloat(b.MonthlyLimit, 64)
		match := matcher(b, s)
		if l != nil {
			for _, d := range l.Days {
				if d.Date < prefix || d.Date >= prefix+"99" {
					continue
				}
				if b.Scope == ScopeCluster {
					st.Spent += d.Bill()
					continue
				}
				for ns, v := range d.Namespaces {
					if match(ns) {
						st.Spent += v.Total()
					}
				}
			}
		}
		if b.Scope == ScopeCluster {
			st.HourlyNow = s.Cluster.BillHourly()
		} else {
			for _, ns := range s.Namespaces {
				if match(ns.Name) {
					st.HourlyNow += ns.TotalHourly()
					st.Namespaces = append(st.Namespaces, ns.Name)
				}
			}
		}
		st.Forecast = st.Spent + st.HourlyNow*remaining
		switch {
		case st.Limit > 0 && st.Spent >= st.Limit:
			st.State = BudgetOver
		case st.Limit > 0 && st.Forecast > st.Limit:
			st.State = BudgetAtRisk
		default:
			st.State = BudgetOK
		}
		out = append(out, st)
	}
	return out
}

// AlertEvent is one alert, kept for the dashboard's feed.
type AlertEvent struct {
	At        time.Time `json:"at"`
	Kind      string    `json:"kind"` // budget, forecast, anomaly
	Title     string    `json:"title"`
	Detail    string    `json:"detail"`
	Budget    string    `json:"budget,omitempty"`
	Namespace string    `json:"namespace,omitempty"`
	// Delivered names where the alert was sent; empty when it was only recorded.
	Delivered string `json:"delivered,omitempty"`
}

// DeliveredWebex marks alerts and digests posted to the Webex space.
const DeliveredWebex = "webex"

// Alert kinds.
const (
	AlertBudget   = "budget"
	AlertForecast = "forecast"
	AlertAnomaly  = "anomaly"
)

// pendingAlert is an alert with the key that stops it from firing twice, and the keys of
// lower thresholds it makes redundant.
type pendingAlert struct {
	key   string
	also  []string
	event AlertEvent
}

// budgetAlerts returns the threshold and forecast alerts the statuses call for.
func budgetAlerts(statuses []BudgetStatus, currency string, now time.Time) []pendingAlert {
	month := now.UTC().Format("2006-01")
	monthName := now.UTC().Format("January")
	m := func(v float64) string { return money(v, currency) }
	var out []pendingAlert
	for _, st := range statuses {
		if st.Limit <= 0 {
			continue
		}
		name := st.Budget.Name
		reached := 0
		for _, t := range Thresholds(st.Budget) {
			if st.Spent >= st.Limit*float64(t)/100 {
				reached = t
			}
		}
		if reached > 0 {
			// One alert for the highest threshold reached; lower ones are marked as fired too,
			// so reaching 100% first never sends the 80% alert afterwards.
			var lower []string
			for _, t := range Thresholds(st.Budget) {
				if t < reached {
					lower = append(lower, fmt.Sprintf("budget/%s/%s/%d", name, month, t))
				}
			}
			out = append(out, pendingAlert{
				key: fmt.Sprintf("budget/%s/%s/%d", name, month, reached), also: lower,
				event: AlertEvent{
					At: now, Kind: AlertBudget, Budget: name,
					Title:  fmt.Sprintf("Budget %s: %d%% of %s used", name, reached, monthName),
					Detail: fmt.Sprintf("%s spent of %s; at the current rate the month ends at %s.", m(st.Spent), m(st.Limit), m(st.Forecast)),
				},
			})
		}
		if st.Budget.Forecast && st.Spent < st.Limit && st.Forecast > st.Limit {
			out = append(out, pendingAlert{
				key: fmt.Sprintf("forecast/%s/%s", name, month),
				event: AlertEvent{
					At: now, Kind: AlertForecast, Budget: name,
					Title:  fmt.Sprintf("Budget %s is heading for %s, over its %s limit", name, m(st.Forecast), m(st.Limit)),
					Detail: fmt.Sprintf("%s spent so far this month at %s an hour.", m(st.Spent), m(st.HourlyNow)),
				},
			})
		}
	}
	return out
}

// anomalyAlerts compares each namespace's last complete day with the average of the seven
// complete days before it.
func anomalyAlerts(cfg finopsv1.AnomalyAlerts, l *Ledger, currency string, now time.Time) []pendingAlert {
	if !cfg.Enabled || l == nil {
		return nil
	}
	percent := cfg.Percent
	if percent <= 0 {
		percent = defaultAnomalyPercent
	}
	minimum, err := strconv.ParseFloat(cfg.MinimumDaily, 64)
	if err != nil || cfg.MinimumDaily == "" {
		minimum = 1
	}
	today := DateOf(now)
	var complete []Day
	for _, d := range l.Days {
		if d.Date < today && d.Hours >= completeDayHours {
			complete = append(complete, d)
		}
	}
	if len(complete) < anomalyMinBaseline+1 {
		return nil
	}
	last := complete[len(complete)-1]
	if last.Date != DateOf(now.AddDate(0, 0, -1)) {
		return nil // only yesterday is news
	}
	base := complete[max(0, len(complete)-1-anomalyBaselineDays) : len(complete)-1]
	var out []pendingAlert
	for ns, v := range last.Namespaces {
		var sum float64
		for _, d := range base {
			sum += d.Namespaces[ns].Total()
		}
		avg := sum / float64(len(base))
		cost := v.Total()
		if avg <= 0 || cost < avg*(1+float64(percent)/100) || cost-avg < minimum {
			continue
		}
		out = append(out, pendingAlert{
			key: fmt.Sprintf("anomaly/%s/%s", ns, last.Date),
			event: AlertEvent{
				At: now, Kind: AlertAnomaly, Namespace: ns,
				Title:  fmt.Sprintf("%s cost %.0f%% more than usual on %s", ns, (cost/avg-1)*100, last.Date),
				Detail: fmt.Sprintf("%s that day against an average of %s over the %d days before. Check what was scaled up, added or left running.", money(cost, currency), money(avg, currency), len(base)),
			},
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].key < out[j].key })
	return out
}

// alertState is what the alerter remembers between runs.
type alertState struct {
	Fired map[string]time.Time `json:"fired"`
}

// AlertEvents returns the recent alerts, newest first.
func AlertEvents(ctx context.Context, c client.Reader, namespace string) ([]AlertEvent, error) {
	var cm corev1.ConfigMap
	if err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: AlertsConfigMap}, &cm); err != nil {
		if apierrors.IsNotFound(err) {
			return []AlertEvent{}, nil
		}
		return nil, err
	}
	events := []AlertEvent{}
	_ = json.Unmarshal([]byte(cm.Data[alertEventsKey]), &events)
	return events, nil
}

// Alerter evaluates budgets and anomalies and sends what fires. It runs on the leader.
type Alerter struct {
	Client    client.Client
	Live      client.Reader
	Pricing   *pricing.Resolver
	Poster    Poster
	Namespace string
	Now       func() time.Time
}

// NeedLeaderElection implements manager.LeaderElectionRunnable.
func (a *Alerter) NeedLeaderElection() bool { return true }

// Start implements manager.Runnable.
func (a *Alerter) Start(ctx context.Context) error {
	ticker := time.NewTicker(alertInterval)
	defer ticker.Stop()
	for {
		if err := a.Check(ctx); err != nil {
			logf.FromContext(ctx).WithName("alerts").Error(err, "Could not check budgets and anomalies")
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// Check evaluates everything once, sends new alerts and records them.
func (a *Alerter) Check(ctx context.Context) error {
	now := time.Now()
	if a.Now != nil {
		now = a.Now()
	}
	cfg, err := config.Get(ctx, a.Client)
	if err != nil {
		return err
	}
	if len(cfg.Spec.Budgets) == 0 && !cfg.Spec.Alerts.Anomalies.Enabled {
		return nil
	}
	rates := a.Pricing.Rates(ctx)
	snap, err := Build(ctx, a.Client, rates, Options{Namespace: a.Namespace, Now: now, Live: a.Live})
	if err != nil {
		return err
	}
	ledger, err := LoadLedger(ctx, a.Client, a.Namespace)
	if err != nil {
		return err
	}
	statuses := EvaluateBudgets(cfg.Spec.Budgets, snap, ledger)
	for _, st := range statuses {
		telemetry.RecordBudget(st.Budget.Name, st.SpentRatio(), safeRatio(st.Forecast, st.Limit))
	}
	telemetry.KeepBudgets(budgetNames(cfg.Spec.Budgets))

	pending := append(budgetAlerts(statuses, rates.Currency, now), anomalyAlerts(cfg.Spec.Alerts.Anomalies, ledger, rates.Currency, now)...)
	if len(pending) == 0 {
		return nil
	}
	return a.fire(ctx, pending, now)
}

func budgetNames(bs []finopsv1.Budget) []string {
	out := make([]string, 0, len(bs))
	for _, b := range bs {
		out = append(out, b.Name)
	}
	return out
}

func safeRatio(a, b float64) float64 {
	if b <= 0 {
		return 0
	}
	return a / b
}

// fire sends the alerts that have not fired yet and records them.
func (a *Alerter) fire(ctx context.Context, pending []pendingAlert, now time.Time) error {
	var cm corev1.ConfigMap
	err := a.Client.Get(ctx, client.ObjectKey{Namespace: a.Namespace, Name: AlertsConfigMap}, &cm)
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	state := alertState{Fired: map[string]time.Time{}}
	_ = json.Unmarshal([]byte(cm.Data[alertStateKey]), &state)
	if state.Fired == nil {
		state.Fired = map[string]time.Time{}
	}
	var fresh []AlertEvent
	var firstErr error
	for _, p := range pending {
		if _, done := state.Fired[p.key]; done {
			continue
		}
		ev := p.event
		md := fmt.Sprintf("**%s**\n%s", ev.Title, ev.Detail)
		if a.Poster != nil {
			if err := a.Poster.Post(ctx, md); err == nil {
				ev.Delivered = DeliveredWebex
			} else if !errors.Is(err, ErrNoDestination) {
				// Not marked as fired: the next check tries again.
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
		}
		state.Fired[p.key] = now
		for _, k := range p.also {
			state.Fired[k] = now
		}
		fresh = append(fresh, ev)
	}
	if len(fresh) == 0 {
		return firstErr
	}
	for k, t := range state.Fired {
		if now.Sub(t) > firedRetention {
			delete(state.Fired, k)
		}
	}
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var cur corev1.ConfigMap
		getErr := a.Client.Get(ctx, client.ObjectKey{Namespace: a.Namespace, Name: AlertsConfigMap}, &cur)
		create := apierrors.IsNotFound(getErr)
		if getErr != nil && !create {
			return getErr
		}
		events := []AlertEvent{}
		_ = json.Unmarshal([]byte(cur.Data[alertEventsKey]), &events)
		events = append(fresh, events...)
		if len(events) > maxAlertEvents {
			events = events[:maxAlertEvents]
		}
		rawEvents, _ := json.Marshal(events)
		rawState, _ := json.Marshal(state)
		if create {
			cur = corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
				Name: AlertsConfigMap, Namespace: a.Namespace, Labels: map[string]string{"app.kubernetes.io/managed-by": "costdeck"},
			}}
		}
		if cur.Data == nil {
			cur.Data = map[string]string{}
		}
		cur.Data[alertEventsKey], cur.Data[alertStateKey] = string(rawEvents), string(rawState)
		if create {
			return a.Client.Create(ctx, &cur)
		}
		return a.Client.Update(ctx, &cur)
	})
	if err != nil {
		return err
	}
	return firstErr
}

// ErrNoDestination is returned by a Poster with nowhere to post; the alert is then only
// recorded in the dashboard's feed.
var ErrNoDestination = errors.New("no destination configured")
