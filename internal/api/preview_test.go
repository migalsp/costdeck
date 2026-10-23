package api

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/pricing"
)

func seedWorkload(t *testing.T, server *Server, ns, name string, replicas int32, cpu, mem string) {
	t.Helper()
	labels := map[string]string{"app": name}
	d := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas, Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}, Spec: corev1.PodSpec{Containers: []corev1.Container{{
				Name: "app", Image: "x",
				Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(cpu), corev1.ResourceMemory: resource.MustParse(mem)}},
			}}}},
		},
	}
	if err := server.Client.Create(context.Background(), d); err != nil {
		t.Fatal(err)
	}
}

func TestPreviewSchedule(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "costdeck")
	server := buildMockServerWithK8s()
	ctx := context.Background()
	// Custom rates: 0.04 per core-hour, 0.005 per GiB-hour.
	if err := server.Client.Create(ctx, &finopsv1.CostDeckConfig{ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: "costdeck"},
		Spec: finopsv1.CostDeckConfigSpec{Pricing: finopsv1.PricingConfig{CPUCoreHour: "0.04", MemoryGBHour: "0.005", Currency: "EUR"}}}); err != nil {
		t.Fatal(err)
	}
	server.Pricing = &pricing.Resolver{Client: server.Client}
	for _, ns := range []string{"shop-dev", "shop-qa", "pay-dev"} {
		if err := server.Client.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns, Labels: map[string]string{"env": "dev"}}}); err != nil {
			t.Fatal(err)
		}
	}
	seedWorkload(t, server, "shop-dev", "api", 2, "500m", "1Gi")
	seedWorkload(t, server, "shop-dev", "redis", 1, "1", "2Gi") // excluded by the workload rules below
	seedWorkload(t, server, "pay-dev", "api", 1, "1", "1Gi")
	if err := server.Client.Create(ctx, &finopsv1.ScalingConfig{ObjectMeta: metav1.ObjectMeta{Name: "shop-dev", Namespace: "costdeck"},
		Spec: finopsv1.ScalingConfigSpec{TargetNamespace: "shop-dev", Exclusions: []string{"redis"}}}); err != nil {
		t.Fatal(err)
	}
	if err := server.Client.Create(ctx, &finopsv1.ScalingGroup{ObjectMeta: metav1.ObjectMeta{Name: "pay", Namespace: "costdeck"},
		Spec: finopsv1.ScalingGroupSpec{Namespaces: []string{"pay-dev"}}}); err != nil {
		t.Fatal(err)
	}

	body := `{"kind":"group","spec":{"namespaces":["shop-dev","ghost"],"namespaceSelector":{"matchLabels":{"env":"dev"}},
		"schedules":[{"days":[1,2,3,4,5],"startTime":"08:00","endTime":"20:00","timezone":"UTC"}]}}`
	rr := send(t, server, "POST", "/api/scaling/preview", body)
	if rr.Code != http.StatusOK {
		t.Fatalf("preview = %d %s", rr.Code, rr.Body.String())
	}
	var got previewResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}

	byName := map[string]namespacePreview{}
	for _, np := range got.Namespaces {
		byName[np.Name] = np
	}
	if np := byName["shop-dev"]; np.Source != "listed" || len(np.Workloads) != 2 || !np.Workloads[1].Excluded || np.Workloads[0].Replicas != 2 {
		t.Errorf("shop-dev = %+v, want api (2 replicas) and an excluded redis", np)
	}
	if np := byName["shop-qa"]; np.Source != "selected" || len(np.Workloads) != 0 {
		t.Errorf("shop-qa = %+v, want selected by label with nothing to scale", np)
	}
	if np := byName["pay-dev"]; np.OwnedBy != "pay" {
		t.Errorf("pay-dev = %+v, want it left to the group pay", np)
	}
	if np := byName["ghost"]; !np.Missing {
		t.Errorf("ghost = %+v, want missing", np)
	}
	for _, w := range []string{"There is no namespace ghost.", "pay-dev is already scaled by pay"} {
		if !strings.Contains(strings.Join(got.Warnings, "\n"), w) {
			t.Errorf("warnings %q lack %q", got.Warnings, w)
		}
	}

	// Only shop-dev's api counts: 2 x (0.5 core, 1 GiB) at the fixed rates.
	wantHourly := 2 * (0.5*0.04 + 1*0.005)
	if math.Abs(got.Savings.Hourly-wantHourly) > 1e-9 || got.Savings.Currency != "EUR" {
		t.Errorf("hourly saving = %v %s, want %v EUR", got.Savings.Hourly, got.Savings.Currency, wantHourly)
	}
	// Weekdays 08:00-20:00 are up about 36% of the time; the rest is saved.
	if got.UpShare < 0.35 || got.UpShare > 0.37 || math.Abs(got.Savings.Monthly-wantHourly*730*(1-got.UpShare)) > 1e-6 {
		t.Errorf("up share %v, monthly %v", got.UpShare, got.Savings.Monthly)
	}
	if len(got.Intervals) < 20 || got.Intervals[0].From.After(time.Now()) || got.NextChange == nil {
		t.Errorf("timetable: %d intervals, next %v", len(got.Intervals), got.NextChange)
	}
}

