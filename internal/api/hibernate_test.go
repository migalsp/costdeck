package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/scaling"
)

var cnpgCluster = schema.GroupVersionKind{Group: "postgresql.cnpg.io", Version: "v1", Kind: "Cluster"}

// hibernateServer is a mock server in a cluster where CloudNativePG is installed.
func hibernateServer(t *testing.T, funcs *interceptor.Funcs) *Server {
	t.Helper()
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(finopsv1.AddToScheme(scheme))
	crds := meta.NewDefaultRESTMapper(nil)
	crds.Add(cnpgCluster, meta.RESTScopeNamespace)
	b := fakeclient.NewClientBuilder().WithScheme(scheme).WithRESTMapper(meta.MultiRESTMapper{testrestmapper.TestOnlyStaticRESTMapper(scheme), crds})
	if funcs != nil {
		b = b.WithInterceptorFuncs(*funcs)
	}
	server := &Server{Client: b.Build(), K8sClient: fake.NewClientset()}
	seedCalendars(t, server)
	return server
}

func seedCluster(t *testing.T, server *Server, ns, name string, annotations map[string]string) {
	t.Helper()
	u := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{"instances": int64(1)}}}
	u.SetGroupVersionKind(cnpgCluster)
	u.SetNamespace(ns)
	u.SetName(name)
	u.SetAnnotations(annotations)
	if err := server.Client.Create(context.Background(), u); err != nil {
		t.Fatal(err)
	}
}

func TestHibernateRulesAreValidatedOnSave(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "costdeck")
	server := buildMockServerWithK8s()
	seedCalendars(t, server)
	cases := []struct {
		path, body string
		code       int
		says       string
	}{
		{"/api/scaling/groups", `{"metadata":{"name":"bad-preset"},"spec":{"namespaces":["a"],"hibernate":[{"preset":"cnpg","kind":"Cluster"}]}}`,
			http.StatusBadRequest, "hibernate rule 1: a preset takes no"},
		{"/api/scaling/groups", `{"metadata":{"name":"core"},"spec":{"namespaces":["a"],"hibernate":[{"apiVersion":"v1","kind":"Pod","replicasField":"spec.replicas"}]}}`,
			http.StatusBadRequest, "must be group/version"},
		{"/api/scaling/configs", `{"metadata":{"name":"glob"},"spec":{"targetNamespace":"a","hibernate":[{"preset":"strimzi","names":["["]}]}}`,
			http.StatusBadRequest, "is not a name or glob"},
		{"/api/scaling/groups", `{"metadata":{"name":"ok"},"spec":{"namespaces":["a"],"hibernate":[{"preset":"cnpg","names":["pg-*"]},{"apiVersion":"redis.example.com/v1","kind":"Redis","replicasField":"spec.size"}]}}`,
			http.StatusCreated, ""},
	}
	for _, c := range cases {
		rr := send(t, server, "POST", c.path, c.body)
		if rr.Code != c.code || !strings.Contains(rr.Body.String(), c.says) {
			t.Errorf("POST %s %s: %d %s, want %d saying %q", c.path, c.body, rr.Code, rr.Body.String(), c.code, c.says)
		}
	}
	g := &finopsv1.ScalingGroup{}
	if err := server.Client.Get(context.Background(), client.ObjectKey{Namespace: "costdeck", Name: "ok"}, g); err != nil || len(g.Spec.Hibernate) != 2 || g.Spec.Hibernate[0].Names[0] != "pg-*" {
		t.Errorf("the saved group lost its hibernate rules: %+v (%v)", g.Spec.Hibernate, err)
	}
}

