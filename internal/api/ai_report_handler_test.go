package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/ai"
	"github.com/migalsp/costdeck-operator/internal/auth"
	"github.com/migalsp/costdeck-operator/internal/finops"
)

const labelOnlyTheDashboardShows = "label-value-the-model-never-needs"

func streamingModel(t *testing.T, answer string) (string, *requestLog) {
	t.Helper()
	srv, log := recordingServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": answer}, "finish_reason": "stop"}}})
		_, _ = io.WriteString(w, "data: "+string(chunk)+"\n\ndata: [DONE]\n\n")
	})
	return srv.URL + "/v1", log
}

func reportServer(t *testing.T, modelURL string) *Server {
	t.Helper()
	t.Setenv("POD_NAMESPACE", operatorNamespace)
	server := buildMockServerWithK8s()
	node := &corev1.Node{Name: "n1"}
	node.Status.Capacity = corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4"), corev1.ResourceMemory: resource.MustParse("16Gi")}
	node.Status.Allocatable = node.Status.Capacity
	createAll(t, server.Client,
		node,
		&corev1.Namespace{Name: "dev-api", Labels: map[string]string{"costdeck.io/test-marker": labelOnlyTheDashboardShows}},
		&finopsv1.NamespaceFinOps{Name: "dev-api", Namespace: operatorNamespace, Spec: finopsv1.NamespaceFinOpsSpec{TargetNamespace: "dev-api"}},
		&finopsv1.ScalingGroup{Name: "nightly-shutdown", Namespace: operatorNamespace, Spec: finopsv1.ScalingGroupSpec{Namespaces: []string{"dev-api"}}},
	)
	seedConfig(t, server, finopsv1.CostDeckConfigSpec{Integrations: finopsv1.IntegrationsConfig{
		AI: &finopsv1.AIIntegrationConfig{Enabled: true, Provider: "local", Model: "fake-model", BaseURL: modelURL},
	}})
	return server
}

func streamEvents(t *testing.T, body string) []ai.Event {
	t.Helper()
	var events []ai.Event
	for line := range strings.SplitSeq(body, "\n") {
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		var e ai.Event
		if err := json.Unmarshal([]byte(data), &e); err != nil {
			t.Fatalf("not an event: %q", data)
		}
		events = append(events, e)
	}
	return events
}

func eventOf(events []ai.Event, kind string) (ai.Event, bool) {
	for _, e := range events {
		if e.Type == kind {
			return e, true
		}
	}
	return ai.Event{}, false
}

func modelMessages(t *testing.T, raw string) map[string]string {
	t.Helper()
	var req struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatalf("the model got no chat request: %v: %s", err, raw)
	}
	out := map[string]string{}
	for _, m := range req.Messages {
		out[m.Role] += m.Content
	}
	return out
}

func TestAIReportGetBeforeAnyReportIsEmpty(t *testing.T) {
	t.Setenv("POD_NAMESPACE", operatorNamespace)
	server := buildMockServerWithK8s()
	rr := send(t, server, http.MethodGet, "/api/ai/report", "")
	got := decodeInto[map[string]string](t, rr)
	if rr.Code != http.StatusOK || got["report"] != "" {
		t.Fatalf("GET /api/ai/report without a saved report = %d %v, want 200 with an empty report", rr.Code, got)
	}
	if _, has := got["generatedAt"]; has {
		t.Errorf("an empty report must not claim a generation time: %v", got)
	}
}

