package finops

import (
	"context"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/pricing"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

func TestEnvironment(t *testing.T) {
	cases := []struct {
		name   string
		labels map[string]string
		want   string
	}{
		{"shop-prod", nil, EnvProduction},
		{"payments", map[string]string{"environment": "production"}, EnvProduction},
		{"dev-backend", nil, EnvNonProduction},
		{"team-a-staging", nil, EnvNonProduction},
		{"qa", nil, EnvNonProduction},
		{"devops-tools", nil, EnvNonProduction},
		{"kube-system", nil, EnvSystem},
		{"gatekeeper-system", nil, EnvSystem},
		{"payments", nil, EnvUnknown},
		// A label wins over the name.
		{"dev-mirror", map[string]string{"env": "prod"}, EnvProduction},
		// An unrecognised label value falls back to the name.
		{"dev-x", map[string]string{"env": "blue"}, EnvNonProduction},
	}
	for _, c := range cases {
		if got := Environment(c.name, c.labels); got != c.want {
			t.Errorf("Environment(%q, %v) = %q, want %q", c.name, c.labels, got, c.want)
		}
	}
	if got := Team(map[string]string{"app.kubernetes.io/part-of": "shop", "team": "payments"}); got != "payments" {
		t.Errorf("Team prefers the team label, got %q", got)
	}
}

func snapAt(at time.Time, provisioned float64, ns map[string]float64, saved float64) Snapshot {
	s := Snapshot{At: at, Rates: pricing.Rates{Currency: "USD"}, Cluster: Cluster{ProvisionedHourly: provisioned}}
	for name, cost := range ns {
		s.Namespaces = append(s.Namespaces, Namespace{Name: name, RequestedHourly: cost, UsedHourly: cost / 2})
		s.Cluster.RequestedHourly += cost
		s.Cluster.UsedHourly += cost / 2
	}
	if saved > 0 {
		s.Schedules = []Schedule{{Kind: "group", Name: "dev", SavedHourly: saved}}
	}
	return s
}

func TestLedgerAdd(t *testing.T) {
	t0 := time.Date(2026, 10, 5, 23, 50, 0, 0, time.UTC)
	var l Ledger
	l.Add(snapAt(t0, 12, map[string]float64{"a": 6}, 2))
	if len(l.Days) != 0 {
		t.Fatalf("the first sample only starts the clock, got %d days", len(l.Days))
	}
	l.Add(snapAt(t0.Add(5*time.Minute), 12, map[string]float64{"a": 6}, 2))
	if len(l.Days) != 1 || !near(l.Days[0].Provisioned, 1) || !near(l.Days[0].Namespaces["a"].Cost, 0.5) || !near(l.Days[0].Saved, 1.0/6) {
		t.Fatalf("five minutes at 12/h should book 1, got %+v", l.Days)
	}
	if !near(l.Days[0].Schedules["group/dev"], 1.0/6) {
		t.Errorf("savings are booked per schedule, got %v", l.Days[0].Schedules)
	}
	// The next sample falls on the next UTC day.
	l.Add(snapAt(t0.Add(15*time.Minute), 12, map[string]float64{"a": 6}, 0))
	if len(l.Days) != 2 || l.Days[1].Date != "2026-10-06" || !near(l.Days[1].Provisioned, 2) {
		t.Fatalf("expected a second day with 10 minutes booked, got %+v", l.Days)
	}
	// A gap longer than maxGap is not counted, it only restarts the clock.
	l.Add(snapAt(t0.Add(3*time.Hour), 12, map[string]float64{"a": 6}, 0))
	if !near(l.Days[1].Provisioned, 2) || !near(l.Days[1].Hours, 10.0/60) {
		t.Errorf("a gap must not be booked, got %+v", l.Days[1])
	}
	// Changing the currency starts a new history.
	eur := snapAt(t0.Add(3*time.Hour+5*time.Minute), 12, nil, 0)
	eur.Rates.Currency = "EUR"
	l.Add(eur)
	if l.Currency != "EUR" || len(l.Days) != 0 {
		t.Errorf("a currency change must reset the ledger, got %s with %d days", l.Currency, len(l.Days))
	}
}

func TestLedgerRetentionAndSize(t *testing.T) {
	var l Ledger
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	ns := map[string]float64{}
	for i := range 400 {
		ns[fmt.Sprintf("namespace-with-a-long-name-%03d", i)] = 1
	}
	for d := range 120 {
		at := start.AddDate(0, 0, d)
		l.Last = time.Time{}
		l.Add(snapAt(at, 100, ns, 0))
		l.Add(snapAt(at.Add(5*time.Minute), 100, ns, 0))
	}
	if len(l.Days) != RetentionDays {
		t.Errorf("expected %d days after trimming, got %d", RetentionDays, len(l.Days))
	}
	data, err := l.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > maxLedgerBytes {
		t.Errorf("encoded ledger is %d bytes, over the %d limit", len(data), maxLedgerBytes)
	}
	if last := l.Days[len(l.Days)-1].Date; last != "2026-04-30" {
		t.Errorf("trimming for size must drop the oldest days, newest is %s", last)
	}
}

func TestNewOverview(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	s := Snapshot{
		At:    now,
		Rates: pricing.Rates{Currency: "USD"},
		Cluster: Cluster{
			ProvisionedHourly: 10, RequestedHourly: 4, UsedHourly: 1,
		},
		Namespaces: []Namespace{
			{Name: "shop-prod", RequestedHourly: 2, UsedHourly: 0.5, IdleHourly: 1.5},
			{Name: "dev-api", RequestedHourly: 1, UsedHourly: 0.25, IdleHourly: 0.75, Insights: []string{"Missing Requests"}},
			{Name: "staging", RequestedHourly: 1, UsedHourly: 0.25, IdleHourly: 0.75},
		},
		Schedules: []Schedule{{Kind: "group", Name: "stage", Namespaces: []string{"staging"}, SavedHourly: 0.5, Mode: "Schedule"}},
	}
	// Fourteen full days of history: dev-api doubled in the last week.
	ledger := &Ledger{Currency: "USD", Since: now.AddDate(0, 0, -20)}
	for d := 13; d >= 0; d-- {
		date := DateOf(now.AddDate(0, 0, -d))
		cost := 24.0
		if d < 7 {
			cost = 48
		}
		ledger.Days = append(ledger.Days, Day{Date: date, Hours: 24, Provisioned: 240, Saved: 12,
			Namespaces: map[string]NamespaceDay{"dev-api": {Cost: cost}, "shop-prod": {Cost: 48}}})
	}

	o := NewOverview(s, ledger, map[string]float64{"shop-prod": 30}, 1)
	checkTotals(t, o)
	checkRows(t, o)
	checkOpportunities(t, o)
	checkMonthToDate(t, o, now)
}

func checkTotals(t *testing.T, o Overview) {
	t.Helper()
	if !near(o.Monthly.Provisioned, 7300) || !near(o.Monthly.Unallocated, 4380) || !near(o.Monthly.Overprovisioned, 3*730) {
		t.Errorf("monthly split wrong: %+v", o.Monthly)
	}
	if !near(o.Savings.SchedulesMonthly, 365) || !near(o.Savings.RightsizingMonthly, 30) || o.Savings.RightsizingPending != 1 {
		t.Errorf("savings wrong: %+v", o.Savings)
	}
	if len(o.History.Days) != 14 || o.History.Top[0] != "shop-prod" {
		t.Errorf("history wrong: top %v, %d days", o.History.Top, len(o.History.Days))
	}
}

func rowOf(t *testing.T, o Overview, name string) NamespaceRow {
	t.Helper()
	for _, r := range o.Namespaces {
		if r.Name == name {
			return r
		}
	}
	t.Fatalf("no row %s", name)
	return NamespaceRow{}
}

func checkRows(t *testing.T, o Overview) {
	t.Helper()
	dev := rowOf(t, o, "dev-api")
	if dev.Environment != EnvNonProduction || !near(dev.ScheduleSavingMonthly, 730*(1-workingHoursUpShare)) || dev.Schedule != nil {
		t.Errorf("dev-api should be an unscheduled non-production candidate, got %+v", dev)
	}
	if dev.Cost7d == nil || dev.CostPrev7d == nil || !near(*dev.Cost7d, 7*48) || !near(*dev.CostPrev7d, 7*24) {
		t.Errorf("week comparison wrong: %v %v", dev.Cost7d, dev.CostPrev7d)
	}
	if st := rowOf(t, o, "staging"); st.Schedule == nil || st.Schedule.Name != "stage" || st.ScheduleSavingMonthly != 0 {
		t.Errorf("staging is scheduled and no candidate, got %+v", st)
	}
	if prod := rowOf(t, o, "shop-prod"); prod.ScheduleSavingMonthly != 0 || prod.Efficiency == nil || !near(*prod.Efficiency, 0.25) {
		t.Errorf("production is never a schedule candidate, got %+v", prod)
	}
}

func checkOpportunities(t *testing.T, o Overview) {
	t.Helper()
	kinds := map[string]bool{}
	for i, op := range o.Opportunities {
		kinds[op.Kind] = true
		if i > 0 && op.MonthlySavings > o.Opportunities[i-1].MonthlySavings && op.Kind != OpportunityMissingRequests {
			t.Errorf("opportunities must be ranked by savings: %+v", o.Opportunities)
		}
	}
	for _, k := range []string{OpportunityRightsizing, OpportunitySchedule, OpportunityIdleCapacity, OpportunityCostIncrease, OpportunityMissingRequests} {
		if !kinds[k] {
			t.Errorf("missing a %s opportunity in %+v", k, o.Opportunities)
		}
	}
	if o.Opportunities[0].Kind != OpportunityIdleCapacity {
		t.Errorf("4380/month of unrequested capacity should rank first, got %+v", o.Opportunities[0])
	}
}

func checkMonthToDate(t *testing.T, o Overview, now time.Time) {
	t.Helper()
	if o.MonthToDate.Month != "2026-10" || !near(o.MonthToDate.Cost, 14*240) || !o.MonthToDate.Partial {
		t.Errorf("month to date wrong: %+v", o.MonthToDate)
	}
	remaining := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC).Sub(now).Hours()
	if !near(o.MonthToDate.Forecast, 14*240+10*remaining) {
		t.Errorf("forecast should add the run rate for the rest of the month, got %v", o.MonthToDate.Forecast)
	}
}

