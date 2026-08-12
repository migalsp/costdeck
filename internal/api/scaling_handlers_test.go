package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func buildMockServer() *Server {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(finopsv1.AddToScheme(scheme))

	client := fake.NewClientBuilder().WithScheme(scheme).Build()
	return &Server{
		Client: client,
	}
}

func TestHandleScalingGroupsGET(t *testing.T) {
	os.Setenv("POD_NAMESPACE", "costdeck")
	defer os.Unsetenv("POD_NAMESPACE")

	server := buildMockServer()

	// Seed one item
	group := &finopsv1.ScalingGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-group",
			Namespace: "costdeck",
		},
		Spec: finopsv1.ScalingGroupSpec{
			Namespaces: []string{"default"},
		},
	}
	server.Client.Create(context.Background(), group)

	req, err := http.NewRequest("GET", "/api/scaling/groups", nil)
	if err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(server.handleScalingGroups)
	handler.ServeHTTP(rr, req)

	if status := rr.Code; status != http.StatusOK {
		t.Errorf("handler returned wrong status code: got %v want %v", status, http.StatusOK)
	}

	var parsed []finopsv1.ScalingGroup
	if err := json.NewDecoder(rr.Body).Decode(&parsed); err != nil {
		t.Fatal(err)
	}

	if len(parsed) != 1 || parsed[0].Name != "test-group" {
		t.Errorf("handler returned unexpected body: %v", parsed)
	}
}

func TestHandleScalingGroupsPOST(t *testing.T) {
	os.Setenv("POD_NAMESPACE", "costdeck")
	defer os.Unsetenv("POD_NAMESPACE")

	server := buildMockServer()

	body := []byte(`{"metadata":{"name":"new-group"},"spec":{"namespaces":["test"]}}`)
	req, err := http.NewRequest("POST", "/api/scaling/groups", bytes.NewBuffer(body))
	if err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(server.handleScalingGroups)
	handler.ServeHTTP(rr, req)

	if status := rr.Code; status != http.StatusCreated {
		t.Errorf("handler returned wrong status code: got %v want %v", status, http.StatusCreated)
	}

	// Verify it was created in the mock cluster
	list := &finopsv1.ScalingGroupList{}
	server.Client.List(context.Background(), list)
	if len(list.Items) != 1 {
		t.Errorf("Expected 1 group created in cluster, got %d", len(list.Items))
	}
}

func TestHandleScalingConfigsGET(t *testing.T) {
	os.Setenv("POD_NAMESPACE", "costdeck")
	defer os.Unsetenv("POD_NAMESPACE")

	server := buildMockServer()

	config := &finopsv1.ScalingConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-config",
			Namespace: "costdeck",
		},
		Spec: finopsv1.ScalingConfigSpec{
			TargetNamespace: "app-ns",
		},
	}
	server.Client.Create(context.Background(), config)

	req, err := http.NewRequest("GET", "/api/scaling/configs", nil)
	if err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(server.handleScalingConfigs)
	handler.ServeHTTP(rr, req)

	if status := rr.Code; status != http.StatusOK {
		t.Errorf("handler returned wrong status code: got %v want %v", status, http.StatusOK)
	}

	var parsed []finopsv1.ScalingConfig
	if err := json.NewDecoder(rr.Body).Decode(&parsed); err != nil {
		t.Fatal(err)
	}

	if len(parsed) != 1 || parsed[0].Name != "test-config" {
		t.Errorf("handler returned unexpected body: %v", parsed)
	}
}

func TestHandleScalingConfigActionsGETAndDELETE(t *testing.T) {
	os.Setenv("POD_NAMESPACE", "costdeck")
	defer os.Unsetenv("POD_NAMESPACE")

	server := buildMockServer()

	config := &finopsv1.ScalingConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-config-action",
			Namespace: "costdeck",
		},
	}
	server.Client.Create(context.Background(), config)

	// GET
	reqGet, _ := http.NewRequest("GET", "/api/scaling/configs/test-config-action", nil)
	rrGet := httptest.NewRecorder()
	handler := http.HandlerFunc(server.handleScalingConfigActions)
	handler.ServeHTTP(rrGet, reqGet)

	if status := rrGet.Code; status != http.StatusOK {
		t.Errorf("GET returned wrong status code: got %v want %v", status, http.StatusOK)
	}

	// DELETE
	reqDel, _ := http.NewRequest("DELETE", "/api/scaling/configs/test-config-action", nil)
	rrDel := httptest.NewRecorder()
	handler.ServeHTTP(rrDel, reqDel)

	if status := rrDel.Code; status != http.StatusNoContent {
		t.Errorf("DELETE returned wrong status code: got %v want %v", status, http.StatusNoContent)
	}
}

