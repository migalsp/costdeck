package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	metricsv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"
	metricsfake "k8s.io/metrics/pkg/client/clientset/versioned/fake"
	"sigs.k8s.io/controller-runtime/pkg/client"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/finops"
)

func TestFinOpsOverview(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "costdeck")
	server := buildMockServerWithK8s()
	ctx := context.Background()
	node := &corev1.Node{Name: "n1"}
	node.Status.Capacity = corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4"), corev1.ResourceMemory: resource.MustParse("16Gi")}
	for _, o := range []client.Object{
		node,
		&corev1.Namespace{Name: "dev-api"},
		&finopsv1.NamespaceFinOps{
			Name: "dev-api", Namespace: "costdeck",
			Spec: finopsv1.NamespaceFinOpsSpec{TargetNamespace: "dev-api"},
			Status: finopsv1.NamespaceFinOpsStatus{History: []finopsv1.MetricDataPoint{{
				CPU:    finopsv1.ResourceMetrics{Requests: "1", Usage: "100m"},
				Memory: finopsv1.ResourceMetrics{Requests: "2Gi", Usage: "1Gi"},
			}}},
		},
	} {
		if err := server.Client.Create(ctx, o); err != nil {
			t.Fatal(err)
		}
	}

	rr := httptest.NewRecorder()
	server.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/finops/overview", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	var o finops.Overview
	if err := json.Unmarshal(rr.Body.Bytes(), &o); err != nil {
		t.Fatal(err)
	}
	if len(o.Namespaces) != 1 || o.Namespaces[0].Environment != finops.EnvNonProduction || o.Monthly.Provisioned <= o.Monthly.Requested {
		t.Errorf("unexpected overview: %+v", o)
	}
	// dev-api is unscheduled non-production, so it is a schedule candidate.
	if o.Savings.ScheduleCandidatesMonthly <= 0 || o.Namespaces[0].ScheduleSavingMonthly <= 0 {
		t.Errorf("dev-api should be a schedule candidate: %+v", o.Savings)
	}
	if o.Savings.RightsizingPending != 1 {
		t.Errorf("advice for dev-api is computed in the background, pending %d", o.Savings.RightsizingPending)
	}
}

func TestCostOverviewTool(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "costdeck")
	server := buildMockServerWithK8s()
	node := &corev1.Node{Name: "n1"}
	node.Status.Capacity = corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2"), corev1.ResourceMemory: resource.MustParse("8Gi")}
	if err := server.Client.Create(context.Background(), node); err != nil {
		t.Fatal(err)
	}
	out, err := server.toolCostOverview(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not JSON: %s", out)
	}
	for _, key := range []string{"monthlyRunRate", "costEfficiency", "monthToDate", "savings", "topNamespaces", "opportunities"} {
		if _, ok := got[key]; !ok {
			t.Errorf("missing %s in %s", key, out)
		}
	}
}

func daemonSetPod(ns, name, node string, resources corev1.ResourceRequirements) *corev1.Pod {
	p := podWith(ns, name, corev1.PodRunning, corev1.Container{Name: "agent", Resources: resources})
	p.Spec.NodeName = node
	p.OwnerReferences = []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "DaemonSet", Name: "agent", UID: "uid-agent"}}
	return p
}

