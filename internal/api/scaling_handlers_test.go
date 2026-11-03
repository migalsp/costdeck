package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/scaling"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func buildMockServer() *Server {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(finopsv1.AddToScheme(scheme))

	return &Server{Client: fake.NewClientBuilder().WithScheme(scheme).Build()}
}

func TestHandleScalingGroupsGET(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "costdeck")

	server := buildMockServer()

	// Seed one item
	group := &finopsv1.ScalingGroup{
		Name:      "test-group",
		Namespace: "costdeck",
		Spec: finopsv1.ScalingGroupSpec{
			Namespaces: []string{"default"},
		},
	}
	if err := server.Client.Create(context.Background(), group); err != nil {
		t.Fatal(err)
	}

	req, err := http.NewRequest("GET", "/api/scaling/groups", nil)
	if err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	handler := server.routes()
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
	t.Setenv("POD_NAMESPACE", "costdeck")

	server := buildMockServer()

	body := []byte(`{"metadata":{"name":"new-group"},"spec":{"namespaces":["test"]}}`)
	req, err := http.NewRequest("POST", "/api/scaling/groups", bytes.NewBuffer(body))
	if err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	handler := server.routes()
	handler.ServeHTTP(rr, req)

	if status := rr.Code; status != http.StatusCreated {
		t.Errorf("handler returned wrong status code: got %v want %v", status, http.StatusCreated)
	}

	// Verify it was created in the mock cluster
	list := &finopsv1.ScalingGroupList{}
	if err := server.Client.List(context.Background(), list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 {
		t.Errorf("Expected 1 group created in cluster, got %d", len(list.Items))
	}
}

func TestHandleScalingConfigsGET(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "costdeck")

	server := buildMockServer()

	config := &finopsv1.ScalingConfig{
		Name:      "test-config",
		Namespace: "costdeck",
		Spec: finopsv1.ScalingConfigSpec{
			TargetNamespace: "app-ns",
		},
	}
	if err := server.Client.Create(context.Background(), config); err != nil {
		t.Fatal(err)
	}

	req, err := http.NewRequest("GET", "/api/scaling/configs", nil)
	if err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	handler := server.routes()
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
	t.Setenv("POD_NAMESPACE", "costdeck")

	server := buildMockServer()

	config := &finopsv1.ScalingConfig{
		Name:      "test-config-action",
		Namespace: "costdeck",
	}
	if err := server.Client.Create(context.Background(), config); err != nil {
		t.Fatal(err)
	}

	// GET
	reqGet, _ := http.NewRequest("GET", "/api/scaling/configs/test-config-action", nil)
	rrGet := httptest.NewRecorder()
	handler := server.routes()
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
		Name: name, Namespace: "costdeck",
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
	server.routes().ServeHTTP(rr, req)
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
	t.Setenv("POD_NAMESPACE", "costdeck")

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
	t.Setenv("POD_NAMESPACE", "costdeck")

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
	t.Setenv("POD_NAMESPACE", "costdeck")

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

func TestHandleScalingGroupManualUntilNextTransition(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "costdeck")
	server := buildMockServer()
	seedGroup(t, server, "night-group")

	before := time.Now()
	if rr := postManual(t, server, "night-group", `{"active": true, "until": "nextTransition"}`); rr.Code != http.StatusOK {
		t.Fatalf("POST /manual returned %d: %s", rr.Code, rr.Body.String())
	}
	got := fetchGroup(t, server, "night-group")
	if got.Spec.ActiveUntil == nil {
		t.Fatal("until=nextTransition must set spec.activeUntil")
	}
	// The seeded schedule is Mon-Fri 09:00-18:00 UTC, so the next change is at most a
	// weekend away and always on a minute boundary.
	want := (&scaling.Engine{}).NextScheduleChange(before, got.Spec.Schedules)
	if want == nil || !got.Spec.ActiveUntil.Time.Equal(*want) {
		t.Errorf("activeUntil = %v, want the next schedule change %v", got.Spec.ActiveUntil, want)
	}
}

func TestHandleScalingGroupManualDurationAndValidation(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "costdeck")
	server := buildMockServer()
	seedGroup(t, server, "dur-group")

	if rr := postManual(t, server, "dur-group", `{"active": false, "until": "2h"}`); rr.Code != http.StatusOK {
		t.Fatalf("POST /manual returned %d: %s", rr.Code, rr.Body.String())
	}
	got := fetchGroup(t, server, "dur-group")
	if got.Spec.ActiveUntil == nil || time.Until(got.Spec.ActiveUntil.Time) < 119*time.Minute {
		t.Errorf("activeUntil = %v, want about two hours from now", got.Spec.ActiveUntil)
	}

	for _, body := range []string{`{"active": true, "until": "soon"}`, `{"active": true, "until": "-1h"}`} {
		if rr := postManual(t, server, "dur-group", body); rr.Code != http.StatusBadRequest {
			t.Errorf("POST /manual %s returned %d, want 400", body, rr.Code)
		}
	}
}

