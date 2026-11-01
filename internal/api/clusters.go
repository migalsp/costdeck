package api

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/config"
	"github.com/migalsp/costdeck-operator/internal/finops"
	"github.com/migalsp/costdeck-operator/internal/netguard"
	"github.com/migalsp/costdeck-operator/internal/scaling"
)

// A Cost Deck can show other installations next to its own: each serves a summary of its
// cluster (GET /api/finops/summary), which this one reads with a viewer API token. An
// installation too old to serve a summary is read from its overview instead.

const (
	clusterTokensSecret = "costdeck-cluster-tokens"
	clusterSummaryTTL   = time.Minute
)

// localName is what the Clusters page calls this installation.
func localName(cfg *finopsv1.CostDeckConfig) string {
	if cfg.Spec.ClusterName != "" {
		return cfg.Spec.ClusterName
	}
	return thisCluster
}

// thisCluster names an installation without spec.clusterName.
const thisCluster = "this cluster"

// summary condenses this installation's overview, with its schedules.
func (s *Server) summary(ctx context.Context) (finops.ClusterSummary, error) {
	cfg, err := config.Get(ctx, s.Client)
	if err != nil {
		return finops.ClusterSummary{}, err
	}
	o, err := s.finopsOverview(ctx)
	if err != nil {
		return finops.ClusterSummary{}, err
	}
	sum := finops.SummaryOf(localName(cfg), o)
	var groups finopsv1.ScalingGroupList
	if err := s.Client.List(ctx, &groups, client.InNamespace(config.OperatorNamespace())); err == nil {
		for _, g := range groups.Items {
			sum.Schedules++
			if g.Status.Phase == scaling.PhaseScaledDown {
				sum.SchedulesDown++
			}
		}
	}
	var configs finopsv1.ScalingConfigList
	if err := s.Client.List(ctx, &configs, client.InNamespace(config.OperatorNamespace())); err == nil {
		for _, c := range configs.Items {
			sum.Schedules++
			if c.Status.Phase == scaling.PhaseScaledDown {
				sum.SchedulesDown++
			}
		}
	}
	return sum, nil
}

func (s *Server) handleSummary(w http.ResponseWriter, r *http.Request) {
	sum, err := s.summary(r.Context())
	if err != nil {
		writeK8sError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sum)
}

// clusterRow is one installation on the Clusters page.
type clusterRow struct {
	Name    string                 `json:"name"`
	URL     string                 `json:"url,omitempty"`
	Local   bool                   `json:"local"`
	Error   string                 `json:"error,omitempty"`
	Summary *finops.ClusterSummary `json:"summary,omitempty"`
}

// clusterTotals adds up the installations that report in one currency.
type clusterTotals struct {
	Currency         string  `json:"currency"`
	Clusters         int     `json:"clusters"`
	Nodes            int     `json:"nodes"`
	MonthlyCost      float64 `json:"monthlyCost"`
	SchedulesSaving  float64 `json:"schedulesSaving"`
	SavingsAvailable float64 `json:"savingsAvailable"`
	MonthToDate      float64 `json:"monthToDate"`
	Forecast         float64 `json:"forecast"`
}

type clustersResponse struct {
	Clusters []clusterRow    `json:"clusters"`
	Totals   []clusterTotals `json:"totals"`
}

// remoteCache keeps each installation's summary for a minute, so a page that polls does
// not ask every cluster on every poll.
type remoteCache struct {
	mu      sync.Mutex
	entries map[string]remoteEntry
}

type remoteEntry struct {
	at  time.Time
	sum finops.ClusterSummary
	err error
}

