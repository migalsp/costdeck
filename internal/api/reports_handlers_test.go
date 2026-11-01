package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/auth"
	"github.com/migalsp/costdeck-operator/internal/finops"
	"github.com/migalsp/costdeck-operator/internal/notify"
)

type reportIndex struct {
	Reports    []finops.ReportEntry    `json:"reports"`
	Schedule   finopsv1.DigestSchedule `json:"schedule"`
	WebexReady bool                    `json:"webexReady"`
	Channels   []string                `json:"channels"`
	LastSent   *time.Time              `json:"lastSent"`
	NextDue    *time.Time              `json:"nextDue"`
}

func saveReport(t *testing.T, server *Server, entry finops.ReportEntry, markdown string) finops.ReportEntry {
	t.Helper()
	saved, err := finops.SaveReport(context.Background(), server.Client, operatorNamespace, entry, markdown)
	if err != nil {
		t.Fatal(err)
	}
	return saved
}

func TestReportHistoryListGetAndDelete(t *testing.T) {
	t.Setenv("POD_NAMESPACE", operatorNamespace)
	server := buildMockServerWithK8s()

	empty := decodeInto[reportIndex](t, send(t, server, http.MethodGet, "/api/reports", ""))
	if empty.Reports == nil || len(empty.Reports) != 0 || empty.Channels == nil || empty.WebexReady || empty.LastSent != nil || empty.NextDue != nil {
		t.Errorf("index of a fresh install = %+v, want empty lists the UI can map and no dates", empty)
	}

	older := saveReport(t, server, finops.ReportEntry{Kind: finops.ReportDigest, Title: "Week 43", GeneratedAt: time.Now().Add(-48 * time.Hour)}, "# Week 43")
	newer := saveReport(t, server, finops.ReportEntry{Kind: finops.ReportAI, Title: "Executive cost report", Audience: "executive"}, "# Executive")

	index := decodeInto[reportIndex](t, send(t, server, http.MethodGet, "/api/reports", ""))
	if len(index.Reports) != 2 || index.Reports[0].ID != newer.ID || index.Reports[1].ID != older.ID {
		t.Errorf("reports = %+v, want the newest first", index.Reports)
	}

	rr := send(t, server, http.MethodGet, "/api/reports/"+older.ID, "")
	got := decodeInto[struct {
		Report   finops.ReportEntry `json:"report"`
		Markdown string             `json:"markdown"`
	}](t, rr)
	if rr.Code != http.StatusOK || got.Report.Title != "Week 43" || got.Markdown != "# Week 43" {
		t.Errorf("GET report = %d %+v", rr.Code, got)
	}
	if rr := send(t, server, http.MethodGet, "/api/reports/20260101-000000-ai-ffffff", ""); rr.Code != http.StatusNotFound || errorMessage(t, rr) != "report not found" {
		t.Errorf("GET unknown report = %d %s", rr.Code, rr.Body.String())
	}

	if rr := send(t, server, http.MethodDelete, "/api/reports/"+older.ID, ""); rr.Code != http.StatusNoContent {
		t.Fatalf("DELETE = %d %s", rr.Code, rr.Body.String())
	}
	if rr := send(t, server, http.MethodGet, "/api/reports/"+older.ID, ""); rr.Code != http.StatusNotFound {
		t.Errorf("a deleted report is still served: %d", rr.Code)
	}
	if rr := send(t, server, http.MethodDelete, "/api/reports/"+older.ID, ""); rr.Code != http.StatusNotFound {
		t.Errorf("deleting twice = %d, want 404", rr.Code)
	}
	if index := decodeInto[reportIndex](t, send(t, server, http.MethodGet, "/api/reports", "")); len(index.Reports) != 1 || index.Reports[0].ID != newer.ID {
		t.Errorf("reports after delete = %+v", index.Reports)
	}
}

func TestReportIndexShowsTheDigestSchedule(t *testing.T) {
	t.Setenv("POD_NAMESPACE", operatorNamespace)
	server := buildMockServerWithK8s()
	schedule := finopsv1.DigestSchedule{Enabled: true, Frequency: "weekly", Time: "09:00", Timezone: "UTC"}
	seedConfig(t, server, finopsv1.CostDeckConfigSpec{Reports: finopsv1.ReportsConfig{Digest: schedule}, Integrations: finopsv1.IntegrationsConfig{
		Notifications: &finopsv1.NotificationsConfig{Slack: &finopsv1.WebhookChannelConfig{Enabled: true, SecretRef: "slack-hook", Events: []finopsv1.NotificationEvent{"digests"}}},
	}})
	lastSent := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	createAll(t, server.Client, &corev1.ConfigMap{Name: finops.ReportsConfigMap, Namespace: operatorNamespace,
		Annotations: map[string]string{"costdeck.io/digest-last-sent": lastSent.Format(time.RFC3339)}})

	index := decodeInto[reportIndex](t, send(t, server, http.MethodGet, "/api/reports", ""))
	if index.Schedule != schedule || strings.Join(index.Channels, ",") != notify.ChannelSlack || index.WebexReady {
		t.Errorf("index = %+v, want the schedule and Slack as the only channel", index)
	}
	if index.LastSent == nil || !index.LastSent.Equal(lastSent) {
		t.Errorf("lastSent = %v, want %v", index.LastSent, lastSent)
	}
	if index.NextDue == nil || !index.NextDue.After(time.Now()) || index.NextDue.UTC().Weekday() != time.Monday || index.NextDue.UTC().Hour() != 9 {
		t.Errorf("nextDue = %v, want the coming Monday 09:00 UTC", index.NextDue)
	}
}

