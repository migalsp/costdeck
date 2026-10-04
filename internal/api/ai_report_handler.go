package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/migalsp/costdeck-operator/internal/ai"
	"github.com/migalsp/costdeck-operator/internal/config"
)

const (
	reportConfigMap = "costdeck-ai-report"
	reportKey       = "report.md"
)

const reportSystemPrompt = `You are a senior FinOps analyst writing a cost report about one Kubernetes cluster for its engineering and finance stakeholders.

Use only the data provided. All money figures are estimates derived from the stated pricing basis; say so once, near the top. Where data is missing, say what is missing instead of guessing.

Structure the report in Markdown:
1. Executive summary: estimated monthly cost, estimated waste, and the three highest-value actions.
2. What is working: schedules and groups that already save money.
3. Waste: the most over-provisioned namespaces in a table (requests vs usage, estimated monthly waste).
4. Scheduling opportunities: always-on or manually pinned groups and namespaces that could follow a schedule, with an estimated saving.
5. Recommendations: concrete next steps, ordered by estimated saving.`

func (s *Server) handleAIReportGet(w http.ResponseWriter, r *http.Request) {
	cm := &corev1.ConfigMap{}
	err := s.Client.Get(r.Context(), client.ObjectKey{Name: reportConfigMap, Namespace: config.OperatorNamespace()}, cm)
	switch {
	case apierrors.IsNotFound(err):
		writeJSON(w, http.StatusOK, map[string]string{"report": ""})
	case err != nil:
		writeError(w, http.StatusInternalServerError, "Failed to read the saved report")
	default:
		writeJSON(w, http.StatusOK, map[string]string{"report": cm.Data[reportKey], "generatedAt": cm.Annotations["costdeck.io/generated-at"]})
	}
}

func (s *Server) handleAIReportSave(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Report string `json:"report"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid request")
		return
	}
	if err := s.saveReport(r.Context(), req.Report); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// saveReport stores the latest report in a ConfigMap in the operator namespace.
func (s *Server) saveReport(ctx context.Context, report string) error {
	key := client.ObjectKey{Name: reportConfigMap, Namespace: config.OperatorNamespace()}
	now := time.Now().UTC().Format(time.RFC3339)
	cm := &corev1.ConfigMap{}
	err := s.Client.Get(ctx, key, cm)
	if apierrors.IsNotFound(err) {
		cm = &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name: key.Name, Namespace: key.Namespace,
				Labels:      map[string]string{"app.kubernetes.io/managed-by": "costdeck-operator"},
				Annotations: map[string]string{"costdeck.io/generated-at": now},
			},
			Data: map[string]string{reportKey: report},
		}
		return s.Client.Create(ctx, cm)
	}
	if err != nil {
		return err
	}
	if cm.Data == nil {
		cm.Data = map[string]string{}
	}
	if cm.Annotations == nil {
		cm.Annotations = map[string]string{}
	}
	cm.Data[reportKey] = report
	cm.Annotations["costdeck.io/generated-at"] = now
	return s.Client.Update(ctx, cm)
}

// reportData gathers the facts the report is written from, using the assistant's own tools.
func (s *Server) reportData(ctx context.Context) string {
	var b strings.Builder
	sections := []struct {
		title string
		fn    func(context.Context) (string, error)
	}{
		{"Cluster overview", s.toolClusterOverview},
		{"Scaling groups", s.toolListGroups},
		{"Namespace scaling configs", s.toolListConfigs},
		{"Namespaces by estimated monthly cost", s.toolListNamespaces},
	}
	for _, sec := range sections {
		out, err := sec.fn(ctx)
		if err != nil {
			out = "unavailable: " + err.Error()
		}
		fmt.Fprintf(&b, "## %s\n```json\n%s\n```\n\n", sec.title, out)
	}
	return b.String()
}

// handleAIReportGenerate streams a new report and saves it when complete.
func (s *Server) handleAIReportGenerate(w http.ResponseWriter, r *http.Request) {
	provider, ok := s.loadAIProvider(w, r)
	if !ok {
		return
	}
	stream, ok := newEventStream(w)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming is not supported")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Minute)
	defer cancel()

	var report strings.Builder
	collect := func(e ai.Event) {
		switch e.Type {
		case "text":
			report.WriteString(e.Text)
		case "reset":
			report.Reset()
		}
		stream.send(e)
	}
	agent := &ai.Agent{Provider: provider, System: reportSystemPrompt}
	prompt := "Write the FinOps report for this cluster from the data below (collected " +
		time.Now().UTC().Format(time.RFC1123) + ").\n\n" + s.reportData(ctx)
	if err := agent.Run(ctx, []ai.Message{{Role: "user", Content: prompt}}, collect); err != nil {
		logf.FromContext(ctx).Error(err, "Report generation failed")
		stream.send(ai.Event{Type: "error", Text: userMessage(err)})
		stream.send(ai.Event{Type: "done"})
		return
	}
	if text := strings.TrimSpace(report.String()); text != "" {
		if err := s.saveReport(ctx, text); err != nil {
			stream.send(ai.Event{Type: "error", Text: "The report was generated but could not be saved: " + err.Error()})
		}
	}
	stream.send(ai.Event{Type: "done"})
}
