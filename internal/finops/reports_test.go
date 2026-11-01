package finops

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/pricing"
)

func weeklyDigestConfig() *finopsv1.CostDeckConfig {
	return &finopsv1.CostDeckConfig{
		Name: "default", Namespace: "costdeck",
		Spec: finopsv1.CostDeckConfigSpec{Reports: finopsv1.ReportsConfig{Digest: finopsv1.DigestSchedule{Enabled: true, Frequency: "weekly"}}},
	}
}

func TestDigestSchedulerStartChecksTheScheduleRightAway(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "costdeck")
	ctx := context.Background()
	c := testClient(weeklyDigestConfig())
	poster := &fakePoster{}
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	s := &DigestScheduler{Client: c, Pricing: &pricing.Resolver{Client: c}, Poster: poster, Namespace: "costdeck", Now: func() time.Time { return now }}
	if !s.NeedLeaderElection() {
		t.Fatal("the digest scheduler must run on the leader only, so a digest is sent once")
	}

	loop := startLoop(ctx, s.Start)
	waitUntil(t, "the first tick records when the schedule started", func() bool { return DigestLastSent(ctx, c, "costdeck").Equal(now) })
	if err := loop.stop(t); err != nil {
		t.Fatalf("Start returned %v after cancellation, want nil", err)
	}
	if len(poster.sent) != 0 {
		t.Errorf("the first tick sent %d digest(s); it must only start the clock", len(poster.sent))
	}
}

func TestDigestLastSent(t *testing.T) {
	ctx := context.Background()
	sent := time.Date(2026, 10, 12, 9, 1, 0, 0, time.UTC)
	reports := func(annotation string) *corev1.ConfigMap {
		return &corev1.ConfigMap{Name: ReportsConfigMap, Namespace: "costdeck", Annotations: map[string]string{annotationDigestSent: annotation}}
	}
	for _, tc := range []struct {
		name string
		got  time.Time
		want time.Time
	}{
		{"never sent", DigestLastSent(ctx, testClient(), "costdeck"), time.Time{}},
		{"sent", DigestLastSent(ctx, testClient(reports(sent.Format(time.RFC3339))), "costdeck"), sent},
		{"an unreadable annotation counts as never", DigestLastSent(ctx, testClient(reports("last monday")), "costdeck"), time.Time{}},
		{"a failed read counts as never", DigestLastSent(ctx, clientFailingGets(), "costdeck"), time.Time{}},
	} {
		if !tc.got.Equal(tc.want) {
			t.Errorf("%s: DigestLastSent = %v, want %v", tc.name, tc.got, tc.want)
		}
	}
}

func TestReportReadsPassOnErrorsAndListEmptyAsAnEmptyList(t *testing.T) {
	ctx := context.Background()
	if list, err := ListReports(ctx, testClient(), "costdeck"); err != nil || list == nil || len(list) != 0 {
		t.Errorf("no reports yet = %#v, %v; want an empty, non-nil list", list, err)
	}
	noIndex := testClient(&corev1.ConfigMap{Name: ReportsConfigMap, Namespace: "costdeck"})
	if list, err := ListReports(ctx, noIndex, "costdeck"); err != nil || list == nil || len(list) != 0 {
		t.Errorf("a ConfigMap without an index = %#v, %v; want an empty, non-nil list", list, err)
	}
	if _, err := ListReports(ctx, clientFailingGets(), "costdeck"); !errors.Is(err, errAPIDown) {
		t.Errorf("ListReports = %v, want the read error", err)
	}
	if _, _, err := GetReport(ctx, clientFailingGets(), "costdeck", "x"); !errors.Is(err, errAPIDown) {
		t.Errorf("GetReport = %v, want the read error rather than not found", err)
	}
}

func TestDigestSchedulerSendBuildsFromTheOverview(t *testing.T) {
	ctx := context.Background()
	c := testClient()
	risk := OpenRisk{Namespace: "shop", Kind: "Deployment", Workload: "api", Container: "main", Code: "oom-killed", Message: "OOM-killed."}
	if err := SaveRiskState(ctx, c, "costdeck", RiskState{Open: map[string]OpenRisk{risk.Key(): risk}}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	if err := SaveLedger(ctx, c, "costdeck", ledgerDays(now, 20, func(int) float64 { return 10 })); err != nil {
		t.Fatal(err)
	}
	calls := 0
	poster := &fakePoster{}
	s := &DigestScheduler{Client: c, Poster: poster, Namespace: "costdeck", Now: func() time.Time { return now },
		Overview: func(context.Context) (Overview, error) {
			calls++
			return Overview{Currency: "USD", Opportunities: []Opportunity{{Title: "Right-size shop", MonthlySavings: 12}}}, nil
		}}

	e, err := s.Send(ctx, PeriodWeek, "ops")
	if err != nil || calls != 1 || len(poster.sent) != 1 {
		t.Fatalf("Send = %+v, %v; overview built %d time(s), %d posted", e, err, calls, len(poster.sent))
	}
	md := poster.sent[0]
	for _, want := range []string{"The cluster cost **$70.00**", "Right-size shop: about $12.00 a month", "shop / api / main: OOM-killed."} {
		if !strings.Contains(md, want) {
			t.Errorf("the digest lacks %q from the ledger, the overview or the open risks:\n%s", want, md)
		}
	}
	if e.Kind != ReportDigest || e.Period != PeriodWeek || e.By != "ops" || e.Delivered != deliveredTo || !e.GeneratedAt.Equal(now) {
		t.Errorf("report entry = %+v", e)
	}
	if _, stored, err := GetReport(ctx, c, "costdeck", e.ID); err != nil || stored != md {
		t.Errorf("stored report = %q, %v; want what was posted", stored, err)
	}
}

func TestDigestSchedulerSendStopsWithoutAnOverview(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, tc := range []struct {
		name      string
		ctx       context.Context
		overview  Overview
		err       error
		wantErr   error
		wantCalls int
	}{
		{"the overview fails", context.Background(), Overview{}, errAPIDown, errAPIDown, 1},
		{"advice still pending when the context ends", cancelled, Overview{Savings: Savings{RightsizingPending: 2}}, nil, context.Canceled, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := testClient()
			poster := &fakePoster{}
			calls := 0
			s := &DigestScheduler{Client: c, Poster: poster, Namespace: "costdeck",
				Overview: func(context.Context) (Overview, error) { calls++; return tc.overview, tc.err }}
			if _, err := s.Send(tc.ctx, PeriodWeek, "schedule"); !errors.Is(err, tc.wantErr) {
				t.Fatalf("Send = %v, want %v", err, tc.wantErr)
			}
			if calls != tc.wantCalls || len(poster.sent) != 0 {
				t.Errorf("overview built %d time(s), %d digest(s) posted", calls, len(poster.sent))
			}
			if list, _ := ListReports(context.Background(), c, "costdeck"); len(list) != 0 {
				t.Errorf("a digest that was not sent is in the history: %+v", list)
			}
		})
	}
}
