package api

import (
	"context"
	"encoding/json"
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
	h, err := server.Handler()
	if err != nil {
		t.Fatal(err)
	}
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
