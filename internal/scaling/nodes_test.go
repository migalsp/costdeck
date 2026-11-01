package scaling

import (
	"context"
	"errors"
	"slices"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func scheduledPod(ns, name, app, node string, phase corev1.PodPhase) *corev1.Pod {
	return &corev1.Pod{Name: name, Namespace: ns, Labels: map[string]string{"app": app},
		Spec: corev1.PodSpec{NodeName: node}, Status: corev1.PodStatus{Phase: phase}}
}

func selecting(name string, selector *metav1.LabelSelector) *appsv1.Deployment {
	return &appsv1.Deployment{Name: name, Namespace: "shop", Spec: appsv1.DeploymentSpec{Selector: selector}}
}

func TestWorkloadNodesListsTheNodesAScaleDownCouldFree(t *testing.T) {
	appSelector := func(app string) *metav1.LabelSelector {
		return &metav1.LabelSelector{MatchLabels: map[string]string{"app": app}}
	}
	db := &appsv1.StatefulSet{Name: "db", Namespace: "shop", Spec: appsv1.StatefulSetSpec{Selector: appSelector("db")}}
	web := rollout("web", map[string]any{"replicas": int64(1)})
	web.Object["status"] = map[string]any{"selector": "app=web"}
	e := hibernateEngine(t, []schema.GroupVersionKind{rolloutGVK}, nil,
		selecting("api", appSelector("api")), db, web,
		selecting("batch", appSelector("batch")),
		selecting("catch-all", &metav1.LabelSelector{}),
		selecting("no-selector", nil),
		scheduledPod("shop", "api-1", "api", "node-b", corev1.PodRunning),
		scheduledPod("shop", "api-2", "api", "node-c", corev1.PodPending),
		scheduledPod("shop", "api-3", "api", "node-x1", corev1.PodSucceeded),
		scheduledPod("shop", "api-4", "api", "node-x2", corev1.PodFailed),
		scheduledPod("shop", "api-5", "api", "", corev1.PodPending),
		scheduledPod("shop", "db-0", "db", "node-b", corev1.PodRunning),
		scheduledPod("shop", "web-1", "web", "node-a", corev1.PodRunning),
		scheduledPod("shop", "batch-1", "batch", "node-x3", corev1.PodRunning),
		scheduledPod("shop", "stray", "stray", "node-x4", corev1.PodRunning),
		scheduledPod("other", "api-1", "api", "node-x5", corev1.PodRunning),
	)

	got, err := e.WorkloadNodes(context.Background(), "shop", []string{"batch"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"node-a", "node-b", "node-c"}; !slices.Equal(got, want) {
		t.Errorf("WorkloadNodes = %v, want %v: running or pending pods of scaled workloads in the namespace, once each, sorted", got, want)
	}

	failing := hibernateEngine(t, nil, &interceptor.Funcs{List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
		if _, ok := list.(*corev1.PodList); ok {
			return errors.New("pods unavailable")
		}
		return c.List(ctx, list, opts...)
	}}, selecting("api", appSelector("api")))
	if got, err := failing.WorkloadNodes(context.Background(), "shop", nil); err == nil || err.Error() != "pods unavailable" || got != nil {
		t.Errorf("WorkloadNodes with a failing pod list = %v, %v", got, err)
	}
}
