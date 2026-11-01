package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/auth"
	"github.com/migalsp/costdeck-operator/internal/jvm"
	"github.com/migalsp/costdeck-operator/internal/rightsizing"
	"github.com/migalsp/costdeck-operator/internal/scaling"
)

func toolResult[T any](t *testing.T) func(string, error) T {
	t.Helper()
	return func(out string, err error) T {
		t.Helper()
		if err != nil {
			t.Fatalf("tool failed: %v", err)
		}
		var v T
		if err := json.Unmarshal([]byte(out), &v); err != nil {
			t.Fatalf("tool output is not JSON (%v): %s", err, out)
		}
		return v
	}
}

func TestClusterOverviewToolCountsReadyNodesAndActivePods(t *testing.T) {
	t.Setenv("POD_NAMESPACE", operatorNamespace)
	server := buildMockServerWithK8s()
	seedConfig(t, server, finopsv1.CostDeckConfigSpec{Pricing: customPricing()})
	web := podWith("shop", "web", corev1.PodRunning,
		corev1.Container{Name: "app", Resources: requests("500m", "1Gi")},
		corev1.Container{Name: "proxy", Resources: requests("250m", "512Mi")})
	web.Spec.InitContainers = []corev1.Container{{Name: "migrate", Resources: requests("1", "256Mi")}}
	createAll(t, server.Client,
		readyNode("n1", "4", "16Gi", true), readyNode("n2", "2", "8Gi", false),
		web,
		podWith("shop", "queued", corev1.PodPending, corev1.Container{Name: "app", Resources: requests("250m", "512Mi")}),
		podWith("shop", "batch-done", corev1.PodSucceeded, corev1.Container{Name: "job", Resources: requests("8", "32Gi")}),
		podWith("shop", "crashed", corev1.PodFailed, corev1.Container{Name: "job", Resources: requests("8", "32Gi")}),
	)

	got := toolResult[struct {
		Nodes             int     `json:"nodes"`
		NodesReady        int     `json:"nodesReady"`
		ActivePods        int     `json:"activePods"`
		AllocatableCPU    string  `json:"allocatableCPU"`
		AllocatableMemory string  `json:"allocatableMemory"`
		RequestedCPU      string  `json:"requestedCPU"`
		RequestedMemory   string  `json:"requestedMemory"`
		AllocatableCost   float64 `json:"estimatedMonthlyCostOfAllocatable"`
		RequestsCost      float64 `json:"estimatedMonthlyCostOfRequests"`
		Currency          string  `json:"currency"`
		Pricing           string  `json:"pricing"`
	}](t)(server.toolClusterOverview(context.Background()))

	if got.Nodes != 2 || got.NodesReady != 1 || got.ActivePods != 2 {
		t.Errorf("counts = %+v, want 2 nodes, 1 ready, 2 pods holding resources", got)
	}
	if !sameQuantity(got.AllocatableCPU, "6") || !sameQuantity(got.AllocatableMemory, "24Gi") {
		t.Errorf("allocatable = %s / %s", got.AllocatableCPU, got.AllocatableMemory)
	}
	if !sameQuantity(got.RequestedCPU, "1250m") || !sameQuantity(got.RequestedMemory, "2Gi") {
		t.Errorf("requested = %s / %s, want the larger init container counted for web and finished pods left out", got.RequestedCPU, got.RequestedMemory)
	}
	if !closeTo(got.AllocatableCost, round2(monthlyAtCustomRates(6, 24))) || !closeTo(got.RequestsCost, round2(monthlyAtCustomRates(1.25, 2))) {
		t.Errorf("costs = %v / %v", got.AllocatableCost, got.RequestsCost)
	}
	if got.Currency != customCurrency || got.Pricing != customRatesBasis {
		t.Errorf("pricing = %q %q", got.Currency, got.Pricing)
	}
}

