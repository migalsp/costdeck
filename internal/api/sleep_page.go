package api

import (
	"context"
	"html/template"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/config"
	"github.com/migalsp/costdeck-operator/internal/scaling"
)

// The sleep page is what a namespace's HTTPRoutes show while its schedule keeps it down. It
// is a page of its own, not the dashboard: it carries no session, so the application's
// host never receives a Cost Deck cookie, and waking happens in the dashboard, where the
// person signs in and the manual override applies.

// sleepPageData is what the page shows.
type sleepPageData struct {
	Namespace string
	Schedule  string
	// NextStart is when the schedule brings the namespace up by itself, if it does;
	// Starting is set once it is on its way up.
	NextStart *time.Time
	Starting  bool
	// WakeURL opens the dashboard's wake page, empty without a configured dashboard URL.
	WakeURL string
}

// sleepPageCSS styles the page in the visitor's light or dark scheme; every colour pair
// that carries text keeps the WCAG AA contrast of 4.5:1.
const sleepPageCSS = `:root{color-scheme:light dark;--bg:#f8fafc;--card:#fff;--text:#0f172a;--muted:#64748b;--line:#e2e8f0;--brand:#0f766e;--on-brand:#fff}
@media (prefers-color-scheme:dark){:root{--bg:#0f172a;--card:#1e293b;--text:#f1f5f9;--muted:#94a3b8;--line:#334155;--brand:#2dd4bf;--on-brand:#042f2e}}
body{margin:0;min-height:100vh;display:grid;place-items:center;background:var(--bg);color:var(--text);font:16px/1.5 system-ui,sans-serif;padding:16px;box-sizing:border-box}
main{max-width:30rem;background:var(--card);border:1px solid var(--line);border-radius:16px;padding:2rem}
h1{font-size:1.4rem;margin:0 0 .5rem}p{color:var(--muted);margin:.5rem 0}
a.wake{display:inline-block;margin-top:1rem;padding:.6rem 1.1rem;border-radius:10px;background:var(--brand);color:var(--on-brand);text-decoration:none;font-weight:600}
small{display:block;margin-top:1.5rem;color:var(--muted)}
`