func TestAIReportSaveKeepsTheLatestReport(t *testing.T) {
	t.Setenv("POD_NAMESPACE", operatorNamespace)
	server := buildMockServerWithK8s()

	if rr := send(t, server, http.MethodPost, "/api/ai/report/save", `{"report":`); rr.Code != http.StatusBadRequest || errorMessage(t, rr) != "Invalid request" {
		t.Errorf("malformed body = %d %s, want 400 Invalid request", rr.Code, rr.Body.String())
	}
	before := time.Now().UTC().Add(-time.Second)
	if rr := send(t, server, http.MethodPost, "/api/ai/report/save", `{"report":"# First"}`); rr.Code != http.StatusOK {
		t.Fatalf("first save = %d %s", rr.Code, rr.Body.String())
	}
	var cm corev1.ConfigMap
	if err := server.Client.Get(context.Background(), client.ObjectKey{Namespace: operatorNamespace, Name: reportConfigMap}, &cm); err != nil {
		t.Fatal(err)
	}
	if cm.Data[reportKey] != "# First" || cm.Labels["app.kubernetes.io/managed-by"] != "costdeck-operator" {
		t.Errorf("stored report = %+v", cm)
	}
	cm.Data["notes"] = "kept by someone else"
	if err := server.Client.Update(context.Background(), &cm); err != nil {
		t.Fatal(err)
	}

	if rr := send(t, server, http.MethodPost, "/api/ai/report/save", `{"report":"# Second"}`); rr.Code != http.StatusOK {
		t.Fatalf("second save = %d %s", rr.Code, rr.Body.String())
	}
	got := decodeInto[map[string]string](t, send(t, server, http.MethodGet, "/api/ai/report", ""))
	at, err := time.Parse(time.RFC3339, got["generatedAt"])
	if got["report"] != "# Second" || err != nil || at.Before(before) {
		t.Errorf("GET after two saves = %v (%v), want the second report with its time", got, err)
	}
	if err := server.Client.Get(context.Background(), client.ObjectKey{Namespace: operatorNamespace, Name: reportConfigMap}, &cm); err != nil || cm.Data["notes"] != "kept by someone else" {
		t.Errorf("saving a report must only replace the report: %v %v", cm.Data, err)
	}
}

func TestAIReportSaveFillsAConfigMapWithoutData(t *testing.T) {
	t.Setenv("POD_NAMESPACE", operatorNamespace)
	server := buildMockServerWithK8s()
	createAll(t, server.Client, &corev1.ConfigMap{Name: reportConfigMap, Namespace: operatorNamespace})
	if rr := send(t, server, http.MethodPost, "/api/ai/report/save", `{"report":"# Only"}`); rr.Code != http.StatusOK {
		t.Fatalf("save into an empty ConfigMap = %d %s", rr.Code, rr.Body.String())
	}
	got := decodeInto[map[string]string](t, send(t, server, http.MethodGet, "/api/ai/report", ""))
	if got["report"] != "# Only" || got["generatedAt"] == "" {
		t.Errorf("GET = %v", got)
	}
}

func TestAIReportGenerateWritesForTheAudienceAndSavesIt(t *testing.T) {
	const answer = "## Waste\nShrink dev-api."
	modelURL, model := streamingModel(t, answer)
	server := reportServer(t, modelURL)

	if !strings.Contains(send(t, server, http.MethodGet, "/api/finops/overview", "").Body.String(), labelOnlyTheDashboardShows) {
		t.Fatal("fixture: the overview must carry the namespace label for this test to mean anything")
	}

	anna := &auth.Identity{Subject: "user:anna", Name: "Anna", Role: auth.RoleOperator}
	rr := sendAs(t, server, anna, http.MethodPost, "/api/ai/report/generate", `{"audience":"engineering"}`)
	if rr.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("generate = %d %s", rr.Code, rr.Body.String())
	}
	events := streamEvents(t, rr.Body.String())
	saved, ok := eventOf(events, "saved")
	if !ok || events[len(events)-1].Type != "done" {
		t.Fatalf("stream = %+v, want a saved event and done last", events)
	}

	reports, err := finops.ListReports(context.Background(), server.Client, operatorNamespace)
	if err != nil || len(reports) != 1 {
		t.Fatalf("reports = %+v (%v)", reports, err)
	}
	r := reports[0]
	if r.ID != saved.Text || r.Kind != finops.ReportAI || r.Title != "Engineering waste report" || r.Audience != "engineering" || r.By != "Anna" {
		t.Errorf("stored report = %+v, saved event %q", r, saved.Text)
	}
	if _, md, err := finops.GetReport(context.Background(), server.Client, operatorNamespace, r.ID); err != nil || md != answer {
		t.Errorf("stored markdown = %q (%v)", md, err)
	}
	if got := decodeInto[map[string]string](t, send(t, server, http.MethodGet, "/api/ai/report", "")); got["report"] != answer {
		t.Errorf("the latest report must also be what GET /api/ai/report reads, got %v", got)
	}

	requests := model.all()
	if len(requests) != 1 {
		t.Fatalf("model calls = %d, want 1", len(requests))
	}
	prompt := modelMessages(t, requests[0].Body)
	if !strings.HasPrefix(prompt["system"], reportSystemPrompt) || !strings.Contains(prompt["system"], "Audience: platform and application engineers.") {
		t.Errorf("system prompt does not brief the engineering audience:\n%s", prompt["system"])
	}
	for _, section := range []string{"## Cost overview", "## Last week against the week before", "## Nodes", "## Schedules", `"nightly-shutdown"`, `"dev-api"`} {
		if !strings.Contains(prompt["user"], section) {
			t.Errorf("report data lacks %s:\n%s", section, prompt["user"])
		}
	}
	if strings.Contains(prompt["user"], labelOnlyTheDashboardShows) {
		t.Error("namespace labels inflate the prompt; the report data must leave them out")
	}
}