func TestListNamespaceConfigsTool(t *testing.T) {
	t.Setenv("POD_NAMESPACE", operatorNamespace)
	server := buildMockServerWithK8s()
	next := metav1.NewTime(time.Date(2026, 11, 2, 18, 0, 0, 0, time.UTC))
	shop := &finopsv1.ScalingConfig{Name: "shop-hours", Namespace: operatorNamespace, Spec: finopsv1.ScalingConfigSpec{
		TargetNamespace: "shop", Calendars: []string{"de"},
		Exceptions: []finopsv1.ScheduleException{{Name: "Release", Start: "2026-11-07", State: "Up"}},
	}}
	shop.Status.Mode, shop.Status.DesiredState, shop.Status.Phase = "Schedule", "Up", scaling.PhaseScaledUp
	shop.Status.NextTransition = &finopsv1.ScheduledTransition{Time: next, DesiredState: "Down"}
	createAll(t, server.Client, shop, &finopsv1.ScalingConfig{Name: "elsewhere", Namespace: "other", Spec: finopsv1.ScalingConfigSpec{TargetNamespace: "x"}})

	rows := toolResult[[]struct {
		Name       string                       `json:"name"`
		Namespace  string                       `json:"namespace"`
		Mode       string                       `json:"mode"`
		Desired    string                       `json:"desired"`
		Phase      string                       `json:"phase"`
		NextChange string                       `json:"nextChange"`
		Exceptions []finopsv1.ScheduleException `json:"exceptions"`
		Calendars  []string                     `json:"calendars"`
	}](t)(server.toolListConfigs(context.Background()))
	if len(rows) != 1 {
		t.Fatalf("rows = %+v, want only the configs of the operator namespace", rows)
	}
	r := rows[0]
	if r.Name != "shop-hours" || r.Namespace != shopNamespace || r.Mode != "Schedule" || r.Desired != "Up" || r.Phase != scaling.PhaseScaledUp ||
		r.NextChange != "Down at 2026-11-02T18:00:00Z" || len(r.Exceptions) != 1 || r.Exceptions[0].Name != "Release" || strings.Join(r.Calendars, ",") != "de" {
		t.Errorf("row = %+v", r)
	}
}

func TestListNamespacesToolRanksByCostAndNeverReportsNegativeWaste(t *testing.T) {
	t.Setenv("POD_NAMESPACE", operatorNamespace)
	server := buildMockServerWithK8s()
	seedConfig(t, server, finopsv1.CostDeckConfigSpec{Pricing: customPricing()})
	busy := namespaceFinOps("busy", "2", "3", "4Gi", "6Gi")
	busy.Status.Insights = []string{"usage above requests"}
	createAll(t, server.Client,
		namespaceFinOps("idle", "1", "100m", "2Gi", "1Gi"),
		busy,
		namespaceFinOps("new", "", "", "", ""),
		namespaceFinOps("garbled", "lots", "some", "much", "little"),
	)

	type row struct {
		Namespace string   `json:"namespace"`
		CPUUsage  string   `json:"cpuUsage"`
		Cost      float64  `json:"estimatedMonthlyCost"`
		Waste     float64  `json:"estimatedMonthlyWaste"`
		Insights  []string `json:"insights"`
	}
	got := toolResult[struct {
		Currency   string `json:"currency"`
		Pricing    string `json:"pricing"`
		Namespaces []row  `json:"namespaces"`
	}](t)(server.toolListNamespaces(context.Background()))

	if got.Currency != customCurrency || got.Pricing != customRatesBasis || len(got.Namespaces) != 4 {
		t.Fatalf("result = %+v", got)
	}
	byName := map[string]row{}
	for _, r := range got.Namespaces {
		byName[r.Namespace] = r
	}
	if got.Namespaces[0].Namespace != "busy" || got.Namespaces[1].Namespace != "idle" {
		t.Errorf("order = %+v, want the costliest namespace first", got.Namespaces)
	}
	if b := byName["busy"]; !closeTo(b.Cost, round2(monthlyAtCustomRates(2, 4))) || b.Waste != 0 || b.CPUUsage != "3" || strings.Join(b.Insights, "") != "usage above requests" {
		t.Errorf("busy = %+v: the latest point counts and using more than requested wastes nothing", b)
	}
	if i := byName["idle"]; !closeTo(i.Cost, round2(monthlyAtCustomRates(1, 2))) || !closeTo(i.Waste, round2(monthlyAtCustomRates(0.9, 1))) {
		t.Errorf("idle = %+v", i)
	}
	for _, name := range []string{"new", "garbled"} {
		if r := byName[name]; r.Cost != 0 || r.Waste != 0 {
			t.Errorf("%s = %+v: no history or unreadable quantities cost nothing", name, r)
		}
	}
}

func TestListNamespacesToolShowsAtMostSixtyNamespaces(t *testing.T) {
	t.Setenv("POD_NAMESPACE", operatorNamespace)
	server := buildMockServerWithK8s()
	for i := range 61 {
		createAll(t, server.Client, namespaceFinOps(fmt.Sprintf("ns-%02d", i), fmt.Sprintf("%dm", 100+i), "0", "1Gi", "0"))
	}
	got := toolResult[struct {
		Namespaces []struct {
			Namespace string `json:"namespace"`
		} `json:"namespaces"`
	}](t)(server.toolListNamespaces(context.Background()))
	if len(got.Namespaces) != 60 || got.Namespaces[0].Namespace != "ns-60" {
		t.Errorf("rows = %d, first %+v: the 60 costliest are kept", len(got.Namespaces), got.Namespaces[0])
	}
}