func TestHandleScalingGroupManualNullClearsLegacyAnnotation(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "costdeck")
	server := buildMockServer()
	seedGroup(t, server, "legacy-group")
	group := fetchGroup(t, server, "legacy-group")
	group.Annotations = map[string]string{scaling.LegacyOverrideAnnotation: "ScaledUp"}
	if err := server.Client.Update(context.Background(), group); err != nil {
		t.Fatal(err)
	}

	if rr := postManual(t, server, "legacy-group", `{"active": null}`); rr.Code != http.StatusOK {
		t.Fatalf("POST /manual returned %d", rr.Code)
	}
	if _, ok := fetchGroup(t, server, "legacy-group").Annotations[scaling.LegacyOverrideAnnotation]; ok {
		t.Error("following the schedule must also drop the legacy override annotation")
	}
}

func putSettings(t *testing.T, server *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/api/settings", bytes.NewBufferString(body))
	rr := httptest.NewRecorder()
	server.routes().ServeHTTP(rr, req)
	return rr
}

func TestSettingsEntraValidationAndMasking(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "costdeck")
	server := buildMockServer()

	if rr := putSettings(t, server, `{"auth":{"entra":{"enabled":true}}}`); rr.Code != http.StatusBadRequest {
		t.Errorf("enabling Entra without tenant/client = %d, want 400", rr.Code)
	}
	if rr := putSettings(t, server, `{"auth":{"entra":{"tenantId":"t","clientId":"c","groupRoleMapping":{"g1":"superuser"}}}}`); rr.Code != http.StatusBadRequest {
		t.Errorf("unknown role in mapping = %d, want 400", rr.Code)
	}

	rr := putSettings(t, server, `{"auth":{"entra":{"enabled":true,"tenantId":"t","clientId":"c","clientSecret":"s","defaultRole":"reader","groupRoleMapping":{"g1":"owner"}}}}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("valid Entra settings = %d: %s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), `"s"`) && strings.Contains(rr.Body.String(), "clientSecret") {
		t.Error("the client secret must never be returned")
	}
	var resp SettingsResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	e := resp.Auth.Entra
	if e == nil || !e.Enabled || !e.HasClientSecret || e.DefaultRole != "viewer" || e.GroupRoleMapping["g1"] != "operator" {
		t.Errorf("stored Entra settings = %+v, want aliases normalised and the secret stored", e)
	}
}

func TestGetScalingGroupLeavesOutManagedFields(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "costdeck")
	server := interceptedServer(interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
		if err := c.Get(ctx, key, obj, opts...); err != nil {
			return err
		}
		obj.SetManagedFields([]metav1.ManagedFieldsEntry{{Manager: "kubectl", Operation: metav1.ManagedFieldsOperationApply, APIVersion: "finops.costdeck.io/v1"}})
		return nil
	}})
	createAll(t, server.Client, &finopsv1.ScalingGroup{Name: "shop", Namespace: "costdeck", Spec: finopsv1.ScalingGroupSpec{Namespaces: []string{"shop-dev"}}})

	rr := send(t, server, http.MethodGet, "/api/scaling/groups/shop", "")
	if rr.Code != http.StatusOK || strings.Contains(rr.Body.String(), "managedFields") || strings.Contains(rr.Body.String(), "kubectl") {
		t.Fatalf("GET = %d %s, want the group without managedFields", rr.Code, rr.Body.String())
	}
	if got := decodeInto[finopsv1.ScalingGroup](t, rr); got.Name != shopNamespace || strings.Join(got.Spec.Namespaces, ",") != "shop-dev" {
		t.Errorf("group = %+v", got)
	}
	if rr := send(t, server, http.MethodGet, "/api/scaling/groups/ghost", ""); rr.Code != http.StatusNotFound {
		t.Errorf("GET unknown group = %d, want 404", rr.Code)
	}
}

func TestDeleteScalingGroup(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "costdeck")
	server := buildMockServer()
	seedGroup(t, server, "shop")
	if rr := send(t, server, http.MethodDelete, "/api/scaling/groups/shop", ""); rr.Code != http.StatusNoContent {
		t.Fatalf("DELETE = %d %s", rr.Code, rr.Body.String())
	}
	var list finopsv1.ScalingGroupList
	if err := server.Client.List(context.Background(), &list); err != nil || len(list.Items) != 0 {
		t.Errorf("groups after delete = %d (%v)", len(list.Items), err)
	}
	if rr := send(t, server, http.MethodDelete, "/api/scaling/groups/shop", ""); rr.Code != http.StatusNotFound {
		t.Errorf("deleting twice = %d, want 404", rr.Code)
	}
}

func TestScalingGroupEventsListOnlyThatGroupsEvents(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "costdeck")
	server := buildMockServer()
	event := func(name, ns, kind, object, reason string) *corev1.Event {
		return &corev1.Event{Name: name, Namespace: ns, Reason: reason, InvolvedObject: corev1.ObjectReference{Kind: kind, Name: object, Namespace: "costdeck"}}
	}
	createAll(t, server.Client,
		event("e1", "costdeck", "ScalingGroup", "shop", "ScaledDown"),
		event("e2", "costdeck", "ScalingGroup", "search", "ScaledUp"),
		event("e3", "costdeck", "ScalingConfig", "shop", "ScaledUp"),
		event("e4", "elsewhere", "ScalingGroup", "shop", "ScaledUp"),
		event("e5", "costdeck", "ScalingGroup", "shop", "ScaledUp"),
	)

	events := decodeInto[[]corev1.Event](t, send(t, server, http.MethodGet, "/api/scaling/groups/shop/events", ""))
	reasons := make([]string, 0, len(events))
	for _, e := range events {
		reasons = append(reasons, e.Name+":"+e.Reason)
	}
	if strings.Join(reasons, ",") != "e1:ScaledDown,e5:ScaledUp" {
		t.Errorf("events = %v, want only the shop group's events in the operator namespace", reasons)
	}
	if body := send(t, server, http.MethodGet, "/api/scaling/groups/quiet/events", "").Body.String(); strings.TrimSpace(body) != "[]" {
		t.Errorf("a group without events = %s, want []", body)
	}
}

func TestUpdateScalingGroupRefusesBadInput(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "costdeck")
	server := buildMockServer()
	seedGroup(t, server, "shop")
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"shop", `{"spec":`, http.StatusBadRequest},
		{"shop", `{"spec":{"namespaces":["a"],"calendars":["no-such-calendar"]}}`, http.StatusBadRequest},
		{"ghost", `{"spec":{"namespaces":["a"]}}`, http.StatusNotFound},
	} {
		if rr := send(t, server, http.MethodPut, "/api/scaling/groups/"+tc.name, tc.body); rr.Code != tc.status {
			t.Errorf("PUT %s %s = %d %s, want %d", tc.name, tc.body, rr.Code, rr.Body.String(), tc.status)
		}
	}
	if g := fetchGroup(t, server, "shop"); strings.Join(g.Spec.Namespaces, ",") != "default" || g.Spec.Active == nil {
		t.Errorf("a refused update changed the group: %+v", g.Spec)
	}
}

func seedConfigObject(t *testing.T, server *Server, name string) {
	t.Helper()
	cfg := &finopsv1.ScalingConfig{Name: name, Namespace: "costdeck", Annotations: map[string]string{scaling.LegacyOverrideAnnotation: "ScaledUp"},
		Spec: finopsv1.ScalingConfigSpec{TargetNamespace: name, Active: new(true), Schedules: []finopsv1.ScalingSchedule{{
			Days: []int{1, 2, 3, 4, 5}, StartTime: "09:00", EndTime: "18:00", Timezone: "UTC",
		}}}}
	cfg.Status.Phase = scaling.PhaseScaledUp
	createAll(t, server.Client, cfg)
}

func fetchConfig(t *testing.T, server *Server, name string) *finopsv1.ScalingConfig {
	t.Helper()
	got := &finopsv1.ScalingConfig{}
	if err := server.Client.Get(context.Background(), client.ObjectKey{Name: name, Namespace: "costdeck"}, got); err != nil {
		t.Fatal(err)
	}
	return got
}

func TestUpdateScalingConfigReplacesTheWholeSpec(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "costdeck")
	server := buildMockServer()
	seedConfigObject(t, server, "shop")

	rr := send(t, server, http.MethodPut, "/api/scaling/configs/shop", `{"metadata":{"name":"renamed"},"spec":{"targetNamespace":"shop","schedules":[{"days":[6],"startTime":"10:00","endTime":"12:00","timezone":"UTC"}]},"status":{"phase":"Forged"}}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("PUT = %d %s", rr.Code, rr.Body.String())
	}
	got := fetchConfig(t, server, "shop")
	if got.Spec.Active != nil || len(got.Spec.Schedules) != 1 || got.Spec.Schedules[0].StartTime != "10:00" {
		t.Errorf("spec = %+v, want the new schedule and the override dropped with the omitted field", got.Spec)
	}
	if got.Status.Phase != scaling.PhaseScaledUp {
		t.Errorf("status = %q, a client cannot write the status through the spec endpoint", got.Status.Phase)
	}
	if body := decodeInto[finopsv1.ScalingConfig](t, rr); body.Name != shopNamespace || body.Spec.Schedules[0].StartTime != "10:00" {
		t.Errorf("response = %+v, want the stored config", body)
	}
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"shop", `{"spec":`, http.StatusBadRequest},
		{"shop", `{"spec":{"targetNamespace":"shop","calendars":["no-such-calendar"]}}`, http.StatusBadRequest},
		{"ghost", `{"spec":{"targetNamespace":"ghost"}}`, http.StatusNotFound},
	} {
		if rr := send(t, server, http.MethodPut, "/api/scaling/configs/"+tc.name, tc.body); rr.Code != tc.status {
			t.Errorf("PUT %s %s = %d %s, want %d", tc.name, tc.body, rr.Code, rr.Body.String(), tc.status)
		}
	}
}

