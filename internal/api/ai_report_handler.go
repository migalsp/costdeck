package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/migalsp/costdeck-operator/internal/ai"
	"github.com/migalsp/costdeck-operator/internal/auth"
	"github.com/migalsp/costdeck-operator/internal/config"
	"github.com/migalsp/costdeck-operator/internal/finops"
)

const (
	reportConfigMap = "costdeck-ai-report"
	reportKey       = "report.md"
)

const reportSystemPrompt = `You are a senior FinOps analyst writing a cost report about one Kubernetes cluster.

Use only the data provided. Every money figure is computed by Cost Deck from the stated pricing basis: quote the figures as given, never recompute or invent them, and say once, near the top, that they are estimates at list prices. Where data is missing, say what is missing instead of guessing. Write in Markdown with short sections and tables where they help.`

// reportAudiences tailor the report to who reads it.
var reportAudiences = map[string]struct{ title, brief string }{
	"executive": {
		title: "Executive cost report",
		brief: `Audience: engineering and finance leadership. One page.
1. Headline: monthly run rate, last week against the week before, month to date and forecast.
2. Efficiency: what share of the node bill pods use, in plain words.
3. What already saves money: the schedules and what they saved.
4. Three decisions, each with its monthly saving and who should act.`,
	},
	"engineering": {
		title: "Engineering waste report",
		brief: `Audience: platform and application engineers.
1. Waste by namespace: a table of the most over-requested namespaces (requested vs used, idle cost per month).
2. Right-sizing: the namespaces with the largest advice and what to change.
3. Scheduling: non-production namespaces that run around the clock and the saving from working hours.
4. Nodes: capacity nobody requests, nodes that could be emptied, and the node shape.
5. Hygiene: namespaces without requests or limits.
End with a checklist ordered by saving.`,
	},
	"showback": {
		title: "Team showback report",
		brief: `Audience: team leads, for showback.
1. Cost by team (and by namespace inside each team) for the last week, with the change against the week before.
2. Efficiency per team: what share of what they request they use.
3. What each team can do to lower its cost, with the saving.
Note that unrequested node capacity is shared out in proportion to requests only if stated, and that namespaces without a team label are listed separately.`,
	},
}

const defaultAudience = "executive"

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

// reportData gathers the facts the report is written from: the same overview, digest and
// node view the dashboard shows, so the report and the screens agree.
func (s *Server) reportData(ctx context.Context) string {
	var b strings.Builder
	section := func(title string, v any, err error) {
		out := "unavailable"
		if err != nil {
			out += ": " + err.Error()
		} else if raw, jerr := json.MarshalIndent(v, "", "  "); jerr == nil {
			out = string(raw)
		}
		fmt.Fprintf(&b, "## %s\n```json\n%s\n```\n\n", title, out)
	}
	overview, err := s.finopsOverview(ctx)
	section("Cost overview (run rate, efficiency, savings, opportunities)", compactOverview(overview), err)
	if err == nil {
		ledger, lerr := s.costLedger(ctx)
		if lerr == nil {
			section("Last week against the week before", finops.NewDigest(overview, ledger, finops.PeriodWeek, time.Now()), nil)
		}
	}
	var nodes corev1.NodeList
	var pods corev1.PodList
	if err := s.Client.List(ctx, &nodes); err == nil && s.Client.List(ctx, &pods) == nil {
		rep := finops.BuildNodes(nodes.Items, pods.Items, s.getNodeMetricsMap(ctx), s.costRates(ctx), time.Now())
		section("Nodes", map[string]any{"summary": rep.Summary, "pools": rep.Pools, "findings": rep.Findings}, nil)
	}
	groups, gerr := s.toolListGroups(ctx)
	fmt.Fprintf(&b, "## Schedules\n```json\n%s\n```\n", orUnavailable(groups, gerr))
	return b.String()
}

func orUnavailable(v string, err error) string {
	if err != nil {
		return "unavailable: " + err.Error()
	}
	return v
}

// compactOverview drops the per-namespace trend arrays, which only inflate the prompt.
func compactOverview(o finops.Overview) finops.Overview {
	rows := make([]finops.NamespaceRow, len(o.Namespaces))
	for i, r := range o.Namespaces {
		r.Trend, r.Labels = nil, nil
		rows[i] = r
	}
	o.Namespaces = rows
	o.History = finops.History{}
	return o
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
	var req struct {
		Audience string `json:"audience"`
	}
	if r.ContentLength > 0 {
		_ = json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req)
	}
	audience, ok := reportAudiences[req.Audience]
	if !ok {
		req.Audience = defaultAudience
		audience = reportAudiences[defaultAudience]
	}
	agent := &ai.Agent{Provider: provider, System: reportSystemPrompt + "\n\n" + audience.brief}
	prompt := "Write the report for this cluster from the data below (collected " +
		time.Now().UTC().Format(time.RFC1123) + ").\n\n" + s.reportData(ctx)
	if err := agent.Run(ctx, []ai.Message{{Role: "user", Content: prompt}}, collect); err != nil {
		logf.FromContext(ctx).Error(err, "Report generation failed")
		stream.send(ai.Event{Type: "error", Text: userMessage(err)})
		stream.send(ai.Event{Type: "done"})
		return
	}
	if text := strings.TrimSpace(report.String()); text != "" {
		by := ""
		if id := auth.FromContext(r.Context()); id != nil {
			by = id.Name
		}
		entry := finops.ReportEntry{Kind: finops.ReportAI, Title: audience.title, Audience: req.Audience, By: by}
		if saved, err := finops.SaveReport(ctx, s.Client, config.OperatorNamespace(), entry, text); err != nil {
			stream.send(ai.Event{Type: "error", Text: "The report was generated but could not be saved: " + err.Error()})
		} else {
			stream.send(ai.Event{Type: "saved", Text: saved.ID})
		}
		// The latest report also stays where GET /api/ai/report reads it.
		_ = s.saveReport(ctx, text)
	}
	stream.send(ai.Event{Type: "done"})
}