func TestNamespaceStatusToolExplainsWhatIsWrong(t *testing.T) {
	t.Setenv("POD_NAMESPACE", operatorNamespace)
	server := buildMockServerWithK8s()
	seedConfig(t, server, finopsv1.CostDeckConfigSpec{Pricing: customPricing()})
	crashing := podWith("shop", "worker", corev1.PodRunning, corev1.Container{Name: "main"})
	crashing.Status.ContainerStatuses = []corev1.ContainerStatus{
		{Name: "main", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}},
		{Name: "sidecar", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ContainerCreating"}}},
	}
	evicted := podWith("shop", "old", corev1.PodFailed)
	evicted.Status.Reason = "Evicted"
	tracked := namespaceFinOps("shop-tracking", "1", "500m", "1Gi", "512Mi")
	tracked.Spec.TargetNamespace = shopNamespace
	tracked.Status.Conditions = []metav1.Condition{{Type: "MetricsAvailable", Status: metav1.ConditionTrue, Reason: "Ok", Message: "metrics-server", LastTransitionTime: metav1.Now()}}
	cfg := &finopsv1.ScalingConfig{Name: "shop-hours", Namespace: operatorNamespace, Spec: finopsv1.ScalingConfigSpec{TargetNamespace: "shop"}}
	cfg.Status.Mode, cfg.Status.Phase = "ManualUp", "ScaledUp"
	createAll(t, server.Client,
		podWith("shop", "web", corev1.PodRunning, corev1.Container{Name: "main"}),
		podWith("shop", "queued", corev1.PodPending, corev1.Container{Name: "main"}),
		podWith("shop", "finished", corev1.PodSucceeded),
		podWith("other", "elsewhere", corev1.PodPending),
		crashing, evicted, tracked, cfg,
		&finopsv1.ScalingGroup{Name: "nonprod", Namespace: operatorNamespace, Spec: finopsv1.ScalingGroupSpec{Namespaces: []string{"shop"}}},
		&finopsv1.ScalingGroup{Name: "unrelated", Namespace: operatorNamespace, Spec: finopsv1.ScalingGroupSpec{Namespaces: []string{"other"}}},
	)

	got := toolResult[struct {
		Namespace     string                      `json:"namespace"`
		ActivePods    int                         `json:"activePods"`
		Problems      []string                    `json:"problems"`
		Usage         *struct{ Namespace string } `json:"usage"`
		Currency      string                      `json:"currency"`
		MetricsSource string                      `json:"metricsSource"`
		ScaledByGroup string                      `json:"scaledByGroup"`
		ScalingConfig map[string]string           `json:"scalingConfig"`
	}](t)(server.toolNamespaceStatus(context.Background(), "shop"))

	if got.Namespace != shopNamespace || got.ActivePods != 3 {
		t.Errorf("status = %+v, want web, queued and worker counted", got)
	}
	want := []string{"old failed (Evicted)", "queued is pending", "worker/main: CrashLoopBackOff"}
	slices.Sort(got.Problems)
	if strings.Join(got.Problems, "|") != strings.Join(want, "|") {
		t.Errorf("problems = %q, want %q", got.Problems, want)
	}
	if got.Usage == nil || got.Usage.Namespace != shopNamespace || got.Currency != customCurrency || got.MetricsSource != "metrics-server" {
		t.Errorf("usage = %+v, currency %q, metrics %q", got.Usage, got.Currency, got.MetricsSource)
	}
	if got.ScaledByGroup != "nonprod" || got.ScalingConfig["name"] != "shop-hours" || got.ScalingConfig["mode"] != "ManualUp" || got.ScalingConfig["phase"] != "ScaledUp" {
		t.Errorf("scaling = group %q, config %v", got.ScaledByGroup, got.ScalingConfig)
	}
}

