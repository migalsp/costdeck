package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/migalsp/costdeck-operator/internal/finops"
)

const remoteCurrency = "EUR"

// remote stands in for another Cost Deck: a current one serves its summary, an old one
// only its overview.
func remote(t *testing.T, token string, old bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		switch {
		case r.URL.Path == "/api/finops/summary" && !old:
			_ = json.NewEncoder(w).Encode(finops.ClusterSummary{Name: "what it calls itself", Currency: remoteCurrency, Nodes: 12, MonthlyCost: 4000, SchedulesSaving: 900, Schedules: 5, SchedulesDown: 2})
		case r.URL.Path == "/api/finops/overview" && old:
			o := finops.Overview{Currency: remoteCurrency}
			o.Cluster.Nodes, o.Monthly.Total, o.Savings.SchedulesMonthly = 3, 1000, 100
			_ = json.NewEncoder(w).Encode(o)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestClusterSettings(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "costdeck")
	server := buildMockServerWithK8s()
	server.HTTPClient = http.DefaultClient
	eu := remote(t, "cdk_eu", false)

	for body, why := range map[string]string{
		`{"clusters":[{"name":"eu","url":"` + eu.URL + `"}]}`:                                                              "a new cluster without a token",
		`{"clusters":[{"name":"EU","url":"` + eu.URL + `","token":"x"}]}`:                                                  "a name that is not a DNS label",
		`{"clusters":[{"name":"eu","url":"ftp://eu","token":"x"}]}`:                                                        "an ftp URL",
		`{"clusters":[{"name":"eu","url":"http://169.254.169.254","token":"x"}]}`:                                          "a metadata address",
		`{"clusters":[{"name":"eu","url":"` + eu.URL + `","token":"x"},{"name":"eu","url":"` + eu.URL + `","token":"y"}]}`: "a name twice",
	} {
		if rr := send(t, server, "PUT", "/api/settings", body); rr.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", why, rr.Code, rr.Body.String())
		}
	}

	if rr := send(t, server, "POST", "/api/settings/providers/cluster/test", `{"name":"eu","url":"`+eu.URL+`","token":"cdk_eu"}`); !strings.Contains(rr.Body.String(), `"connected":true`) ||
		!strings.Contains(rr.Body.String(), "12 nodes, 4000 EUR a month") {
		t.Errorf("test: %s", rr.Body.String())
	}

	// A stored token is kept when the list is saved again, and a removed cluster's dropped.
	if rr := send(t, server, "PUT", "/api/settings", `{"clusters":[{"name":"eu","url":"`+eu.URL+`","token":"cdk_eu"},{"name":"gone","url":"`+eu.URL+`","token":"x"}]}`); rr.Code != http.StatusOK {
		t.Fatal(rr.Body.String())
	}
	if rr := send(t, server, "PUT", "/api/settings", `{"clusters":[{"name":"eu","url":"`+eu.URL+`"}]}`); rr.Code != http.StatusOK {
		t.Fatalf("keeping eu with its stored token: %d %s", rr.Code, rr.Body.String())
	}
	var secret corev1.Secret
	_ = server.Client.Get(context.Background(), client.ObjectKey{Namespace: "costdeck", Name: clusterTokensSecret}, &secret)
	if len(secret.Data) != 1 || string(secret.Data["eu"]) != "cdk_eu" {
		t.Errorf("tokens = %v", secret.Data)
	}
}

func TestClustersRead(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "costdeck")
	server := buildMockServerWithK8s()
	server.HTTPClient = http.DefaultClient
	ctx := context.Background()
	eu, old, denied := remote(t, "cdk_eu", false), remote(t, "cdk_old", true), remote(t, "cdk_right", false)
	down := httptest.NewServer(http.NotFoundHandler())
	down.Close()

	all := `{"clusters":[
		{"name":"eu","url":"` + eu.URL + `/","token":"cdk_eu"},
		{"name":"old","url":"` + old.URL + `","token":"cdk_old"},
		{"name":"denied","url":"` + denied.URL + `","token":"cdk_wrong"},
		{"name":"down","url":"` + down.URL + `","token":"x"}]}`
	if rr := send(t, server, "PUT", "/api/settings", all); rr.Code != http.StatusOK {
		t.Fatalf("valid: %d %s", rr.Code, rr.Body.String())
	}
	rr := send(t, server, "GET", "/api/settings", "")
	if strings.Contains(rr.Body.String(), "cdk_eu") {
		t.Error("a token came back")
	}
	var settings struct {
		Clusters []ClusterSettings `json:"clusters"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &settings)
	if len(settings.Clusters) != 4 || !settings.Clusters[0].HasToken || settings.Clusters[0].URL != eu.URL {
		t.Errorf("settings = %+v", settings.Clusters)
	}

	var got clustersResponse
	_ = json.Unmarshal(send(t, server, "GET", "/api/clusters", "").Body.Bytes(), &got)
	if len(got.Clusters) != 5 || !got.Clusters[0].Local || got.Clusters[0].Name != "this cluster" {
		t.Fatalf("clusters = %+v", got)
	}
	byName := map[string]clusterRow{}
	for _, c := range got.Clusters {
		byName[c.Name] = c
	}
	if s := byName["eu"].Summary; s == nil || s.Name != "eu" || s.Nodes != 12 || s.SchedulesDown != 2 {
		t.Errorf("eu = %+v", byName["eu"])
	}
	if s := byName["old"].Summary; s == nil || s.MonthlyCost != 1000 || s.Nodes != 3 {
		t.Errorf("old, read from its overview = %+v", byName["old"])
	}
	if !strings.Contains(byName["denied"].Error, "refused the API token") || !strings.Contains(byName["down"].Error, "could not reach") {
		t.Errorf("errors: denied %q, down %q", byName["denied"].Error, byName["down"].Error)
	}
	var eur *clusterTotals
	for i := range got.Totals {
		if got.Totals[i].Currency == remoteCurrency {
			eur = &got.Totals[i]
		}
	}
	if eur == nil || eur.Clusters != 2 || eur.MonthlyCost != 5000 || eur.SchedulesSaving != 1000 {
		t.Errorf("EUR totals = %+v (%+v)", eur, got.Totals)
	}

	// The same through the assistant.
	for _, tool := range server.toolRegistry() {
		if tool.Name == "get_clusters" {
			if out, err := tool.Run(ctx, nil); err != nil || !strings.Contains(out, `"name":"eu"`) {
				t.Errorf("tool = %s, %v", out, err)
			}
		}
	}

}