func TestPreviewWarnsAndReports(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "costdeck")
	server := buildMockServerWithK8s()
	seedCalendars(t, server)
	if err := server.Client.Create(context.Background(), &finopsv1.ScalingGroup{ObjectMeta: metav1.ObjectMeta{Name: "shop", Namespace: "costdeck"},
		Spec: finopsv1.ScalingGroupSpec{Namespaces: []string{"shop-dev"}}}); err != nil {
		t.Fatal(err)
	}
	decode := func(body string) previewResponse {
		rr := send(t, server, "POST", "/api/scaling/preview", body)
		if rr.Code != http.StatusOK {
			t.Fatalf("preview = %d %s", rr.Code, rr.Body.String())
		}
		var got previewResponse
		_ = json.Unmarshal(rr.Body.Bytes(), &got)
		return got
	}

	got := decode(`{"kind":"group","name":"shop","spec":{"namespaces":["shop-dev"],"calendars":["nope"]}}`)
	if len(got.Problems) != 1 || !strings.Contains(got.Problems[0], `calendar "nope" does not exist`) {
		t.Errorf("problems = %q", got.Problems)
	}
	if got.Namespaces[0].OwnedBy != "" || !strings.Contains(strings.Join(got.Warnings, " "), "No hours are set") {
		t.Errorf("editing shop must not conflict with itself, and no hours must warn: %+v %q", got.Namespaces, got.Warnings)
	}
	if got.UpShare != 1 || got.Savings.Monthly != 0 {
		t.Errorf("without hours nothing is saved: %v %v", got.UpShare, got.Savings.Monthly)
	}

	got = decode(`{"kind":"config","spec":{"targetNamespace":"shop-dev","schedules":[{"days":[1],"startTime":"08:00","endTime":"09:00"}]}}`)
	if !strings.Contains(strings.Join(got.Warnings, " "), "belongs to the schedule shop") {
		t.Errorf("a config inside a group must say the group's hours apply: %q", got.Warnings)
	}
	got = decode(`{"kind":"group","spec":{"namespaces":["x"],"activation":"OnDemand"}}`)
	if !got.OnDemand || got.UpShare != 0 {
		t.Errorf("on demand: %+v", got)
	}
	if rr := send(t, server, "POST", "/api/scaling/preview", `{"kind":"other","spec":{}}`); rr.Code != http.StatusBadRequest {
		t.Errorf("unknown kind = %d", rr.Code)
	}
}

func TestPreviewTool(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "costdeck")
	server := buildMockServerWithK8s()
	seedCalendars(t, server)
	ctx := context.Background()
	if err := server.Client.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "shop-dev"}}); err != nil {
		t.Fatal(err)
	}
	seedWorkload(t, server, "shop-dev", "api", 2, "500m", "1Gi")
	if err := server.Client.Create(ctx, &finopsv1.ScalingGroup{ObjectMeta: metav1.ObjectMeta{Name: "shop", Namespace: "costdeck"},
		Spec: finopsv1.ScalingGroupSpec{Namespaces: []string{"shop-dev"}, Schedules: []finopsv1.ScalingSchedule{{Days: []int{1, 2, 3, 4, 5}, StartTime: "08:00", EndTime: "20:00", Timezone: "UTC"}}}}); err != nil {
		t.Fatal(err)
	}
	out, err := server.toolPreview(ctx, "group", "shop")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"namespace":"shop-dev"`, `"workloadsToStop":1`, `"upShare":"36%"`, "(schedule)"} {
		if !strings.Contains(out, want) {
			t.Errorf("preview tool output lacks %s:\n%s", want, out)
		}
	}
	if _, err := server.toolPreview(ctx, "group", "missing"); err == nil {
		t.Error("previewing a missing group did not fail")
	}
}
