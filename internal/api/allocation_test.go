package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/migalsp/costdeck-operator/internal/config"
	"github.com/migalsp/costdeck-operator/internal/finops"
)

func TestAllocationQuery(t *testing.T) {
	now := time.Date(2026, 10, 23, 15, 0, 0, 0, time.UTC)
	for q, want := range map[string]string{
		"":                                 "team 2026-10-01 2026-10-23",
		"period=last-month&by=environment": "environment 2026-09-01 2026-09-30",
		"period=last-7-days&by=namespace":  "namespace 2026-10-17 2026-10-23",
		"period=last-30-days":              "team 2026-09-24 2026-10-23",
		"from=2026-08-01&to=2026-08-31&by=label:cost-center": "label:cost-center 2026-08-01 2026-08-31",
	} {
		v, _ := url.ParseQuery(q)
		by, from, to, err := allocationQuery(v, now)
		if got := by + " " + from + " " + to; err != nil || got != want {
			t.Errorf("%q = %q, %v; want %q", q, got, err, want)
		}
	}
	for _, q := range []string{"by=owner", "by=label:", "by=label:bad key", "period=forever", "from=2026-08-31&to=2026-08-01", "from=yesterday&to=today"} {
		v, _ := url.ParseQuery(q)
		if _, _, _, err := allocationQuery(v, now); err == nil {
			t.Errorf("%q was accepted", q)
		}
	}
}

func TestAllocationEndpoints(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "costdeck")
	server := buildMockServerWithK8s()
	ctx := context.Background()
	today := time.Now().UTC().Format(time.DateOnly)
	l := &finops.Ledger{Currency: "EUR", Days: []finops.Day{{Date: today, Hours: 24, Provisioned: 100, Requested: 70, Storage: 10, Network: 5,
		Namespaces: map[string]finops.NamespaceDay{"shop-dev": {Cost: 30, Storage: 6, Network: 5}, "shop-prod": {Cost: 20, Storage: 4}, "kube-system": {Cost: 10}, "monitoring": {Cost: 10}}}}}
	if err := finops.SaveLedger(ctx, server.Client, "costdeck", l); err != nil {
		t.Fatal(err)
	}
	for name, labels := range map[string]map[string]string{"shop-dev": {"team": "payments", "cost-center": "cc-1"}, "shop-prod": {"team": "payments"}} {
		_ = server.Client.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}})
	}

	for body, why := range map[string]string{
		`{"allocation":{"idle":"sometimes"}}`:           "an unknown idle policy",
		`{"allocation":{"sharedSplit":"fair"}}`:         "an unknown split",
		`{"allocation":{"sharedNamespaces":["[kube"]}}`: "a broken pattern",
	} {
		if rr := send(t, server, "PUT", "/api/settings", body); rr.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", why, rr.Code)
		}
	}
	if rr := send(t, server, "PUT", "/api/settings", `{"allocation":{"idle":"proportional","sharedNamespaces":["kube-*"," monitoring","kube-*"]}}`); rr.Code != http.StatusOK {
		t.Fatalf("valid: %d %s", rr.Code, rr.Body.String())
	}
	cfg, _ := config.Get(ctx, server.Client)
	if a := cfg.Spec.Allocation; a.Idle != "proportional" || strings.Join(a.SharedNamespaces, ",") != "kube-*,monitoring" {
		t.Errorf("saved = %+v", a)
	}

	var report finops.AllocationReport
	rr := send(t, server, "GET", "/api/finops/allocation?by=label:cost-center", "")
	_ = json.Unmarshal(rr.Body.Bytes(), &report)
	if rr.Code != http.StatusOK || report.Total != 115 || len(report.Rows) != 2 || report.Rows[0].Key != "cc-1" || report.Rows[0].Total != 71 || report.Unallocated != 0 {
		t.Fatalf("report: %d %+v", rr.Code, report)
	}

	rr = send(t, server, "GET", "/api/finops/allocation.csv?by=team", "")
	csv := rr.Body.String()
	if rr.Code != http.StatusOK || !strings.HasPrefix(rr.Header().Get("Content-Type"), "text/csv") ||
		!strings.Contains(rr.Header().Get("Content-Disposition"), "cost-allocation-team-") {
		t.Fatalf("csv: %d %v", rr.Code, rr.Header())
	}
	for _, want := range []string{"team,namespaces,compute_EUR,storage_EUR,load_balancers_EUR,shared_EUR,idle_EUR,total_EUR", "payments,shop-dev shop-prod,50.00,10.00,5.00,20.00,30.00,115.00", "(total),,,,,,,115.00"} {
		if !strings.Contains(csv, want) {
			t.Errorf("csv has no %q:\n%s", want, csv)
		}
	}
	if rr := send(t, server, "GET", "/api/finops/allocation?by=owner", ""); rr.Code != http.StatusBadRequest {
		t.Errorf("a bad group: %d", rr.Code)
	}
}

// The assistant and MCP clients read the same report.
func TestAllocationTool(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "costdeck")
	server := buildMockServerWithK8s()
	ctx := context.Background()
	l := &finops.Ledger{Currency: "EUR", Days: []finops.Day{{Date: "2026-09-15", Hours: 24, Provisioned: 10, Namespaces: map[string]finops.NamespaceDay{"shop": {Cost: 10}}}}}
	_ = finops.SaveLedger(ctx, server.Client, "costdeck", l)
	for _, tool := range server.toolRegistry() {
		if tool.Name != "get_cost_allocation" {
			continue
		}
		out, err := tool.Run(ctx, map[string]any{"by": "namespace", "from": "2026-09-01", "to": "2026-09-30"})
		if err != nil || !strings.Contains(out, `"key":"shop"`) || !strings.Contains(out, `"total":10`) {
			t.Fatalf("tool = %s, %v", out, err)
		}
		if _, err := tool.Run(ctx, map[string]any{"by": "owner"}); err == nil {
			t.Error("a bad group was accepted")
		}
		return
	}
	t.Fatal("no get_cost_allocation tool")
}
