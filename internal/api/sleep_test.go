package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/meta/testrestmapper"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes/fake"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	fakeclient "sigs.k8s.io/controller-runtime/pkg/client/fake"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/scaling"
)

func TestSleepPagesAndWakeStatus(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "costdeck")
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(finopsv1.AddToScheme(scheme))
	routeGVK := schema.GroupVersionKind{Group: "gateway.networking.k8s.io", Version: "v1", Kind: "HTTPRoute"}
	crds := meta.NewDefaultRESTMapper(nil)
	crds.Add(routeGVK, meta.RESTScopeNamespace)
	c := fakeclient.NewClientBuilder().WithScheme(scheme).WithRESTMapper(meta.MultiRESTMapper{testrestmapper.TestOnlyStaticRESTMapper(scheme), crds}).Build()
	server := &Server{Client: c, K8sClient: fake.NewClientset()}
	ctx := context.Background()

	route := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{"hostnames": []any{"shop-dev.example.com"}}}}
	route.SetGroupVersionKind(routeGVK)
	route.SetNamespace("shop-dev")
	route.SetName("shop")
	route.SetAnnotations(map[string]string{scaling.SleepingAnnotation: "[]"})
	next := metav1.NewTime(time.Date(2026, 10, 26, 7, 0, 0, 0, time.UTC))
	objs := []client.Object{
		route,
		&finopsv1.CostDeckConfig{Name: "default", Namespace: "costdeck", Spec: finopsv1.CostDeckConfigSpec{DashboardURL: "https://costdeck.example.com"}},
		&finopsv1.ScalingGroup{Name: "shop", Namespace: "costdeck", Spec: finopsv1.ScalingGroupSpec{Namespaces: []string{"shop-dev"}, SleepPage: true},
			Status: finopsv1.ScalingGroupStatus{Phase: "ScaledDown", ScheduleStatus: finopsv1.ScheduleStatus{DesiredState: "Down", Mode: "Schedule",
				NextTransition: &finopsv1.ScheduledTransition{Time: next, DesiredState: "Up"}}}},
	}
	for _, o := range objs {
		if err := c.Create(ctx, o); err != nil {
			t.Fatal(err)
		}
	}
	h := server.SleepHandler()
	visit := func(ns, accept string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "http://shop-dev.example.com/cart", nil)
		req.Header.Set(scaling.SleepingHeader, ns)
		req.Header.Set("Accept", accept)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		return rr
	}
	rr := visit("shop-dev", "text/html")
	body := rr.Body.String()
	if rr.Code != http.StatusServiceUnavailable || !strings.Contains(body, "shop-dev is asleep") || !strings.Contains(body, "<b>shop</b>") ||
		!strings.Contains(body, "until Mon 26 Oct, 07:00 UTC") || !strings.Contains(body, `href="https://costdeck.example.com/wake/shop-dev?return=`) {
		t.Errorf("sleep page = %d:\n%s", rr.Code, body)
	}
	// A namespace Cost Deck did not put to sleep gets no details.
	if rr := visit("billing", "text/html"); rr.Code != http.StatusServiceUnavailable || strings.Contains(rr.Body.String(), "billing") || !strings.Contains(rr.Body.String(), "This application is asleep") {
		t.Errorf("unknown namespace = %d %s", rr.Code, rr.Body.String())
	}

	rr = send(t, server, "GET", "/api/wake/shop-dev", "")
	var got wakeStatusResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &got)
	if rr.Code != http.StatusOK || got.Schedule == nil || got.Schedule.Kind != "group" || got.Schedule.Name != "shop" || !got.Sleeping || got.Phase != "ScaledDown" ||
		strings.Join(got.Hostnames, ",") != "shop-dev.example.com" {
		t.Errorf("wake status = %d %+v", rr.Code, got)
	}

	if rr := send(t, server, "PUT", "/api/settings", `{"dashboardUrl":"ftp://nope"}`); rr.Code != http.StatusBadRequest {
		t.Errorf("a non-http dashboard URL: %d", rr.Code)
	}
	if rr := send(t, server, "PUT", "/api/settings", `{"dashboardUrl":"https://costdeck.example.org/"}`); rr.Code != http.StatusOK {
		t.Errorf("dashboard URL: %d %s", rr.Code, rr.Body.String())
	}
	var settings SettingsResponse
	_ = json.Unmarshal(send(t, server, "GET", "/api/settings", "").Body.Bytes(), &settings)
	if settings.DashboardURL != "https://costdeck.example.org" {
		t.Errorf("dashboard URL = %q", settings.DashboardURL)
	}
}

