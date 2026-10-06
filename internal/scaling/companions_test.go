package scaling

import (
	"context"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func scaledObject(name string, annotations map[string]string) *unstructured.Unstructured {
	so := &unstructured.Unstructured{}
	so.SetGroupVersionKind(schema.GroupVersionKind{Group: "keda.sh", Version: "v1alpha1", Kind: "ScaledObject"})
	so.SetName(name)
	so.SetNamespace("test-ns")
	so.SetAnnotations(annotations)
	return so
}

func companionEngine(t *testing.T, withKEDA bool, objs ...client.Object) *Engine {
	t.Helper()
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	mapper := meta.NewDefaultRESTMapper(nil)
	mapper.Add(batchv1.SchemeGroupVersion.WithKind("CronJob"), meta.RESTScopeNamespace)
	if withKEDA {
		mapper.Add(schema.GroupVersionKind{Group: "keda.sh", Version: "v1alpha1", Kind: "ScaledObject"}, meta.RESTScopeNamespace)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithRESTMapper(mapper).WithObjects(objs...).Build()
	return &Engine{Client: c}
}

func TestCompanionsArePausedAndOnlyOursResumed(t *testing.T) {
	ctx := context.Background()
	nightly := &batchv1.CronJob{ObjectMeta: metav1.ObjectMeta{Name: "nightly", Namespace: "test-ns"}}
	manual := &batchv1.CronJob{ObjectMeta: metav1.ObjectMeta{Name: "manual", Namespace: "test-ns"}, Spec: batchv1.CronJobSpec{Suspend: new(true)}}
	keep := &batchv1.CronJob{ObjectMeta: metav1.ObjectMeta{Name: "backup-keep", Namespace: "test-ns"}}
	e := companionEngine(t, true, nightly, manual, keep, scaledObject("api", nil))

	if err := e.holdCompanions(ctx, "test-ns", []string{"backup-*"}); err != nil {
		t.Fatal(err)
	}
	get := func(name string) *batchv1.CronJob {
		cj := &batchv1.CronJob{}
		if err := e.Client.Get(ctx, client.ObjectKey{Name: name, Namespace: "test-ns"}, cj); err != nil {
			t.Fatal(err)
		}
		return cj
	}
	if cj := get("nightly"); cj.Spec.Suspend == nil || !*cj.Spec.Suspend || cj.Annotations[AnnotationSuspended] != "true" {
		t.Errorf("nightly was not suspended by CostDeck: %+v", cj.Spec.Suspend)
	}
	if cj := get("backup-keep"); cj.Spec.Suspend != nil && *cj.Spec.Suspend {
		t.Error("an excluded CronJob must keep running")
	}
	so := scaledObject("api", nil)
	if err := e.Client.Get(ctx, client.ObjectKeyFromObject(so), so); err != nil {
		t.Fatal(err)
	}
	if so.GetAnnotations()[kedaPausedReplicas] != "0" {
		t.Errorf("ScaledObject was not paused: %v", so.GetAnnotations())
	}

	if err := e.releaseCompanions(ctx, "test-ns", []string{"backup-*"}); err != nil {
		t.Fatal(err)
	}
	if cj := get("nightly"); *cj.Spec.Suspend || cj.Annotations[AnnotationSuspended] != "" {
		t.Error("nightly was not resumed")
	}
	if cj := get("manual"); cj.Spec.Suspend == nil || !*cj.Spec.Suspend {
		t.Error("a CronJob suspended by a user must stay suspended")
	}
	if err := e.Client.Get(ctx, client.ObjectKeyFromObject(so), so); err != nil {
		t.Fatal(err)
	}
	if _, ok := so.GetAnnotations()[kedaPausedReplicas]; ok {
		t.Errorf("ScaledObject was not resumed: %v", so.GetAnnotations())
	}
}

func TestClustersWithoutKEDAAreFine(t *testing.T) {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	calls := 0
	c := fake.NewClientBuilder().WithScheme(scheme).WithInterceptorFuncs(interceptor.Funcs{
		List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			if u, ok := list.(*unstructured.UnstructuredList); ok {
				calls++
				return &meta.NoKindMatchError{GroupKind: u.GroupVersionKind().GroupKind()}
			}
			return cl.List(ctx, list, opts...)
		},
	}).Build()
	e := &Engine{Client: c}
	if err := e.holdCompanions(context.Background(), "test-ns", nil); err != nil {
		t.Fatalf("a cluster without KEDA must not fail scaling: %v", err)
	}
	if err := e.holdCompanions(context.Background(), "test-ns", nil); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Errorf("the missing KEDA CRDs should be remembered, got %d list calls", calls)
	}
}