func (s *Server) handleClusters(w http.ResponseWriter, r *http.Request) {
	resp, err := s.clusters(r.Context())
	if err != nil {
		writeK8sError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) clusters(ctx context.Context) (clustersResponse, error) {
	cfg, err := config.Get(ctx, s.Client)
	if err != nil {
		return clustersResponse{}, err
	}
	rows := make([]clusterRow, 1+len(cfg.Spec.Clusters))
	rows[0] = clusterRow{Name: localName(cfg), Local: true}
	if sum, err := s.summary(ctx); err != nil {
		rows[0].Error = err.Error()
	} else {
		rows[0].Summary = &sum
	}
	var wg sync.WaitGroup
	for i, rc := range cfg.Spec.Clusters {
		wg.Go(func() {
			row := clusterRow{Name: rc.Name, URL: rc.URL}
			if sum, err := s.cachedRemote(ctx, rc); err != nil {
				row.Error = err.Error()
			} else {
				row.Summary = &sum
			}
			rows[i+1] = row
		})
	}
	wg.Wait()
	return clustersResponse{Clusters: rows, Totals: totals(rows)}, nil
}

func totals(rows []clusterRow) []clusterTotals {
	byCur := map[string]*clusterTotals{}
	for _, r := range rows {
		if r.Summary == nil {
			continue
		}
		t := byCur[r.Summary.Currency]
		if t == nil {
			t = &clusterTotals{Currency: r.Summary.Currency}
			byCur[r.Summary.Currency] = t
		}
		t.Clusters++
		t.Nodes += r.Summary.Nodes
		t.MonthlyCost += r.Summary.MonthlyCost
		t.SchedulesSaving += r.Summary.SchedulesSaving
		t.SavingsAvailable += r.Summary.SavingsAvailable
		t.MonthToDate += r.Summary.MonthToDate
		t.Forecast += r.Summary.Forecast
	}
	out := make([]clusterTotals, 0, len(byCur))
	for _, t := range byCur {
		out = append(out, *t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Currency < out[j].Currency })
	return out
}

func (s *Server) cachedRemote(ctx context.Context, rc finopsv1.RemoteCluster) (finops.ClusterSummary, error) {
	key := rc.Name + "\x00" + rc.URL
	s.remotes.mu.Lock()
	if e, ok := s.remotes.entries[key]; ok && time.Since(e.at) < clusterSummaryTTL {
		s.remotes.mu.Unlock()
		return e.sum, e.err
	}
	s.remotes.mu.Unlock()
	sum, err := s.remoteSummary(ctx, rc, s.secretValue(ctx, rc.SecretRef, rc.Name))
	s.remotes.mu.Lock()
	if s.remotes.entries == nil {
		s.remotes.entries = map[string]remoteEntry{}
	}
	s.remotes.entries[key] = remoteEntry{at: time.Now(), sum: sum, err: err}
	s.remotes.mu.Unlock()
	return sum, err
}

// remoteSummary reads another installation's summary, or its overview when it is too old
// to serve one.
func (s *Server) remoteSummary(ctx context.Context, rc finopsv1.RemoteCluster, token string) (finops.ClusterSummary, error) {
	if token == "" {
		return finops.ClusterSummary{}, fmt.Errorf("no API token for %s", rc.Name)
	}
	hc := s.HTTPClient
	if hc == nil {
		t := netguard.Transport()
		if rc.SkipTLSVerify {
			t.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // the admin chose to skip verification for this cluster
		}
		hc = &http.Client{Timeout: 15 * time.Second, Transport: t}
	}
	get := func(path string, out any) (int, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(rc.URL, "/")+path, nil)
		if err != nil {
			return 0, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := hc.Do(req)
		if err != nil {
			if ue, ok := err.(*url.Error); ok {
				err = ue.Err
			}
			return 0, fmt.Errorf("could not reach %s: %w", req.URL.Host, err)
		}
		defer func() { _ = res.Body.Close() }()
		switch {
		case res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden:
			return res.StatusCode, fmt.Errorf("%s refused the API token (%s)", req.URL.Host, res.Status)
		case res.StatusCode/100 != 2:
			detail, _ := io.ReadAll(io.LimitReader(res.Body, 200))
			return res.StatusCode, fmt.Errorf("%s answered %s %s", req.URL.Host, res.Status, strings.TrimSpace(string(detail)))
		}
		return res.StatusCode, json.NewDecoder(io.LimitReader(res.Body, 8<<20)).Decode(out)
	}
	var sum finops.ClusterSummary
	code, err := get("/api/finops/summary", &sum)
	if code == http.StatusNotFound {
		var o finops.Overview
		if _, err := get("/api/finops/overview", &o); err != nil {
			return finops.ClusterSummary{}, err
		}
		return finops.SummaryOf(rc.Name, o), nil
	}
	if err != nil {
		return finops.ClusterSummary{}, err
	}
	sum.Name = rc.Name
	return sum, nil
}

// ClusterSettings is spec.clusters as the settings API reads it; tokens are only reported.
type ClusterSettings struct {
	Name          string `json:"name"`
	URL           string `json:"url"`
	SkipTLSVerify bool   `json:"skipTlsVerify"`
	HasToken      bool   `json:"hasToken"`
}

// ClusterUpdate is one installation in a settings update, which replaces the list. A
// token left out keeps the stored one of that name.
type ClusterUpdate struct {
	Name          string `json:"name"`
	URL           string `json:"url"`
	Token         string `json:"token,omitempty"`
	SkipTLSVerify bool   `json:"skipTlsVerify"`
}

func clusterSettings(list []finopsv1.RemoteCluster) []ClusterSettings {
	out := make([]ClusterSettings, 0, len(list))
	for _, c := range list {
		out = append(out, ClusterSettings{Name: c.Name, URL: c.URL, SkipTLSVerify: c.SkipTLSVerify, HasToken: c.SecretRef != ""})
	}
	return out
}

// checkCluster validates a name and URL before they are stored.
func checkCluster(name, raw string) error {
	if errs := validation.IsDNS1123Label(name); len(errs) > 0 {
		return fmt.Errorf("cluster name %q: %s", name, strings.Join(errs, "; "))
	}
	u, ok := httpURL(raw)
	if !ok {
		return fmt.Errorf("cluster %s: the URL must be http or https, such as https://costdeck.eu.example.com", name)
	}
	if netguard.ForbiddenHost(u.Hostname()) {
		return fmt.Errorf("cluster %s: %w", name, netguard.ErrForbiddenAddress)
	}
	return nil
}

func (s *Server) applyClusterSettings(ctx context.Context, cfg *finopsv1.CostDeckConfig, req *SettingsUpdateRequest) error {
	if req.Clusters == nil {
		return nil
	}
	if len(*req.Clusters) > 50 {
		return badRequestf("at most 50 clusters")
	}
	tokens, err := s.secretDataOrEmpty(ctx, clusterTokensSecret)
	if err != nil {
		return err
	}
	had := map[string]bool{}
	for _, c := range cfg.Spec.Clusters {
		had[c.Name] = c.SecretRef != ""
	}
	var out []finopsv1.RemoteCluster
	seen := map[string]bool{}
	for _, u := range *req.Clusters {
		u.Name, u.URL = strings.TrimSpace(u.Name), strings.TrimRight(strings.TrimSpace(u.URL), "/")
		if err := checkCluster(u.Name, u.URL); err != nil {
			return badRequest{err}
		}
		if seen[u.Name] {
			return badRequestf("cluster %s is listed twice", u.Name)
		}
		seen[u.Name] = true
		if u.Token != "" {
			tokens[u.Name] = []byte(strings.TrimSpace(u.Token))
		} else if !had[u.Name] {
			return badRequestf("cluster %s needs a viewer API token of that installation", u.Name)
		}
		out = append(out, finopsv1.RemoteCluster{Name: u.Name, URL: u.URL, SkipTLSVerify: u.SkipTLSVerify, SecretRef: clusterTokensSecret})
	}
	for name := range tokens {
		if !seen[name] {
			delete(tokens, name)
		}
	}
	cfg.Spec.Clusters = out
	if len(out) == 0 && len(had) == 0 {
		return nil
	}
	ref := clusterTokensSecret
	s.storeCredentials(req, clusterTokensSecret, tokens, &ref)
	return nil
}

// testCluster reaches another installation with the unsaved values of the form, the
// stored token when none is typed.
func (s *Server) testCluster(w http.ResponseWriter, r *http.Request) {
	var u ClusterUpdate
	if err := decodeJSON(w, r, &u); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	u.Name, u.URL = strings.TrimSpace(u.Name), strings.TrimRight(strings.TrimSpace(u.URL), "/")
	if err := checkCluster(u.Name, u.URL); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"connected": false, "error": err.Error()})
		return
	}
	token := u.Token
	if token == "" {
		token = s.secretValue(r.Context(), clusterTokensSecret, u.Name)
	}
	sum, err := s.remoteSummary(r.Context(), finopsv1.RemoteCluster{Name: u.Name, URL: u.URL, SkipTLSVerify: u.SkipTLSVerify}, token)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"connected": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"connected": true, "message": fmt.Sprintf("Reached %s: %d nodes, %.0f %s a month", u.Name, sum.Nodes, sum.MonthlyCost, sum.Currency)})
}