func TestAIReportGenerateFallsBackToTheExecutiveReport(t *testing.T) {
	modelURL, model := streamingModel(t, "One page.")
	server := reportServer(t, modelURL)

	rr := send(t, server, http.MethodPost, "/api/ai/report/generate", `{"audience":"board-of-directors"}`)
	if _, ok := eventOf(streamEvents(t, rr.Body.String()), "saved"); !ok {
		t.Fatalf("stream = %s", rr.Body.String())
	}
	reports, _ := finops.ListReports(context.Background(), server.Client, operatorNamespace)
	if len(reports) != 1 || reports[0].Title != "Executive cost report" || reports[0].Audience != defaultAudience || reports[0].By != "" {
		t.Errorf("an unknown audience must get the executive report, got %+v", reports)
	}
	if prompt := modelMessages(t, model.all()[0].Body); !strings.Contains(prompt["system"], "Audience: engineering and finance leadership.") {
		t.Errorf("system prompt = %s", prompt["system"])
	}
}

func TestAIReportGenerateReportsAModelFailureAndSavesNothing(t *testing.T) {
	srv, _ := recordingServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"model overloaded"}}`, http.StatusInternalServerError)
	})
	server := reportServer(t, srv.URL+"/v1")

	events := streamEvents(t, send(t, server, http.MethodPost, "/api/ai/report/generate", "").Body.String())
	failure, ok := eventOf(events, "error")
	if !ok || !strings.Contains(failure.Text, "model overloaded") || events[len(events)-1].Type != "done" {
		t.Fatalf("stream = %+v, want the provider's error and done", events)
	}
	if _, saved := eventOf(events, "saved"); saved {
		t.Error("a failed generation must not report a saved report")
	}
	if reports, _ := finops.ListReports(context.Background(), server.Client, operatorNamespace); len(reports) != 0 {
		t.Errorf("reports after a failure = %+v", reports)
	}
	if got := decodeInto[map[string]string](t, send(t, server, http.MethodGet, "/api/ai/report", "")); got["report"] != "" {
		t.Errorf("latest report after a failure = %v", got)
	}
}

func TestAIReportGenerateNeedsTheAIIntegration(t *testing.T) {
	t.Setenv("POD_NAMESPACE", operatorNamespace)
	server := buildMockServerWithK8s()
	rr := send(t, server, http.MethodPost, "/api/ai/report/generate", `{}`)
	if rr.Code != http.StatusBadRequest || !strings.Contains(errorMessage(t, rr), "disabled") {
		t.Errorf("generate with AI off = %d %s", rr.Code, rr.Body.String())
	}
}

func TestReportDataSaysWhatIsUnavailable(t *testing.T) {
	t.Setenv("POD_NAMESPACE", operatorNamespace)
	server := interceptedServer(interceptor.Funcs{List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
		switch list.(type) {
		case *corev1.NodeList:
			return errors.New("nodes are unreadable")
		case *finopsv1.ScalingGroupList:
			return errors.New("groups are unreadable")
		}
		return c.List(ctx, list, opts...)
	}})

	data := server.reportData(context.Background())
	for _, want := range []string{
		"## Cost overview (run rate, efficiency, savings, opportunities)\n```json\nunavailable: nodes are unreadable\n```",
		"## Schedules\n```json\nunavailable: groups are unreadable\n```",
	} {
		if !strings.Contains(data, want) {
			t.Errorf("report data lacks %q:\n%s", want, data)
		}
	}
	for _, missing := range []string{"## Nodes", "## Last week"} {
		if strings.Contains(data, missing) {
			t.Errorf("a section without data must be left out, found %s:\n%s", missing, data)
		}
	}
}