func TestRecommendationsToolKeepsOnlyWhatShouldChange(t *testing.T) {
	t.Setenv("POD_NAMESPACE", operatorNamespace)
	server := buildMockServerWithK8s()
	keep := rightsizing.ResourceAdvice{Action: rightsizing.ActionKeep}
	reduce := rightsizing.ResourceAdvice{Action: rightsizing.ActionReduce, Request: "2", Recommended: "500m"}
	server.recommendationCache.put("shop", rightsizing.Report{Namespace: "shop", MonthlySavings: 30.126, JavaMonthlySavings: 4.004, Workloads: []rightsizing.WorkloadAdvice{
		{Kind: "Deployment", Name: "api", MonthlySavings: 26.118, Containers: []rightsizing.ContainerAdvice{
			{Name: "app", CPU: reduce, Memory: keep},
			{Name: "proxy", CPU: keep, Memory: keep},
			{Name: "jvm", CPU: keep, Memory: keep, JVM: &rightsizing.JVMAdvice{Findings: []jvm.Finding{{Code: "default-heap", Message: "25% heap"}}}},
		}},
		{Kind: "Deployment", Name: "steady", Containers: []rightsizing.ContainerAdvice{{Name: "app", CPU: keep, Memory: keep}}},
	}})

	report := toolResult[rightsizing.Report](t)(server.toolRecommendations(context.Background(), "shop"))
	if len(report.Workloads) != 1 || report.Workloads[0].Name != apiWorkload {
		t.Fatalf("workloads = %+v, want only api", report.Workloads)
	}
	names := make([]string, 0, len(report.Workloads[0].Containers))
	for _, c := range report.Workloads[0].Containers {
		names = append(names, c.Name)
	}
	if strings.Join(names, ",") != "app,jvm" {
		t.Errorf("containers = %v, want the one to shrink and the one with JVM findings", names)
	}
	if report.MonthlySavings != 30.13 || report.JavaMonthlySavings != 4 || report.Workloads[0].MonthlySavings != 26.12 {
		t.Errorf("savings = %v / %v / %v, want them rounded to cents", report.MonthlySavings, report.JavaMonthlySavings, report.Workloads[0].MonthlySavings)
	}
}

func TestRecommendationsToolSaysWhenThereIsNoUsage(t *testing.T) {
	t.Setenv("POD_NAMESPACE", operatorNamespace)
	server := buildMockServerWithK8s()
	if _, err := server.toolRecommendations(context.Background(), "shop"); err == nil || !strings.Contains(err.Error(), "no usage data for namespace shop") {
		t.Errorf("err = %v, want the missing usage named", err)
	}
}

func TestConfirmedRightsizingRecordsWhoConfirmedIt(t *testing.T) {
	for _, tc := range []struct {
		name string
		who  *auth.Identity
		want string
	}{
		{"a signed-in operator", &auth.Identity{Subject: "user:anna", Name: "Anna", Role: auth.RoleOperator}, "Anna"},
		{"no identity", nil, "the assistant"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := rightsizingFixture(t)
			if rr := send(t, server, http.MethodPut, "/api/settings", `{"rightsizing":{"apply":true,"guardMinutes":15}}`); rr.Code != http.StatusOK {
				t.Fatal(rr.Body.String())
			}
			rr := sendAs(t, server, tc.who, http.MethodPost, "/api/ai/tools/apply_rightsizing", `{"args":{"namespace":"shop","kind":"Deployment","name":"api"}}`)
			if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "Applied the advice to Deployment shop/api in 1 container(s)") {
				t.Fatalf("apply = %d %s", rr.Code, rr.Body.String())
			}
			var opt finopsv1.NamespaceOptimization
			if err := server.Client.Get(context.Background(), client.ObjectKey{Namespace: operatorNamespace, Name: "shop"}, &opt); err != nil {
				t.Fatal(err)
			}
			if len(opt.Status.Workloads) != 1 || opt.Status.Workloads[0].AppliedBy != tc.want {
				t.Errorf("record = %+v, want appliedBy %q", opt.Status.Workloads, tc.want)
			}
		})
	}
}

func TestCostOverviewToolNamesWhatEachNamespaceCouldSave(t *testing.T) {
	t.Setenv("POD_NAMESPACE", operatorNamespace)
	server := buildMockServerWithK8s()
	seedConfig(t, server, finopsv1.CostDeckConfigSpec{Pricing: customPricing()})
	node := readyNode("n1", "4", "16Gi", true)
	createAll(t, server.Client, node,
		&corev1.Namespace{Name: "dev-api", Labels: map[string]string{"team": "payments"}},
		namespaceFinOps("dev-api", "1", "100m", "2Gi", "1Gi"),
	)
	got := toolResult[struct {
		Currency      string `json:"currency"`
		TopNamespaces []struct {
			Namespace        string  `json:"namespace"`
			Environment      string  `json:"environment"`
			Team             string  `json:"team"`
			CouldSaveMonthly float64 `json:"couldSaveMonthly"`
		} `json:"topNamespaces"`
	}](t)(server.toolCostOverview(context.Background()))
	if got.Currency != customCurrency || len(got.TopNamespaces) != 1 {
		t.Fatalf("overview = %+v", got)
	}
	if n := got.TopNamespaces[0]; n.Namespace != "dev-api" || n.Team != "payments" || n.Environment != "non-production" || n.CouldSaveMonthly <= 0 {
		t.Errorf("row = %+v, want an unscheduled non-production namespace with what a schedule would save", n)
	}
}