func TestDigestCoversTheRequestedPeriod(t *testing.T) {
	t.Setenv("POD_NAMESPACE", operatorNamespace)
	server := buildMockServerWithK8s()
	seedConfig(t, server, finopsv1.CostDeckConfigSpec{Pricing: customPricing()})
	createAll(t, server.Client, readyNode("n1", "4", "16Gi", true))
	for query, period := range map[string]string{"": finops.PeriodWeek, "?period=month": finops.PeriodMonth, "?period=decade": finops.PeriodWeek} {
		rr := send(t, server, http.MethodGet, "/api/reports/digest"+query, "")
		got := decodeInto[struct {
			Digest   finops.Digest `json:"digest"`
			Markdown string        `json:"markdown"`
		}](t, rr)
		if rr.Code != http.StatusOK || got.Digest.Period != period || got.Digest.Currency != customCurrency || got.Markdown != got.Digest.Markdown() || !strings.Contains(got.Markdown, got.Digest.Title) {
			t.Errorf("digest%s = %d period %q, want %q with its Markdown", query, rr.Code, got.Digest.Period, period)
		}
	}
}

func TestSendDigestPostsSavesAndRecordsWhoSentIt(t *testing.T) {
	t.Setenv("POD_NAMESPACE", operatorNamespace)
	server := buildMockServerWithK8s()
	createAll(t, server.Client, readyNode("n1", "4", "16Gi", true))
	poster := &recordingPoster{}
	server.Poster = poster
	anna := &auth.Identity{Subject: "user:anna", Name: "Anna", Role: auth.RoleOperator}

	if rr := sendAs(t, server, anna, http.MethodPost, "/api/reports/digest/send", `{"period":`); rr.Code != http.StatusBadRequest {
		t.Errorf("malformed body = %d", rr.Code)
	}
	rr := sendAs(t, server, anna, http.MethodPost, "/api/reports/digest/send", `{"period":"month"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("send = %d %s", rr.Code, rr.Body.String())
	}
	entry := decodeInto[finops.ReportEntry](t, rr)
	if entry.Kind != finops.ReportDigest || entry.Period != finops.PeriodMonth || entry.By != "Anna" || entry.Delivered != notify.ChannelSlack || entry.ID == "" {
		t.Errorf("entry = %+v", entry)
	}
	if len(poster.posts) != 1 || !strings.Contains(poster.posts[0], entry.Title) {
		t.Fatalf("posts = %q, want the digest once", poster.posts)
	}
	if _, md, err := finops.GetReport(context.Background(), server.Client, operatorNamespace, entry.ID); err != nil || md != poster.posts[0] {
		t.Errorf("the stored digest differs from what was posted: %v", err)
	}
}

func TestSendDigestRefusesWithoutAChannel(t *testing.T) {
	t.Setenv("POD_NAMESPACE", operatorNamespace)
	server := buildMockServerWithK8s()
	rr := send(t, server, http.MethodPost, "/api/reports/digest/send", "")
	if rr.Code != http.StatusBadRequest || !strings.Contains(errorMessage(t, rr), "Could not post the digest: no destination configured") {
		t.Errorf("send without channels = %d %s, want 400", rr.Code, rr.Body.String())
	}
	if reports, _ := finops.ListReports(context.Background(), server.Client, operatorNamespace); len(reports) != 0 {
		t.Errorf("a digest nobody received was saved: %+v", reports)
	}
}

func TestSendDigestReportsAFailedChannelAsBadGateway(t *testing.T) {
	t.Setenv("POD_NAMESPACE", operatorNamespace)
	server := buildMockServerWithK8s()
	poster := &failingPoster{err: errors.New("slack answered 500")}
	server.Poster = poster
	rr := send(t, server, http.MethodPost, "/api/reports/digest/send", `{"period":"week"}`)
	if rr.Code != http.StatusBadGateway || errorMessage(t, rr) != "Could not post the digest: slack answered 500" || poster.posts != 1 {
		t.Errorf("send with a failing channel = %d %s", rr.Code, rr.Body.String())
	}
	if reports, _ := finops.ListReports(context.Background(), server.Client, operatorNamespace); len(reports) != 0 {
		t.Errorf("a digest that was not delivered was saved: %+v", reports)
	}
}