// seedGroup creates a ScalingGroup that is pinned up by a manual override.
func seedGroup(t *testing.T, server *Server, name string) {
	t.Helper()
	forcedUp := true
	group := &finopsv1.ScalingGroup{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "costdeck"},
		Spec: finopsv1.ScalingGroupSpec{
			Namespaces: []string{"default"},
			Active:     &forcedUp,
			Schedules: []finopsv1.ScalingSchedule{{
				Days: []int{1, 2, 3, 4, 5}, StartTime: "09:00", EndTime: "18:00", Timezone: "UTC",
			}},
		},
	}
	if err := server.Client.Create(context.Background(), group); err != nil {
		t.Fatal(err)
	}
}

func postManual(t *testing.T, server *Server, name, body string) *httptest.ResponseRecorder {
	t.Helper()
	req, err := http.NewRequest("POST", "/api/scaling/groups/"+name+"/manual", bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	http.HandlerFunc(server.handleScalingGroupActions).ServeHTTP(rr, req)
	return rr
}

func fetchGroup(t *testing.T, server *Server, name string) *finopsv1.ScalingGroup {
	t.Helper()
	got := &finopsv1.ScalingGroup{}
	key := client.ObjectKey{Name: name, Namespace: "costdeck"}
	if err := server.Client.Get(context.Background(), key, got); err != nil {
		t.Fatal(err)
	}
	return got
}

// A null "active" is the only way back to schedule-driven behaviour, so it must clear
// spec.active rather than being treated as "no change" or as false.
func TestHandleScalingGroupManualNullClearsOverride(t *testing.T) {
	os.Setenv("POD_NAMESPACE", "costdeck")
	defer os.Unsetenv("POD_NAMESPACE")

	server := buildMockServer()
	seedGroup(t, server, "pinned-group")

	if rr := postManual(t, server, "pinned-group", `{"active": null}`); rr.Code != http.StatusOK {
		t.Fatalf("POST /manual returned %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}

	got := fetchGroup(t, server, "pinned-group")
	if got.Spec.Active != nil {
		t.Errorf("spec.active = %v; want nil so the schedule takes over", *got.Spec.Active)
	}
	if got.Spec.ActiveUntil != nil {
		t.Errorf("spec.activeUntil = %v; want nil once the override is cleared", got.Spec.ActiveUntil)
	}
	if len(got.Spec.Schedules) != 1 {
		t.Errorf("schedules were lost by the override call: %v", got.Spec.Schedules)
	}
}

func TestHandleScalingGroupManualFalsePinsDown(t *testing.T) {
	os.Setenv("POD_NAMESPACE", "costdeck")
	defer os.Unsetenv("POD_NAMESPACE")

	server := buildMockServer()
	seedGroup(t, server, "down-group")

	if rr := postManual(t, server, "down-group", `{"active": false}`); rr.Code != http.StatusOK {
		t.Fatalf("POST /manual returned %d, want %d", rr.Code, http.StatusOK)
	}

	got := fetchGroup(t, server, "down-group")
	if got.Spec.Active == nil || *got.Spec.Active {
		t.Errorf("spec.active = %v; want false", got.Spec.Active)
	}
}

func TestHandleScalingGroupManualActiveUntil(t *testing.T) {
	os.Setenv("POD_NAMESPACE", "costdeck")
	defer os.Unsetenv("POD_NAMESPACE")

	server := buildMockServer()
	seedGroup(t, server, "temp-group")

	deadline := metav1.NewTime(time.Now().Add(2 * time.Hour).Truncate(time.Second))
	body, err := json.Marshal(map[string]any{"active": true, "activeUntil": deadline})
	if err != nil {
		t.Fatal(err)
	}
	if rr := postManual(t, server, "temp-group", string(body)); rr.Code != http.StatusOK {
		t.Fatalf("POST /manual returned %d, want %d", rr.Code, http.StatusOK)
	}

	got := fetchGroup(t, server, "temp-group")
	if got.Spec.ActiveUntil == nil || !got.Spec.ActiveUntil.Equal(&deadline) {
		t.Errorf("spec.activeUntil = %v; want %v", got.Spec.ActiveUntil, deadline)
	}

	// A deadline in the past would produce an override nobody can observe.
	stale, err := json.Marshal(map[string]any{"active": true, "activeUntil": metav1.NewTime(time.Now().Add(-time.Hour))})
	if err != nil {
		t.Fatal(err)
	}
	if rr := postManual(t, server, "temp-group", string(stale)); rr.Code != http.StatusBadRequest {
		t.Errorf("POST /manual with a past deadline returned %d, want %d", rr.Code, http.StatusBadRequest)
	}
}