func TestScalingConfigManualOverride(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "costdeck")
	server := buildMockServer()
	seedConfigObject(t, server, "batch")
	manual := func(name, body string) *httptest.ResponseRecorder {
		return send(t, server, http.MethodPost, "/api/scaling/configs/"+name+"/manual", body)
	}

	if rr := manual("batch", `{"active": false, "until": "2h"}`); rr.Code != http.StatusOK {
		t.Fatalf("pin down = %d %s", rr.Code, rr.Body.String())
	}
	got := fetchConfig(t, server, "batch")
	if got.Spec.Active == nil || *got.Spec.Active || got.Spec.ActiveUntil == nil || time.Until(got.Spec.ActiveUntil.Time) < 119*time.Minute {
		t.Errorf("override = %v until %v, want down for two hours", got.Spec.Active, got.Spec.ActiveUntil)
	}
	if _, kept := got.Annotations[scaling.LegacyOverrideAnnotation]; !kept {
		t.Error("an override must leave the legacy annotation until control goes back to the schedule")
	}

	for _, body := range []string{`{"active": true, "until": "soon"}`, `{"active":`} {
		if rr := manual("batch", body); rr.Code != http.StatusBadRequest {
			t.Errorf("manual %s = %d, want 400", body, rr.Code)
		}
	}
	if rr := manual("ghost", `{"active": true}`); rr.Code != http.StatusNotFound {
		t.Errorf("manual on an unknown config = %d, want 404", rr.Code)
	}

	if rr := manual("batch", `{"active": null}`); rr.Code != http.StatusOK {
		t.Fatalf("resume = %d %s", rr.Code, rr.Body.String())
	}
	got = fetchConfig(t, server, "batch")
	if got.Spec.Active != nil || got.Spec.ActiveUntil != nil || len(got.Spec.Schedules) != 1 {
		t.Errorf("after resume = %+v, want the schedule back in control", got.Spec)
	}
	if _, kept := got.Annotations[scaling.LegacyOverrideAnnotation]; kept {
		t.Error("following the schedule must also drop the legacy override annotation")
	}
}