func TestPreviewListsServicesAndLeavesTheirWorkloads(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "costdeck")
	server := hibernateServer(t, nil)
	ctx := context.Background()
	if err := server.Client.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "shop-dev"}}); err != nil {
		t.Fatal(err)
	}
	seedWorkload(t, server, "shop-dev", "api", 1, "100m", "128Mi")
	seedWorkload(t, server, "shop-dev", "db-pooler", 1, "1", "1Gi")
	seedCluster(t, server, "shop-dev", "db", nil)
	seedCluster(t, server, "shop-dev", "archive", map[string]string{"cnpg.io/hibernation": "on"})
	// CloudNativePG runs the pooler: its Deployment is the operator's, not the schedule's.
	d := &appsv1.Deployment{}
	if err := server.Client.Get(ctx, client.ObjectKey{Namespace: "shop-dev", Name: "db-pooler"}, d); err != nil {
		t.Fatal(err)
	}
	d.OwnerReferences = []metav1.OwnerReference{{APIVersion: "postgresql.cnpg.io/v1", Kind: "Pooler", Name: "db", UID: "1", Controller: new(true)}}
	if err := server.Client.Update(ctx, d); err != nil {
		t.Fatal(err)
	}
	// The namespace's own config adds the Pooler kind; the group adds CloudNativePG.
	if err := server.Client.Create(ctx, &finopsv1.ScalingConfig{ObjectMeta: metav1.ObjectMeta{Name: "shop-dev", Namespace: "costdeck"},
		Spec: finopsv1.ScalingConfigSpec{TargetNamespace: "shop-dev", Hibernate: []finopsv1.HibernateRule{{APIVersion: "postgresql.cnpg.io/v1", Kind: "Pooler", ReplicasField: "spec.instances"}}}}); err != nil {
		t.Fatal(err)
	}

	rr := send(t, server, "POST", "/api/scaling/preview", `{"kind":"group","spec":{"namespaces":["shop-dev"],"hibernate":[{"preset":"cnpg"}]}}`)
	var got previewResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil || rr.Code != http.StatusOK {
		t.Fatalf("%d %s", rr.Code, rr.Body.String())
	}
	ns := got.Namespaces[0]
	want := []scaling.HibernatedService{{Kind: "Cluster", Name: "archive"}, {Kind: "Cluster", Name: "db", Running: true}}
	if len(ns.Services) != 2 || ns.Services[0] != want[0] || ns.Services[1] != want[1] {
		t.Errorf("services = %+v, want %+v", ns.Services, want)
	}
	for _, w := range ns.Workloads {
		if w.Excluded != (w.Name == "db-pooler") || (w.Name == "db-pooler" && w.ManagedBy != "Pooler") {
			t.Errorf("%s excluded = %v, managed by %q: only the operator's workload should be, by the Pooler", w.Name, w.Excluded, w.ManagedBy)
		}
	}

	if err := server.Client.Create(ctx, &finopsv1.ScalingGroup{ObjectMeta: metav1.ObjectMeta{Name: "shop", Namespace: "costdeck"},
		Spec: finopsv1.ScalingGroupSpec{Namespaces: []string{"shop-dev"}, Hibernate: []finopsv1.HibernateRule{{Preset: "cnpg"}}}}); err != nil {
		t.Fatal(err)
	}
	out, err := server.toolPreview(ctx, "group", "shop")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"servicesToStop":["Cluster db"]`) {
		t.Errorf("preview tool output lacks the cluster to stop:\n%s", out)
	}
}

func TestPreviewExplainsMissingHibernationPermissions(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "costdeck")
	server := hibernateServer(t, &interceptor.Funcs{List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
		if _, ok := list.(*unstructured.UnstructuredList); ok {
			return apierrors.NewForbidden(schema.GroupResource{Group: "postgresql.cnpg.io", Resource: "clusters"}, "", nil)
		}
		return c.List(ctx, list, opts...)
	}})
	if err := server.Client.Create(context.Background(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "shop-dev"}}); err != nil {
		t.Fatal(err)
	}
	rr := send(t, server, "POST", "/api/scaling/preview", `{"kind":"config","spec":{"targetNamespace":"shop-dev","hibernate":[{"preset":"cnpg"}]}}`)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "enable hibernation in the Helm chart") {
		t.Errorf("%d %s, want a warning naming hibernation.enabled", rr.Code, rr.Body.String())
	}
}
