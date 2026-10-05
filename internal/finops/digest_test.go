package finops

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/pricing"
)

func ledgerDays(now time.Time, n int, cost func(daysAgo int) float64) *Ledger {
	l := &Ledger{Currency: "USD", Since: now.AddDate(0, 0, -n)}
	for d := n; d >= 0; d-- {
		c := cost(d)
		l.Days = append(l.Days, Day{
			Date: DateOf(now.AddDate(0, 0, -d)), Hours: 24, Provisioned: c, Requested: c / 2, Used: c / 10, Saved: 1,
			Namespaces: map[string]NamespaceDay{"shop": {Cost: c / 4}, "dev": {Cost: c / 4}},
		})
	}
	return l
}

func TestNewDigestWeek(t *testing.T) {
	now := time.Date(2026, 10, 7, 9, 0, 0, 0, time.UTC) // a Wednesday
	// The last full week costs 20 a day, the week before 10.
	l := ledgerDays(now, 20, func(d int) float64 {
		if d >= 1 && d <= 7 {
			return 20
		}
		return 10
	})
	o := Overview{Currency: "USD", Namespaces: []NamespaceRow{{Name: "shop", Team: "payments"}, {Name: "dev"}},
		Opportunities: []Opportunity{{Title: "Right-size shop", MonthlySavings: 12}, {Title: "Set requests"}}}
	d := NewDigest(o, l, PeriodWeek, now)

	if d.From != "2026-09-30" || d.To != "2026-10-06" || d.PrevFrom != "2026-09-23" || d.PrevTo != "2026-09-29" {
		t.Fatalf("periods wrong: %s..%s, %s..%s", d.From, d.To, d.PrevFrom, d.PrevTo)
	}
	if !near(d.Cost, 140) || !near(d.PrevCost, 70) || !near(d.Saved, 7) || !d.Complete {
		t.Errorf("totals wrong: %+v", d)
	}
	if len(d.Namespaces) != 2 || d.Namespaces[0].Change() == nil || !near(*d.Namespaces[0].Change(), 1) {
		t.Errorf("namespace rows wrong: %+v", d.Namespaces)
	}
	if len(d.Teams) != 2 || len(d.Opportunities) != 1 {
		t.Errorf("teams %+v, opportunities %+v", d.Teams, d.Opportunities)
	}
	md := d.Markdown()
	for _, want := range []string{"**Cost digest · Sep 30 – Oct 6**", "$140.00", "100% more than the week before", "payments", "Right-size shop"} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown lacks %q:\n%s", want, md)
		}
	}
}

func TestNewDigestMonthAndEmpty(t *testing.T) {
	now := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	d := NewDigest(Overview{Currency: "USD"}, &Ledger{}, PeriodMonth, now)
	if d.From != "2026-09-01" || d.To != "2026-09-30" || d.PrevFrom != "2026-08-01" || d.PrevTo != "2026-08-31" {
		t.Errorf("month periods wrong: %+v", d)
	}
	if !strings.Contains(d.Markdown(), "does not cover this period") {
		t.Errorf("an empty ledger must say so: %s", d.Markdown())
	}
}

func TestNextDigest(t *testing.T) {
	berlin, _ := time.LoadLocation("Europe/Berlin")
	cases := []struct {
		sched finopsv1.DigestSchedule
		after time.Time
		want  time.Time
	}{
		// Wednesday → next Monday 09:00 UTC.
		{finopsv1.DigestSchedule{Frequency: "weekly"}, time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC), time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)},
		// Monday 08:00 → the same day at 09:00.
		{finopsv1.DigestSchedule{}, time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC), time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)},
		// Exactly at the time → a week later.
		{finopsv1.DigestSchedule{}, time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC), time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)},
		{finopsv1.DigestSchedule{Frequency: "monthly", Time: "07:30", Timezone: "Europe/Berlin"},
			time.Date(2026, 10, 1, 6, 0, 0, 0, time.UTC), time.Date(2026, 11, 1, 7, 30, 0, 0, berlin)},
	}
	for _, c := range cases {
		got, err := NextDigest(c.sched, c.after)
		if err != nil || !got.Equal(c.want) {
			t.Errorf("NextDigest(%+v, %s) = %s, %v; want %s", c.sched, c.after, got, err, c.want)
		}
	}
	if _, err := NextDigest(finopsv1.DigestSchedule{Timezone: "Mars/Olympus"}, time.Now()); err == nil {
		t.Error("an unknown time zone must be an error")
	}
}

