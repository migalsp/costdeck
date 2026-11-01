package scaling

import (
	"context"
	"errors"
	"maps"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func stoppedDeployment(name string, recorded string) *appsv1.Deployment {
	zero := int32(0)
	d := &appsv1.Deployment{Name: name, Namespace: "shop", Spec: appsv1.DeploymentSpec{Replicas: &zero}}
	if recorded != "" {
		d.Annotations = map[string]string{OriginalReplicasAnnotation: recorded}
	}
	return d
}

func readDeployment(t *testing.T, e *Engine, name string) *appsv1.Deployment {
	t.Helper()
	d := &appsv1.Deployment{}
	must(t, e.Client.Get(context.Background(), client.ObjectKey{Namespace: "shop", Name: name}, d))
	return d
}

func markReady(t *testing.T, e *Engine, name string) {
	t.Helper()
	d := readDeployment(t, e, name)
	d.Status.Replicas = *d.Spec.Replicas
	d.Status.ReadyReplicas = *d.Spec.Replicas
	must(t, e.Client.Status().Update(context.Background(), d))
}

func TestScaleUpForgetsRecordedReplicasOnceTheStageIsReady(t *testing.T) {
	e := buildMockEngine()
	ctx := context.Background()
	must(t, e.Client.Create(ctx, stoppedDeployment("api", "3")))
	originals := map[string]int32{"*v1.Deployment/api": 3, "*v1.Deployment/deleted-meanwhile": 2}

	originals, done, err := e.ScaleTarget(ctx, "shop", true, nil, nil, originals, false)
	if err != nil || done {
		t.Fatalf("first pass = done %v, %v; want the scale-up started and not done", done, err)
	}
	d := readDeployment(t, e, "api")
	if *d.Spec.Replicas != 3 || d.Annotations[OriginalReplicasAnnotation] != "" {
		t.Errorf("api after the first pass = %d replicas, record %q; want 3 and the record removed", *d.Spec.Replicas, d.Annotations[OriginalReplicasAnnotation])
	}
	if originals["*v1.Deployment/api"] != 3 {
		t.Errorf("originals = %v; the status record must stay until api is ready", originals)
	}

	markReady(t, e, "api")
	originals, done, err = e.ScaleTarget(ctx, "shop", true, nil, nil, originals, false)
	if err != nil || !done {
		t.Fatalf("second pass = done %v, %v; want done", done, err)
	}
	if want := map[string]int32{"*v1.Deployment/deleted-meanwhile": 2}; !maps.Equal(originals, want) {
		t.Errorf("originals = %v, want only the record of a workload not in the namespace left", originals)
	}
}

func TestScaleUpRestoresAndClearsTheRecordOfAWorkloadAlreadyRunning(t *testing.T) {
	e := buildMockEngine()
	ctx := context.Background()
	must(t, e.Client.Create(ctx, stoppedDeployment("api", "4")))
	must(t, e.SetWorkloadReplicas(ctx, "shop", kindDeployment, "api", 2))
	markReady(t, e, "api")

	if _, done, err := e.ScaleTarget(ctx, "shop", true, nil, nil, nil, false); err != nil || done {
		t.Fatalf("ScaleTarget = done %v, %v; want it to wait for the restored replicas", done, err)
	}
	api := readDeployment(t, e, "api")
	if *api.Spec.Replicas != 4 || api.Annotations[OriginalReplicasAnnotation] != "" {
		t.Errorf("api = %d replicas, record %q after the namespace came up; want the recorded 4 restored over the partial manual start "+
			"(targetReplicas) and the record removed, so a later manual scale-to-zero does not inherit it", *api.Spec.Replicas, api.Annotations[OriginalReplicasAnnotation])
	}
	markReady(t, e, "api")
	if _, done, err := e.ScaleTarget(ctx, "shop", true, nil, nil, nil, false); err != nil || !done {
		t.Fatalf("ScaleTarget once the four replicas are ready = done %v, %v", done, err)
	}
}

func TestScaleTargetMovesPastAStuckStageOnlyAfterTheTimeout(t *testing.T) {
	ctx := context.Background()
	sequence := []string{"db", "api"}
	for _, c := range []struct {
		name          string
		timeoutPassed bool
		wantAPI       int32
		wantDone      bool
	}{
		{"before the timeout the next stage waits", false, 0, false},
		{"after the timeout the next stage starts", true, 2, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := buildMockEngine()
			must(t, e.Client.Create(ctx, stoppedDeployment("db", "1")))
			must(t, e.Client.Create(ctx, stoppedDeployment("api", "2")))

			_, done, err := e.ScaleTarget(ctx, "shop", true, sequence, nil, nil, c.timeoutPassed)
			if err != nil || done != c.wantDone {
				t.Fatalf("ScaleTarget = done %v, %v; want done %v", done, err, c.wantDone)
			}
			if db, api := *readDeployment(t, e, "db").Spec.Replicas, *readDeployment(t, e, "api").Spec.Replicas; db != 1 || api != c.wantAPI {
				t.Errorf("db=%d api=%d, want db started and api at %d", db, api, c.wantAPI)
			}
		})
	}
}