var sleepPageTemplate = template.Must(template.New("sleep").Funcs(template.FuncMap{
	"when": func(t time.Time) string { return t.UTC().Format("Mon 2 Jan, 15:04 MST") },
}).Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<meta http-equiv="refresh" content="20">
<title>{{.Namespace}} is asleep</title>
<style>
` + sleepPageCSS + `</style></head>
<body><main>
{{if .Starting}}<h1>{{.Namespace}} is starting</h1>
<p>It will be back in a moment; this page opens the application by itself.</p>
{{else}}<h1>{{.Namespace}} is asleep</h1>
<p>Its schedule{{if .Schedule}} <b>{{.Schedule}}</b>{{end}} keeps it scaled down to save money{{if .NextStart}}, until {{when .NextStart}}{{end}}.</p>
{{if .WakeURL}}<a class="wake" href="{{.WakeURL}}">Wake it in Cost Deck</a>
<p>Signing in with an operator role starts it for a while; this page then opens the application by itself.</p>
{{else}}<p>Someone with the operator role can start it in Cost Deck.</p>{{end}}{{end}}
<small>Cost Deck · this page checks again every 20 seconds</small>
</main></body></html>
`))

// writeSleepPage answers a request a sleeping namespace's route sent to Cost Deck: the page
// for browsers, a short JSON error for everything else, both 503 with Retry-After, so
// crawlers and clients back off instead of treating it as the application's content.
func writeSleepPage(w http.ResponseWriter, r *http.Request, d sleepPageData) {
	w.Header().Set("Retry-After", "60")
	w.Header().Set("Cache-Control", "no-store")
	if !strings.Contains(r.Header.Get("Accept"), "text/html") {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": d.Namespace + " is asleep: its schedule keeps it scaled down"})
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusServiceUnavailable)
	_ = sleepPageTemplate.Execute(w, d)
}

// wakeURL is the dashboard's wake page for a namespace, returning to the address the
// person opened.
func wakeURL(dashboard, ns string, r *http.Request) string {
	if dashboard == "" {
		return ""
	}
	scheme := r.Header.Get("X-Forwarded-Proto")
	if scheme == "" {
		scheme = "https"
	}
	back := scheme + "://" + r.Host + r.URL.RequestURI()
	return strings.TrimRight(dashboard, "/") + "/wake/" + url.PathEscape(ns) + "?return=" + url.QueryEscape(back)
}

// serveSleepPage answers what a sleeping namespace's routes send: the gateway marks those
// requests with scaling.SleepingHeader. Only a namespace whose routes Cost Deck put to
// sleep gets its details shown, so the header cannot be used to ask about others.
func (s *Server) serveSleepPage(w http.ResponseWriter, r *http.Request) {
	ns := r.Header.Get(scaling.SleepingHeader)
	ctx := r.Context()
	if ns == "" || !slices.Contains(s.sleeping.get(ctx, (&scaling.Engine{Client: s.Client}).SleepingNamespaces, time.Now()), ns) {
		writeSleepPage(w, r, sleepPageData{Namespace: "This application"})
		return
	}
	d := sleepPageData{Namespace: ns}
	if st, _, name := s.scheduleOf(ctx, ns); st != nil {
		d.Schedule = name
		if st.DesiredState == scaling.StateUp {
			d.Starting = true
		} else if st.NextTransition != nil {
			t := st.NextTransition.Time.Time
			d.NextStart = &t
		}
	}
	if cfg, err := config.Get(ctx, s.Client); err == nil {
		d.WakeURL = wakeURL(cfg.Spec.DashboardURL, ns, r)
	}
	writeSleepPage(w, r, d)
}

const sleepingRefresh = 10 * time.Second

type sleepingNamespaces struct {
	mu    sync.Mutex
	names []string
	at    time.Time
}

func (c *sleepingNamespaces) get(ctx context.Context, load func(context.Context) ([]string, error), now time.Time) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.at.IsZero() && now.Sub(c.at) < sleepingRefresh {
		return c.names
	}
	names, err := load(ctx)
	if err != nil {
		return c.names
	}
	c.names, c.at = names, now
	return names
}

// scheduleOf finds the schedule that scales a namespace: the group it belongs to (the
// older one, if several), else its own config.
func (s *Server) scheduleOf(ctx context.Context, ns string) (*finopsv1.ScheduleStatus, string, string) {
	var groups finopsv1.ScalingGroupList
	if err := s.Client.List(ctx, &groups, client.InNamespace(config.OperatorNamespace())); err == nil {
		sort.Slice(groups.Items, func(i, j int) bool {
			return groups.Items[i].CreationTimestamp.Before(&groups.Items[j].CreationTimestamp)
		})
		for i := range groups.Items {
			if slices.Contains(groups.Items[i].Members(), ns) {
				return &groups.Items[i].Status.ScheduleStatus, targetGroup, groups.Items[i].Name
			}
		}
	}
	var configs finopsv1.ScalingConfigList
	if err := s.Client.List(ctx, &configs, client.InNamespace(config.OperatorNamespace())); err == nil {
		for i := range configs.Items {
			if configs.Items[i].Spec.TargetNamespace == ns {
				return &configs.Items[i].Status.ScheduleStatus, kindConfig, configs.Items[i].Name
			}
		}
	}
	return nil, "", ""
}

// wakeStatusResponse is GET /api/wake/{ns}: what the dashboard's wake page needs.
type wakeStatusResponse struct {
	Namespace         string        `json:"namespace"`
	Schedule          *wakeSchedule `json:"schedule,omitempty"`
	DesiredState      string        `json:"desiredState,omitempty"`
	Phase             string        `json:"phase,omitempty"`
	Mode              string        `json:"mode,omitempty"`
	OverrideExpiresAt *metav1.Time  `json:"overrideExpiresAt,omitempty"`
	Sleeping          bool          `json:"sleeping"`
	Hostnames         []string      `json:"hostnames"`
}

type wakeSchedule struct {
	Kind string `json:"kind"` // group or config
	Name string `json:"name"`
}

func (s *Server) handleWakeStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ns := r.PathValue("ns")
	engine := &scaling.Engine{Client: s.Client}
	resp := wakeStatusResponse{Namespace: ns, Hostnames: []string{}}
	if hosts, err := engine.RouteHostnames(ctx, ns); err == nil && hosts != nil {
		resp.Hostnames = hosts
	}
	if sleeping, err := engine.SleepingNamespaces(ctx); err == nil {
		resp.Sleeping = slices.Contains(sleeping, ns)
	}
	if st, kind, name := s.scheduleOf(ctx, ns); st != nil {
		resp.Schedule = &wakeSchedule{Kind: kind, Name: name}
		resp.DesiredState, resp.Mode, resp.OverrideExpiresAt = st.DesiredState, st.Mode, st.OverrideExpiresAt
		resp.Phase = s.phaseOf(ctx, kind, name)
	}
	writeJSON(w, http.StatusOK, resp)
}

// phaseOf reads a schedule's phase, which lives beside its schedule status.
func (s *Server) phaseOf(ctx context.Context, kind, name string) string {
	key := client.ObjectKey{Namespace: config.OperatorNamespace(), Name: name}
	if kind == targetGroup {
		var g finopsv1.ScalingGroup
		if s.Client.Get(ctx, key, &g) == nil {
			return g.Status.Phase
		}
		return ""
	}
	var c finopsv1.ScalingConfig
	if s.Client.Get(ctx, key, &c) == nil {
		return c.Status.Phase
	}
	return ""
}