type fakePoster struct {
	sent []string
	err  error
}

func (f *fakePoster) Post(_ context.Context, md string) error {
	if f.err != nil {
		return f.err
	}
	f.sent = append(f.sent, md)
	return nil
}

func testClient(objs ...client.Object) client.Client {
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = finopsv1.AddToScheme(scheme)
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
}

func TestReportStore(t *testing.T) {
	ctx := context.Background()
	c := testClient()
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	var first ReportEntry
	for i := range maxReports + 3 {
		e, err := SaveReport(ctx, c, "costdeck", ReportEntry{Kind: ReportAI, Title: "r", GeneratedAt: base.Add(time.Duration(i) * time.Hour)}, "# report")
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = e
		}
	}
	list, err := ListReports(ctx, c, "costdeck")
	if err != nil || len(list) != maxReports {
		t.Fatalf("expected %d reports, got %d (%v)", maxReports, len(list), err)
	}
	if !list[0].GeneratedAt.After(list[1].GeneratedAt) {
		t.Error("reports must be listed newest first")
	}
	if _, _, err := GetReport(ctx, c, "costdeck", first.ID); !errors.Is(err, ErrReportNotFound) {
		t.Error("the oldest report should have been dropped")
	}
	if _, md, err := GetReport(ctx, c, "costdeck", list[0].ID); err != nil || md != "# report" {
		t.Errorf("GetReport: %q %v", md, err)
	}
	if err := DeleteReport(ctx, c, "costdeck", list[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := DeleteReport(ctx, c, "costdeck", list[0].ID); !errors.Is(err, ErrReportNotFound) {
		t.Errorf("deleting twice should report not found, got %v", err)
	}
}

func TestDigestScheduler(t *testing.T) {
	ctx := context.Background()
	cfg := &finopsv1.CostDeckConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: "costdeck"},
		Spec:       finopsv1.CostDeckConfigSpec{Reports: finopsv1.ReportsConfig{Digest: finopsv1.DigestSchedule{Enabled: true, Frequency: "weekly"}}},
	}
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n"}}
	c := testClient(cfg, node)
	poster := &fakePoster{}
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC) // Wednesday
	s := &DigestScheduler{Client: c, Pricing: &pricing.Resolver{Client: c}, Poster: poster, Namespace: "costdeck", Now: func() time.Time { return now }}
	t.Setenv("POD_NAMESPACE", "costdeck")

	// The first tick only starts the clock.
	if err := s.Tick(ctx); err != nil || len(poster.sent) != 0 {
		t.Fatalf("first tick: %v, sent %d", err, len(poster.sent))
	}
	// Still the same week: nothing to send.
	now = now.Add(24 * time.Hour)
	if err := s.Tick(ctx); err != nil || len(poster.sent) != 0 {
		t.Fatalf("before Monday: %v, sent %d", err, len(poster.sent))
	}
	// Monday 09:01: the digest goes out and lands in the history.
	now = time.Date(2026, 10, 12, 9, 1, 0, 0, time.UTC)
	if err := s.Tick(ctx); err != nil || len(poster.sent) != 1 {
		t.Fatalf("on Monday: %v, sent %d", err, len(poster.sent))
	}
	if list, _ := ListReports(ctx, c, "costdeck"); len(list) != 1 || list[0].Kind != ReportDigest || list[0].Delivered != "webex" {
		t.Errorf("the digest should be kept in the history: %+v", list)
	}
	// A minute later nothing is sent again.
	now = now.Add(time.Minute)
	if err := s.Tick(ctx); err != nil || len(poster.sent) != 1 {
		t.Fatalf("after sending: %v, sent %d", err, len(poster.sent))
	}
	// A failed delivery waits an hour before the next attempt.
	poster.err = errors.New("webex down")
	now = time.Date(2026, 10, 19, 9, 0, 30, 0, time.UTC)
	if err := s.Tick(ctx); err == nil {
		t.Fatal("a failed delivery must be reported")
	}
	poster.err = nil
	now = now.Add(10 * time.Minute)
	if err := s.Tick(ctx); err != nil || len(poster.sent) != 1 {
		t.Fatalf("retry must wait an hour: %v, sent %d", err, len(poster.sent))
	}
	now = now.Add(time.Hour)
	if err := s.Tick(ctx); err != nil || len(poster.sent) != 2 {
		t.Fatalf("retry after an hour: %v, sent %d", err, len(poster.sent))
	}
}