func TestScaleTargetReturnsAFailedPatch(t *testing.T) {
	replicas := int32(3)
	api := &appsv1.Deployment{Name: "api", Namespace: "shop", Spec: appsv1.DeploymentSpec{Replicas: &replicas}}
	e := hibernateEngine(t, nil, &interceptor.Funcs{Patch: func(context.Context, client.WithWatch, client.Object, client.Patch, ...client.PatchOption) error {
		return errors.New("the object has been modified")
	}}, api)

	_, done, err := e.ScaleTarget(context.Background(), "shop", false, nil, nil, nil, false)
	if done || err == nil || err.Error() != "scale *v1.Deployment/api in shop to 0 replicas: the object has been modified" {
		t.Errorf("ScaleTarget = done %v, %v", done, err)
	}
	if got := *readDeployment(t, e, "api").Spec.Replicas; got != 3 {
		t.Errorf("api = %d replicas after a failed patch", got)
	}
}

func TestReadinessOfAWorkloadThatCannotBeRead(t *testing.T) {
	for _, c := range []struct {
		name   string
		getErr error
		want   bool
	}{
		{"deleted mid-way has nothing left to wait for", apierrors.NewNotFound(schema.GroupResource{Group: "apps", Resource: "deployments"}, "api"), true},
		{"unreadable keeps the stage waiting", errors.New("apiserver unavailable"), false},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := hibernateEngine(t, nil, &interceptor.Funcs{Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
				return c.getErr
			}})
			api := stoppedDeployment("api", "")
			for _, active := range []bool{true, false} {
				if got := e.isResourceReady(context.Background(), api, active); got != c.want {
					t.Errorf("isResourceReady(active=%v) = %v, want %v", active, got, c.want)
				}
			}
		})
	}
}

func TestSetReplicasRefusesKindsItDoesNotScale(t *testing.T) {
	patches := 0
	e := hibernateEngine(t, nil, &interceptor.Funcs{Patch: func(context.Context, client.WithWatch, client.Object, client.Patch, ...client.PatchOption) error {
		patches++
		return nil
	}})
	configMap := &unstructured.Unstructured{}
	configMap.SetGroupVersionKind(schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"})
	if err := e.setReplicas(context.Background(), configMap, 0, 3); err == nil || err.Error() != "unsupported workload kind ConfigMap" {
		t.Errorf("setReplicas(ConfigMap) = %v", err)
	}
	if err := e.setReplicas(context.Background(), &corev1.Pod{}, 0, 3); err == nil || err.Error() != "unsupported workload type *v1.Pod" {
		t.Errorf("setReplicas(Pod) = %v", err)
	}
	if patches != 0 {
		t.Errorf("%d patches sent for kinds CostDeck does not scale", patches)
	}
}

func TestPhaseStaysInMotionWhenWorkloadsOrPodsCannotBeListed(t *testing.T) {
	ctx := context.Background()
	failAll := hibernateEngine(t, nil, &interceptor.Funcs{List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error {
		return errors.New("apiserver unavailable")
	}})
	if up, down := failAll.ComputePhase(ctx, "shop", true, nil), failAll.ComputePhase(ctx, "shop", false, nil); up != PhaseScalingUp || down != PhaseScalingDown {
		t.Errorf("ComputePhase without a workload list = %s / %s, want ScalingUp / ScalingDown", up, down)
	}
	if _, _, err := failAll.KeptDown(ctx, "shop", map[string]int32{"*v1.Deployment/api": 2}); err == nil {
		t.Error("KeptDown without a workload list returned no error")
	}

	stopped := stoppedDeployment("api", "2")
	stopped.Spec.Selector = &metav1.LabelSelector{MatchLabels: map[string]string{"app": "api"}}
	failPods := hibernateEngine(t, nil, &interceptor.Funcs{List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
		if _, ok := list.(*corev1.PodList); ok {
			return errors.New("pods unavailable")
		}
		return c.List(ctx, list, opts...)
	}}, stopped)
	if phase := failPods.ComputePhase(ctx, "shop", false, nil); phase != PhaseScalingDown {
		t.Errorf("ComputePhase without a pod list = %s, want ScalingDown until pods are known to be gone", phase)
	}
	if failPods.isResourceReady(ctx, stopped, false) {
		t.Error("a stopped workload whose pods cannot be listed counts as down")
	}
}