func TestFinOpsNodesPricesNonProductionComputeForSpot(t *testing.T) {
	t.Setenv("POD_NAMESPACE", operatorNamespace)
	server := buildMockServerWithK8s()
	seedConfig(t, server, finopsv1.CostDeckConfigSpec{Pricing: customPricing()})
	unscheduled := podWith("dev-shop", "waiting", corev1.PodPending, corev1.Container{Name: "app", Resources: requests("4", "8Gi")})
	unscheduled.Spec.NodeName = ""
	createAll(t, server.Client,
		readyNode("n1", "8", "32Gi", true),
		&corev1.Namespace{Name: "dev-shop"}, &corev1.Namespace{Name: "prod-shop"},
		podWith("dev-shop", "web", corev1.PodRunning, corev1.Container{Name: "app", Resources: requests("1", "2Gi")}),
		podWith("dev-shop", "finished", corev1.PodSucceeded, corev1.Container{Name: "job", Resources: requests("2", "2Gi")}),
		daemonSetPod("dev-shop", "agent-x", "n1", requests("500m", "512Mi")),
		unscheduled,
		podWith("prod-shop", "web", corev1.PodRunning, corev1.Container{Name: "app", Resources: requests("2", "4Gi")}),
	)

	rr := send(t, server, http.MethodGet, "/api/finops/nodes", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("nodes = %d %s", rr.Code, rr.Body.String())
	}
	got := decodeInto[struct {
		K8sVersion string             `json:"k8sVersion"`
		Cloud      string             `json:"cloud"`
		Report     finops.NodesReport `json:"report"`
	}](t, rr)
	if got.K8sVersion != kubernetesVersion || got.Cloud != "local" || got.Report.Currency != customCurrency || got.Report.Summary.Nodes != 1 {
		t.Errorf("nodes view = %+v", got)
	}
	if got.Report.Spot == nil || !closeTo(got.Report.Spot.NonProductionMonthly, monthlyAtCustomRates(1, 2)) {
		t.Errorf("spot advice = %+v, want only dev-shop/web: DaemonSets, finished, unscheduled and production pods do not move to spot", got.Report.Spot)
	}
}

