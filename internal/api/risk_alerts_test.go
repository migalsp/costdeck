package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	metricsfake "k8s.io/metrics/pkg/client/clientset/versioned/fake"
	"sigs.k8s.io/controller-runtime/pkg/client"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/finops"
)

type recordingPoster struct{ posts []string }

func (p *recordingPoster) Post(_ context.Context, markdown string) (string, error) {
	p.posts = append(p.posts, markdown)
	return "Slack", nil
}

func TestRiskAlerterSendsNewCriticalRisksOncePerDay(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "costdeck")
	server := buildMockServerWithK8s()
	server.MetricsClient = metricsfake.NewSimpleClientset() //nolint:staticcheck // k8s.io/metrics generates no NewClientset
	ctx := context.Background()
	cfg := &finopsv1.CostDeckConfig{ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: "costdeck"},
		Spec: finopsv1.CostDeckConfigSpec{Alerts: finopsv1.AlertsConfig{PerformanceRisks: &finopsv1.PerformanceRiskAlerts{Enabled: true}}}}
	oom := corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{
		Name: "main", RestartCount: 1, LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled", ExitCode: 137}},
	}}}
	for _, obj := range []client.Object{
		cfg,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "shop"}}, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "costdeck"}},
		riskDeployment("api"), riskPod("api-7d9f8c-abcde", oom),
	} {
		if err := server.Client.Create(ctx, obj); err != nil {
			t.Fatal(err)
		}
	}
	poster := &recordingPoster{}
	now := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	alerter := &RiskAlerter{Server: server, Poster: poster, Namespace: "costdeck", Now: func() time.Time { return now }}

	if err := alerter.Check(ctx); err != nil {
		t.Fatal(err)
	}
	if len(poster.posts) != 1 || !strings.Contains(poster.posts[0], "Performance risks: 1 new") || !strings.Contains(poster.posts[0], "shop / api / main: OOM-killed") || !strings.Contains(poster.posts[0], "Fix: Raise the memory limit") {
		t.Fatalf("posts = %q", poster.posts)
	}
	state, err := finops.LoadRiskState(ctx, server.Client, "costdeck")
	if err != nil || len(state.Open) != 1 {
		t.Fatalf("state = %+v, %v", state, err)
	}
	var alerts corev1.ConfigMap
	if err := server.Client.Get(ctx, client.ObjectKey{Namespace: "costdeck", Name: finops.AlertsConfigMap}, &alerts); err != nil || !strings.Contains(alerts.Data["events.json"], `"kind":"risk"`) {
		t.Errorf("the alert is not in the alert history: %v %v", alerts.Data, err)
	}

	server.pressureCache = pressureCache{}
	now = now.Add(time.Hour)
	if err := alerter.Check(ctx); err != nil || len(poster.posts) != 1 {
		t.Fatalf("posts = %d, err = %v: the same risk an hour later is not sent again", len(poster.posts), err)
	}
	server.pressureCache = pressureCache{}
	now = now.Add(24 * time.Hour)
	if err := alerter.Check(ctx); err != nil || len(poster.posts) != 2 {
		t.Fatalf("posts = %d, err = %v: a risk still open a day later is sent again", len(poster.posts), err)
	}

	cfg.Spec.Alerts.PerformanceRisks.Enabled = false
	if err := server.Client.Update(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if err := alerter.Check(ctx); err != nil || len(poster.posts) != 2 {
		t.Fatalf("posts = %d, err = %v", len(poster.posts), err)
	}
	if state, _ := finops.LoadRiskState(ctx, server.Client, "costdeck"); len(state.Open) != 0 {
		t.Errorf("open risks = %+v after alerts were turned off; the digest would list stale risks", state.Open)
	}
}

func TestAlertSettingsAreSavedSectionBySection(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "costdeck")
	server := buildMockServerWithK8s()
	put := func(body string) {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPut, "/api/settings", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		server.routes().ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("PUT %s = %d: %s", body, rr.Code, rr.Body.String())
		}
	}
	put(`{"alerts":{"anomalies":{"enabled":true,"percent":40}}}`)
	put(`{"alerts":{"performanceRisks":{"enabled":true}}}`)
	put(`{"alerts":{"anomalies":{"enabled":true,"percent":50}}}`)
	var cfg finopsv1.CostDeckConfig
	if err := server.Client.Get(context.Background(), client.ObjectKey{Namespace: "costdeck", Name: "default"}, &cfg); err != nil {
		t.Fatal(err)
	}
	if a := cfg.Spec.Alerts; a.Anomalies.Percent != 50 || a.PerformanceRisks == nil || !a.PerformanceRisks.Enabled {
		t.Errorf("alerts = %+v: saving one section must leave the other alone", a)
	}
}
