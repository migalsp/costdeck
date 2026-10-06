package finops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/pricing"
)

// monthLedger has ten full days of October at 10/day for "api" (team payments) and
// 2/day for "dev-web", and a node bill of 20/day.
func monthLedger(now time.Time) *Ledger {
	l := &Ledger{Currency: "USD", Since: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}
	for d := 1; d <= now.Day(); d++ {
		hours := 24.0
		if d == now.Day() {
			hours = float64(now.Hour())
		}
		f := hours / 24
		l.Days = append(l.Days, Day{
			Date: fmt.Sprintf("2026-10-%02d", d), Hours: hours, Provisioned: 18 * f, Storage: 2 * f,
			Namespaces: map[string]NamespaceDay{"api": {Cost: 9 * f, Storage: 1 * f}, "dev-web": {Cost: 2 * f}},
		})
	}
	return l
}

func TestEvaluateBudgets(t *testing.T) {
	now := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	snap := Snapshot{At: now, Cluster: Cluster{ProvisionedHourly: 18.0 / 24, StorageHourly: 2.0 / 24},
		Namespaces: []Namespace{
			{Name: "api", Labels: map[string]string{"team": "payments"}, RequestedHourly: 9.0 / 24, StorageHourly: 1.0 / 24},
			{Name: "dev-web", RequestedHourly: 2.0 / 24},
		}}
	budgets := []finopsv1.Budget{
		{Name: "cluster", Scope: ScopeCluster, MonthlyLimit: "1000"},
		{Name: "payments", Scope: ScopeTeam, Value: "payments", MonthlyLimit: "250"},
		{Name: "dev", Scope: ScopeEnvironment, Value: EnvNonProduction, MonthlyLimit: "20"},
		{Name: "api", Scope: ScopeNamespace, Value: "api", MonthlyLimit: "500"},
	}
	st := EvaluateBudgets(budgets, snap, monthLedger(now))
	byName := map[string]BudgetStatus{}
	for _, s := range st {
		byName[s.Budget.Name] = s
	}
	// Ten days at 20 a day; 21 days to go at 20 a day.
	if c := byName["cluster"]; !near(c.Spent, 200) || !near(c.Forecast, 620) || c.State != BudgetOK {
		t.Errorf("cluster: %+v", c)
	}
	if p := byName["payments"]; !near(p.Spent, 100) || !near(p.Forecast, 310) || p.State != BudgetAtRisk || p.Namespaces[0] != "api" {
		t.Errorf("payments: %+v", p)
	}
	if d := byName["dev"]; !near(d.Spent, 20) || d.State != BudgetOver {
		t.Errorf("dev: %+v", d)
	}
	if a := byName["api"]; !near(a.SpentRatio(), 0.2) {
		t.Errorf("api: %+v", a)
	}
}

func TestBudgetAlertsFireHighestThreshold(t *testing.T) {
	now := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	st := []BudgetStatus{
		{Budget: finopsv1.Budget{Name: "dev", Forecast: true}, Limit: 100, Spent: 105, Forecast: 300},
		{Budget: finopsv1.Budget{Name: "pay", Forecast: true, Thresholds: []int{50, 90}}, Limit: 100, Spent: 60, Forecast: 130},
		{Budget: finopsv1.Budget{Name: "ok"}, Limit: 100, Spent: 10, Forecast: 50},
	}
	got := budgetAlerts(st, "USD", now)
	keys := make([]string, 0, len(got))
	for _, p := range got {
		keys = append(keys, p.key)
	}
	want := "budget/dev/2026-10/100 budget/pay/2026-10/50 forecast/pay/2026-10"
	if strings.Join(keys, " ") != want {
		t.Errorf("keys = %v, want %s", keys, want)
	}
	if len(got[0].also) != 1 || got[0].also[0] != "budget/dev/2026-10/80" {
		t.Errorf("reaching 100%% first must cover 80%%: %v", got[0].also)
	}
}

func TestAnomalyAlerts(t *testing.T) {
	now := time.Date(2026, 10, 11, 6, 0, 0, 0, time.UTC)
	l := &Ledger{}
	for d := 3; d <= 10; d++ {
		api := 10.0
		if d == 10 {
			api = 16 // +60% yesterday
		}
		l.Days = append(l.Days, Day{Date: fmt.Sprintf("2026-10-%02d", d), Hours: 24,
			Namespaces: map[string]NamespaceDay{"api": {Cost: api}, "tiny": {Cost: map[bool]float64{true: 0.5, false: 0.2}[d == 10]}}})
	}
	got := anomalyAlerts(finopsv1.AnomalyAlerts{Enabled: true}, l, "USD", now)
	if len(got) != 1 || got[0].key != "anomaly/api/2026-10-10" || !strings.Contains(got[0].event.Title, "60% more") {
		t.Fatalf("expected one anomaly for api (tiny is below the minimum), got %+v", got)
	}
	if got := anomalyAlerts(finopsv1.AnomalyAlerts{Enabled: true, Percent: 80}, l, "USD", now); len(got) != 0 {
		t.Errorf("60%% is below an 80%% threshold: %+v", got)
	}
	if got := anomalyAlerts(finopsv1.AnomalyAlerts{Enabled: false}, l, "USD", now); len(got) != 0 {
		t.Error("disabled anomaly alerts must not fire")
	}
}

type stubPoster struct {
	err  error
	sent []string
}

func (s *stubPoster) Post(_ context.Context, md string) error {
	if s.err != nil {
		return s.err
	}
	s.sent = append(s.sent, md)
	return nil
}

func TestAlerterFiresOnce(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	raw, _ := json.Marshal(monthLedger(now))
	cfg := &finopsv1.CostDeckConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: "costdeck"},
		Spec: finopsv1.CostDeckConfigSpec{Budgets: []finopsv1.Budget{
			{Name: "cluster", Scope: ScopeCluster, MonthlyLimit: "150"},
		}},
	}
	c := testClient(cfg, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: LedgerConfigMap, Namespace: "costdeck"}, Data: map[string]string{ledgerKey: string(raw)}})
	t.Setenv("POD_NAMESPACE", "costdeck")
	poster := &stubPoster{err: errors.New("webex is down")}
	a := &Alerter{Client: c, Pricing: &pricing.Resolver{Client: c}, Poster: poster, Namespace: "costdeck", Now: func() time.Time { return now }}

	// A failed delivery is not recorded as fired.
	if err := a.Check(ctx); err == nil {
		t.Fatal("a delivery failure must be reported")
	}
	if ev, _ := AlertEvents(ctx, c, "costdeck"); len(ev) != 0 {
		t.Fatalf("nothing should be recorded yet: %+v", ev)
	}
	poster.err = nil
	if err := a.Check(ctx); err != nil || len(poster.sent) != 1 || !strings.Contains(poster.sent[0], "100% of October") {
		t.Fatalf("expected the 100%% alert: %v %v", err, poster.sent)
	}
	if err := a.Check(ctx); err != nil || len(poster.sent) != 1 {
		t.Fatalf("an alert fires once: %v %d", err, len(poster.sent))
	}
	ev, _ := AlertEvents(ctx, c, "costdeck")
	if len(ev) != 1 || ev[0].Delivered != "webex" || ev[0].Budget != "cluster" {
		t.Errorf("feed: %+v", ev)
	}

	// Without a destination alerts are still recorded.
	poster.err = fmt.Errorf("%w: webex off", ErrNoDestination)
	a.Now = func() time.Time { return now.AddDate(0, 1, 0) }
	if err := a.Check(ctx); err != nil {
		t.Fatalf("no destination is not an error: %v", err)
	}
}