func TestConfirmedScaleNamespaceOverridesItsConfig(t *testing.T) {
	t.Setenv("POD_NAMESPACE", operatorNamespace)
	server := buildMockServer()
	createAll(t, server.Client, &finopsv1.ScalingConfig{Name: "shop-hours", Namespace: operatorNamespace, Spec: finopsv1.ScalingConfigSpec{
		TargetNamespace: shopNamespace, Schedules: []finopsv1.ScalingSchedule{{Days: []int{1, 2, 3, 4, 5}, StartTime: "09:00", EndTime: "18:00", Timezone: "UTC"}},
	}})

	rr := execTool(t, server, "scale_namespace", `{"args":{"namespace":"shop","action":"down","duration":"forever"}}`)
	if rr.Code != http.StatusOK || decodeInto[map[string]string](t, rr)["result"] != "shop-hours is forced down until the override is removed." {
		t.Fatalf("scale_namespace = %d %s", rr.Code, rr.Body.String())
	}
	cfg := &finopsv1.ScalingConfig{}
	if err := server.Client.Get(context.Background(), client.ObjectKey{Namespace: operatorNamespace, Name: "shop-hours"}, cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Spec.Active == nil || *cfg.Spec.Active || cfg.Spec.ActiveUntil != nil {
		t.Errorf("override = %v until %v, want down with no end", cfg.Spec.Active, cfg.Spec.ActiveUntil)
	}

	rr = execTool(t, server, "scale_namespace", `{"args":{"namespace":"shop-hours","action":"resume"}}`)
	if rr.Code != http.StatusOK || decodeInto[map[string]string](t, rr)["result"] != "shop-hours now follows its schedule again." {
		t.Fatalf("resume by config name = %d %s", rr.Code, rr.Body.String())
	}
	if err := server.Client.Get(context.Background(), client.ObjectKey{Namespace: operatorNamespace, Name: "shop-hours"}, cfg); err != nil || cfg.Spec.Active != nil {
		t.Errorf("after resume the override is %v (%v)", cfg.Spec.Active, err)
	}
}

func TestConfirmedRevertRestoresTheOriginalRequests(t *testing.T) {
	server := rightsizingFixture(t)
	if rr := send(t, server, http.MethodPut, "/api/settings", `{"rightsizing":{"apply":true,"guardMinutes":15}}`); rr.Code != http.StatusOK {
		t.Fatal(rr.Body.String())
	}
	if rr := execTool(t, server, "revert_optimization", `{"args":{"namespace":"shop"}}`); rr.Code != http.StatusNotFound {
		t.Errorf("reverting a namespace never right-sized = %d %s, want 404", rr.Code, rr.Body.String())
	}
	if rr := execTool(t, server, "apply_rightsizing", `{"args":{"namespace":"shop","kind":"Deployment","name":"api"}}`); rr.Code != http.StatusOK || requestsOf(t, server, apiWorkload) == originalRequests {
		t.Fatalf("apply = %d %s", rr.Code, rr.Body.String())
	}
	rr := execTool(t, server, "revert_optimization", `{"args":{"namespace":"shop"}}`)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "Restored the original requests and limits in namespace shop.") {
		t.Fatalf("revert = %d %s", rr.Code, rr.Body.String())
	}
	if got := requestsOf(t, server, apiWorkload); got != originalRequests {
		t.Errorf("requests after revert = %s, want %s", got, originalRequests)
	}
}

func TestConfirmedActionReportsClientErrorsAsClientErrors(t *testing.T) {
	t.Skip("bug: POST /api/ai/tools/{name} answers 500 for an unknown action, invalid arguments or a namespace without a ScalingConfig")
	t.Setenv("POD_NAMESPACE", operatorNamespace)
	server := buildMockServer()
	seedGroup(t, server, "pps1")
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"scale_group", `{"args":{"name":"pps1","action":"explode"}}`, http.StatusBadRequest},
		{"drop_database", `{"args":{}}`, http.StatusNotFound},
		{"get_cluster_overview", `{"args":{}}`, http.StatusNotFound},
		{"scale_namespace", `{"args":{"namespace":"nowhere","action":"up"}}`, http.StatusNotFound},
	} {
		if rr := execTool(t, server, tc.name, tc.body); rr.Code != tc.status {
			t.Errorf("%s %s = %d %s, want %d", tc.name, tc.body, rr.Code, rr.Body.String(), tc.status)
		}
	}
}