func TestFinOpsNodePodsListsTheLivePodsOfOneNode(t *testing.T) {
	t.Setenv("POD_NAMESPACE", operatorNamespace)
	server := buildMockServerWithK8s()
	seedConfig(t, server, finopsv1.CostDeckConfigSpec{Pricing: customPricing()})
	elsewhere := podWith("shop", "elsewhere", corev1.PodRunning, corev1.Container{Name: "app", Resources: requests("8", "8Gi")})
	elsewhere.Spec.NodeName = "n2"
	createAll(t, server.Client,
		podWith("shop", "small", corev1.PodRunning, corev1.Container{Name: "app", Resources: requests("250m", "512Mi")}),
		podWith("shop", "big", corev1.PodRunning, corev1.Container{Name: "app", Resources: requests("2", "4Gi")}),
		podWith("shop", "done", corev1.PodSucceeded, corev1.Container{Name: "job", Resources: requests("4", "4Gi")}),
		daemonSetPod("kube-system", "agent-n1", "n1", requests("100m", "128Mi")),
		elsewhere,
	)
	metricsClient := metricsfake.NewSimpleClientset() //nolint:staticcheck
	pm := &metricsv1beta1.PodMetrics{Name: "big", Namespace: "shop", Containers: []metricsv1beta1.ContainerMetrics{{Name: "app",
		Usage: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m"), corev1.ResourceMemory: resource.MustParse("1Gi")}}}}
	if err := metricsClient.Tracker().Create(metricsv1beta1.SchemeGroupVersion.WithResource("pods"), pm, "shop"); err != nil {
		t.Fatal(err)
	}
	server.MetricsClient = metricsClient

	rr := send(t, server, http.MethodGet, "/api/finops/nodes/n1/pods", "")
	pods := decodeInto[[]NodePod](t, rr)
	if rr.Code != http.StatusOK || len(pods) != 3 {
		t.Fatalf("pods = %d %+v, want the three live pods of n1", rr.Code, pods)
	}
	if pods[0].Name != "big" || pods[1].Name != "small" || pods[2].Name != "agent-n1" {
		t.Errorf("order = %s, %s, %s, want the costliest first", pods[0].Name, pods[1].Name, pods[2].Name)
	}
	big := pods[0]
	if big.CPURequest != 2 || big.MemoryRequest != 4 || !closeTo(big.MonthlyCost, monthlyAtCustomRates(2, 4)) || big.CPUUsed != 0.5 || big.MemoryUsed != 1 || big.Phase != "Running" {
		t.Errorf("big = %+v", big)
	}
	if !pods[2].DaemonSet || pods[1].DaemonSet || pods[1].CPUUsed != 0 {
		t.Errorf("small = %+v, agent = %+v: only the agent is a DaemonSet pod, and a pod without a reading shows no usage", pods[1], pods[2])
	}
	if body := send(t, server, http.MethodGet, "/api/finops/nodes/ghost/pods", "").Body.String(); strings.TrimSpace(body) != "[]" {
		t.Errorf("an unknown node = %s, want an empty list the UI can map", body)
	}
}

func TestFinOpsInfraListsVolumesAndLoadBalancers(t *testing.T) {
	t.Setenv("POD_NAMESPACE", operatorNamespace)
	server := buildMockServerWithK8s()
	rates := customPricing()
	rates.StorageGiBMonth, rates.LoadBalancerMonth = "0.1", "18"
	seedConfig(t, server, finopsv1.CostDeckConfigSpec{Pricing: rates})
	class := "standard"
	claim := &corev1.PersistentVolumeClaim{Name: "data", Namespace: "shop", Spec: corev1.PersistentVolumeClaimSpec{StorageClassName: &class,
		Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("10Gi")}}},
		Status: corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound, Capacity: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("10Gi")}}}
	db := podWith("shop", "db", corev1.PodRunning, corev1.Container{Name: "db"})
	db.Spec.Volumes = []corev1.Volume{{Name: "data", PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "data"}}}
	ready := true
	createAll(t, server.Client, readyNode("n1", "4", "16Gi", true), claim, db,
		&corev1.Service{Name: "shop-lb", Namespace: "shop", Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer, Ports: []corev1.ServicePort{{Port: 443}}}},
		&corev1.Service{Name: "internal", Namespace: "shop", Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP}},
		&discoveryv1.EndpointSlice{Name: "shop-lb-abc", Namespace: "shop", Labels: map[string]string{discoveryv1.LabelServiceName: "shop-lb"},
			AddressType: discoveryv1.AddressTypeIPv4, Endpoints: []discoveryv1.Endpoint{{Addresses: []string{"10.0.0.7"}, Conditions: discoveryv1.EndpointConditions{Ready: &ready}}}},
	)

	rr := send(t, server, http.MethodGet, "/api/finops/infra", "")
	got := decodeInto[struct {
		Currency string       `json:"currency"`
		Cloud    string       `json:"cloud"`
		Infra    finops.Infra `json:"infra"`
		Error    *string      `json:"error"`
	}](t, rr)
	if rr.Code != http.StatusOK || got.Currency != customCurrency || got.Cloud != "local" || got.Error != nil && *got.Error != "" {
		t.Fatalf("infra = %d %+v", rr.Code, got)
	}
	if len(got.Infra.Volumes) != 1 || got.Infra.Volumes[0].Claim != "data" || !closeTo(got.Infra.Volumes[0].MonthlyCost, 1) ||
		strings.Join(got.Infra.Volumes[0].MountedBy, ",") != "db" || got.Infra.Volumes[0].State != finops.StateInUse {
		t.Errorf("volumes = %+v", got.Infra.Volumes)
	}
	if len(got.Infra.LoadBalancers) != 1 || got.Infra.LoadBalancers[0].Name != "shop-lb" || got.Infra.LoadBalancers[0].MonthlyCost != 18 ||
		got.Infra.LoadBalancers[0].ReadyEndpoints != 1 || got.Infra.LoadBalancers[0].State != finops.StateServing || strings.Join(got.Infra.LoadBalancers[0].Ports, ",") != "TCP/443" {
		t.Errorf("load balancers = %+v", got.Infra.LoadBalancers)
	}
	if !closeTo(got.Infra.StorageMonthly, 1) || got.Infra.NetworkMonthly != 18 {
		t.Errorf("totals = %v storage, %v network", got.Infra.StorageMonthly, got.Infra.NetworkMonthly)
	}
}

