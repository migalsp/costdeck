package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
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