func TestBuild(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = finopsv1.AddToScheme(scheme)
	node := func(name string, spot bool) *corev1.Node {
		n := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{}}}
		if spot {
			n.Labels["karpenter.sh/capacity-type"] = "spot"
		}
		n.Status.Capacity = corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4"), corev1.ResourceMemory: resource.MustParse("16Gi")}
		return n
	}
	tracked := &finopsv1.NamespaceFinOps{
		ObjectMeta: metav1.ObjectMeta{Name: "dev", Namespace: "costdeck"},
		Spec:       finopsv1.NamespaceFinOpsSpec{TargetNamespace: "dev"},
		Status: finopsv1.NamespaceFinOpsStatus{History: []finopsv1.MetricDataPoint{{
			CPU:    finopsv1.ResourceMetrics{Requests: "2", Usage: "500m", Limits: "4"},
			Memory: finopsv1.ResourceMetrics{Requests: "4Gi", Usage: "6Gi", Limits: "8Gi"},
		}}},
	}
	gone := &finopsv1.NamespaceFinOps{
		ObjectMeta: metav1.ObjectMeta{Name: "gone", Namespace: "costdeck"},
		Spec:       finopsv1.NamespaceFinOpsSpec{TargetNamespace: "gone"},
	}
	group := &finopsv1.ScalingGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "dev", Namespace: "costdeck"},
		Spec:       finopsv1.ScalingGroupSpec{Category: "Env", Namespaces: []string{"dev"}},
		Status:     finopsv1.ScalingGroupStatus{ScheduleStatus: finopsv1.ScheduleStatus{EstimatedHourlySavings: "0.2500"}},
	}
	rulesOnly := &finopsv1.ScalingConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "costdeck"},
		Spec:       finopsv1.ScalingConfigSpec{TargetNamespace: "web"},
		Status:     finopsv1.ScalingConfigStatus{ScheduleStatus: finopsv1.ScheduleStatus{Mode: "AlwaysOn"}},
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "dev"}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		node("a", false), node("b", true), tracked, gone, group, rulesOnly, pod,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "dev", Labels: map[string]string{"team": "core"}}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "web"}},
	).Build()

	rates := pricing.Rates{CPUCoreHour: 0.04, MemoryGBHour: 0.004, Currency: "USD"}
	s, err := Build(context.Background(), c, rates, Options{Namespace: "costdeck", Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if s.Cluster.Nodes != 2 || s.Cluster.SpotNodes != 1 || !near(s.Cluster.CPUCores, 8) || !near(s.Cluster.MemoryGiB, 32) {
		t.Errorf("cluster capacity wrong: %+v", s.Cluster)
	}
	if !near(s.Cluster.ProvisionedHourly, 8*0.04+32*0.004) {
		t.Errorf("provisioned cost wrong: %v", s.Cluster.ProvisionedHourly)
	}
	if len(s.Namespaces) != 1 || s.Namespaces[0].Name != "dev" {
		t.Fatalf("only namespaces that still exist are reported, got %+v", s.Namespaces)
	}
	dev := s.Namespaces[0]
	if dev.Pods != 1 || dev.Labels["team"] != "core" || !near(dev.RequestedHourly, 2*0.04+4*0.004) {
		t.Errorf("dev wrong: %+v", dev)
	}
	// Memory is used beyond the request: idle counts only the unused CPU.
	if !near(dev.IdleHourly, 1.5*0.04) {
		t.Errorf("idle must never go negative per resource, got %v", dev.IdleHourly)
	}
	if !near(s.SavedHourly(), 0.25) {
		t.Errorf("saved %v", s.SavedHourly())
	}
	if _, ok := s.ScheduleFor("web"); ok {
		t.Error("a config with only workload rules does not schedule its namespace")
	}
	if sc, ok := s.ScheduleFor("dev"); !ok || sc.Kind != "group" {
		t.Errorf("dev is scheduled by its group, got %+v", sc)
	}
	if !strings.Contains(fmt.Sprint(s.Schedules), "web") {
		t.Errorf("configs are listed as schedules too: %+v", s.Schedules)
	}
}