func TestBudgetsShowEveryBudgetWithItsScopes(t *testing.T) {
	t.Setenv("POD_NAMESPACE", operatorNamespace)
	server := buildMockServerWithK8s()
	seedConfig(t, server, finopsv1.CostDeckConfigSpec{
		Pricing:      customPricing(),
		Budgets:      []finopsv1.Budget{{Name: "cluster", Scope: finops.ScopeCluster, MonthlyLimit: "1000", Thresholds: []int{80, 100}}},
		Alerts:       finopsv1.AlertsConfig{Anomalies: finopsv1.AnomalyAlerts{Enabled: true, Percent: 40}, PerformanceRisks: &finopsv1.PerformanceRiskAlerts{Enabled: true}},
		Integrations: finopsv1.IntegrationsConfig{Messenger: &finopsv1.MessengerIntegrationConfig{Webex: &finopsv1.WebexConfig{Enabled: true, RoomID: "room-1"}}},
	})
	createAll(t, server.Client, readyNode("n1", "4", "16Gi", true),
		&corev1.Namespace{Name: "shop", Labels: map[string]string{"team": "payments"}},
		&corev1.Namespace{Name: "search", Labels: map[string]string{"team": "discovery"}},
		&corev1.Namespace{Name: "billing", Labels: map[string]string{"team": "payments"}},
		namespaceFinOps("shop", "1", "1", "1Gi", "1Gi"), namespaceFinOps("search", "1", "1", "1Gi", "1Gi"), namespaceFinOps("billing", "1", "1", "1Gi", "1Gi"),
	)

	rr := send(t, server, http.MethodGet, "/api/budgets", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("budgets = %d %s", rr.Code, rr.Body.String())
	}
	got := decodeInto[struct {
		Currency         string                 `json:"currency"`
		Budgets          []finops.BudgetStatus  `json:"budgets"`
		Anomalies        finopsv1.AnomalyAlerts `json:"anomalies"`
		PerformanceRisks map[string]bool        `json:"performanceRisks"`
		Events           []json.RawMessage      `json:"events"`
		WebexReady       bool                   `json:"webexReady"`
		Channels         []string               `json:"channels"`
		Scopes           struct {
			Namespaces []string `json:"namespaces"`
			Teams      []string `json:"teams"`
		} `json:"scopes"`
	}](t, rr)
	if got.Currency != customCurrency || len(got.Budgets) != 1 || got.Budgets[0].Budget.Name != "cluster" || got.Budgets[0].Limit != 1000 {
		t.Errorf("budgets = %+v", got.Budgets)
	}
	if !got.Anomalies.Enabled || got.Anomalies.Percent != 40 || !got.PerformanceRisks["enabled"] || !got.WebexReady || strings.Join(got.Channels, ",") != "Webex" {
		t.Errorf("alert settings = %+v", got)
	}
	if strings.Join(got.Scopes.Teams, ",") != "discovery,payments" || len(got.Scopes.Namespaces) != 3 || got.Events == nil {
		t.Errorf("scopes = %+v, events %v: each team once, sorted, and an empty event list rather than null", got.Scopes, got.Events)
	}
}

func TestBillingReconcileNeedsItTurnedOn(t *testing.T) {
	t.Setenv("POD_NAMESPACE", operatorNamespace)
	server := buildMockServerWithK8s()
	rr := send(t, server, http.MethodPost, "/api/billing/reconcile", "")
	if rr.Code != http.StatusBadRequest || !strings.Contains(errorMessage(t, rr), "billing reconciliation is off") {
		t.Errorf("reconcile while off = %d %s", rr.Code, rr.Body.String())
	}
}

func TestFinOpsOverviewForOtherComponentsMatchesTheDashboard(t *testing.T) {
	t.Setenv("POD_NAMESPACE", operatorNamespace)
	server := buildMockServerWithK8s()
	seedConfig(t, server, finopsv1.CostDeckConfigSpec{Pricing: customPricing()})
	createAll(t, server.Client, readyNode("n1", "4", "16Gi", true), &corev1.Namespace{Name: "dev-api"}, namespaceFinOps("dev-api", "1", "100m", "2Gi", "1Gi"))
	o, err := server.FinOpsOverview(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	dashboard := decodeInto[finops.Overview](t, send(t, server, http.MethodGet, "/api/finops/overview", ""))
	if o.Currency != customCurrency || len(o.Namespaces) != 1 || o.Monthly.Provisioned != dashboard.Monthly.Provisioned || o.Namespaces[0].Name != dashboard.Namespaces[0].Name {
		t.Errorf("FinOpsOverview = %+v, dashboard = %+v", o.Monthly, dashboard.Monthly)
	}
}
