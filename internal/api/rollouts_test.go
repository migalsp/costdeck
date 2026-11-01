package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/meta/testrestmapper"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes/fake"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	fakeclient "sigs.k8s.io/controller-runtime/pkg/client/fake"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
)

// Argo Rollouts show up on the namespace page and can be scaled from it like Deployments;
// a Deployment a Rollout takes its pods from does not.
func TestWorkloadsIncludeArgoRollouts(t *testing.T) {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(finopsv1.AddToScheme(scheme))
	gvk := schema.GroupVersionKind{Group: "argoproj.io", Version: "v1alpha1", Kind: "Rollout"}
	crds := meta.NewDefaultRESTMapper(nil)
	crds.Add(gvk, meta.RESTScopeNamespace)
	web := &unstructured.Unstructured{Object: map[string]any{
		"spec":   map[string]any{"replicas": int64(2)},
		"status": map[string]any{"readyReplicas": int64(2), "replicas": int64(2)},
	}}
	web.SetGroupVersionKind(gvk)
	web.SetNamespace("shop")
	web.SetName("web")
	ref := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{"workloadRef": map[string]any{"kind": "Deployment", "name": "legacy"}}}}
	ref.SetGroupVersionKind(gvk)
	ref.SetNamespace("shop")
	ref.SetName("ref")
	zero := int32(0)
	legacy := &appsv1.Deployment{Name: "legacy", Namespace: "shop", Spec: appsv1.DeploymentSpec{Replicas: &zero}}
	c := fakeclient.NewClientBuilder().WithScheme(scheme).WithRESTMapper(meta.MultiRESTMapper{testrestmapper.TestOnlyStaticRESTMapper(scheme), crds}).
		WithObjects(web, ref, legacy).Build()
	server := &Server{Client: c, K8sClient: fake.NewClientset()}

	rr := send(t, server, "GET", "/api/namespaces/shop/workloads", "")
	var got []WorkloadDetail
	_ = json.Unmarshal(rr.Body.Bytes(), &got)
	if len(got) != 2 || got[0].Kind != "Rollout" || got[1].Kind != "Rollout" {
		t.Fatalf("workloads = %+v", got)
	}
	for _, w := range got {
		if w.Name == "web" && (w.Replicas != 2 || w.ReadyReplicas != 2 || w.Status != "running") {
			t.Errorf("web = %+v", w)
		}
	}

	if rr := send(t, server, "PUT", "/api/namespaces/shop/workloads/web", `{"kind":"Rollout","replicas":0}`); rr.Code != http.StatusOK {
		t.Fatalf("scaling a Rollout: %d %s", rr.Code, rr.Body.String())
	}
	cur := &unstructured.Unstructured{}
	cur.SetGroupVersionKind(gvk)
	_ = c.Get(context.Background(), client.ObjectKeyFromObject(web), cur)
	if n, _, _ := unstructured.NestedInt64(cur.Object, "spec", "replicas"); n != 0 {
		t.Errorf("web has %d replicas", n)
	}
	if rr := send(t, server, "PUT", "/api/namespaces/shop/workloads/web", `{"kind":"DaemonSet","replicas":0}`); rr.Code != http.StatusBadRequest {
		t.Errorf("an unknown kind: %d", rr.Code)
	}
	if rr := send(t, server, "PUT", "/api/namespaces/shop/workloads/gone", `{"kind":"Rollout","replicas":1}`); rr.Code != http.StatusNotFound {
		t.Errorf("a missing Rollout: %d", rr.Code)
	}
}