func TestCreateScheduleStartsScheduleDrivenAndRefusesDuplicates(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "costdeck")
	server := buildMockServer()
	for _, kind := range []string{"groups", "configs"} {
		body := `{"metadata":{"name":"shop","namespace":"elsewhere","resourceVersion":"42"},"spec":{"namespaces":["shop"],"targetNamespace":"shop","active":false,"activeUntil":"2030-01-01T00:00:00Z"},"status":{"phase":"ScaledDown"}}`
		rr := send(t, server, http.MethodPost, "/api/scaling/"+kind, body)
		if rr.Code != http.StatusCreated {
			t.Fatalf("create %s = %d %s", kind, rr.Code, rr.Body.String())
		}
		if strings.Contains(rr.Body.String(), "ScaledDown") || strings.Contains(rr.Body.String(), `"active"`) {
			t.Errorf("created %s = %s, want no status and no override", kind, rr.Body.String())
		}
		if rr := send(t, server, http.MethodPost, "/api/scaling/"+kind, body); rr.Code != http.StatusConflict {
			t.Errorf("creating %s shop twice = %d, want 409", kind, rr.Code)
		}
		if rr := send(t, server, http.MethodPost, "/api/scaling/"+kind, `{"metadata":`); rr.Code != http.StatusBadRequest {
			t.Errorf("malformed %s body = %d, want 400", kind, rr.Code)
		}
	}
	g, c := fetchGroup(t, server, shopNamespace), fetchConfig(t, server, shopNamespace)
	if g.Spec.Active != nil || g.Spec.ActiveUntil != nil || g.Status.Phase != "" || c.Spec.Active != nil || c.Spec.ActiveUntil != nil || c.Status.Phase != "" {
		t.Errorf("stored group %+v / config %+v, want both in the operator namespace, schedule-driven", g, c)
	}
}