func TestTheSleepPageListenerServesNothingElse(t *testing.T) {
	t.Setenv("POD_NAMESPACE", operatorNamespace)
	server := buildMockServerWithK8s()
	server.UI = testUI
	sleep := server.SleepHandler()
	for _, path := range []string{"/api/settings", "/api/tokens", "/mcp", "/", "/settings"} {
		req := httptest.NewRequest(http.MethodGet, "http://shop-dev.example.com"+path, nil)
		req.Header.Set("Accept", "text/html")
		rr := httptest.NewRecorder()
		sleep.ServeHTTP(rr, req)
		if rr.Code != http.StatusServiceUnavailable || !strings.Contains(rr.Body.String(), "This application is asleep") {
			t.Errorf("GET %s through the sleep page Service = %d; a route there must reach nothing but the page", path, rr.Code)
		}
	}

	main, err := server.Handler()
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/version", nil)
	req.Header.Set(scaling.SleepingHeader, "shop-dev")
	rr := httptest.NewRecorder()
	main.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK || strings.Contains(rr.Body.String(), "asleep") {
		t.Errorf("the API with the sleeping header = %d %s; the API listener no longer answers for sleeping routes", rr.Code, rr.Body.String())
	}
}

func TestSleepingNamespacesAreListedAtMostEveryTenSeconds(t *testing.T) {
	var cache sleepingNamespaces
	lists := 0
	load := func(context.Context) ([]string, error) {
		lists++
		return []string{"shop-dev"}, nil
	}
	now := time.Now()
	for i := range 100 {
		if got := cache.get(context.Background(), load, now.Add(time.Duration(i)*50*time.Millisecond)); len(got) != 1 {
			t.Fatalf("sleeping = %v", got)
		}
	}
	if lists != 1 {
		t.Errorf("HTTPRoutes listed %d times for 100 page views in 5 s, want once", lists)
	}
	cache.get(context.Background(), load, now.Add(sleepingRefresh))
	if lists != 2 {
		t.Errorf("listed %d times in total once 10 s passed, want 2", lists)
	}
	failing := func(context.Context) ([]string, error) { return nil, errors.New("apiserver down") }
	if got := cache.get(context.Background(), failing, now.Add(3*sleepingRefresh)); len(got) != 1 {
		t.Errorf("a failed refresh dropped what was known: %v", got)
	}
}

func TestEveryAnswerAgreesWithTheOperatorOnWhoseNamespaceItIs(t *testing.T) {
	t.Setenv("POD_NAMESPACE", operatorNamespace)
	server := buildMockServerWithK8s()
	older := metav1.NewTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	newer := metav1.NewTime(time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC))
	always := []finopsv1.ScalingSchedule{{Days: []int{0, 1, 2, 3, 4, 5, 6}, StartTime: "00:00", EndTime: "23:59", Timezone: "UTC"}}
	createAll(t, server.Client,
		&finopsv1.ScalingGroup{Name: "by-label", Namespace: operatorNamespace, CreationTimestamp: older,
			Spec:   finopsv1.ScalingGroupSpec{NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"env": "dev"}}},
			Status: finopsv1.ScalingGroupStatus{SelectedNamespaces: []string{"ns-x"}}},
		&finopsv1.ScalingGroup{Name: "by-name", Namespace: operatorNamespace, CreationTimestamp: newer,
			Spec: finopsv1.ScalingGroupSpec{Namespaces: []string{"ns-x"}, Schedules: always}},
	)
	ctx := context.Background()
	if _, _, name := server.scheduleOf(ctx, "ns-x"); name != "by-name" {
		t.Errorf("the wake page and sleep page name %q, the operator scales ns-x with by-name: listing it by name wins", name)
	}
	if owner, _, _, _ := server.scheduleOwner(ctx, "ns-x"); owner == nil || owner.GetName() != "by-name" {
		t.Errorf("wake-up risks read %v", owner)
	}
	shares, err := server.scheduleShares(ctx, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	if err != nil || !strings.Contains(shares["ns-x"].note, "by-name") {
		t.Errorf("the estimate prices ns-x with %+v, %v", shares["ns-x"], err)
	}
}
