package controller

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
)

func finOps(name, target string) *finopsv1.NamespaceFinOps {
	return &finopsv1.NamespaceFinOps{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "costdeck"},
		Spec:       finopsv1.NamespaceFinOpsSpec{TargetNamespace: target},
	}
}

func TestDiscoveryForgetsDeletedNamespaces(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "costdeck")
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(finopsv1.AddToScheme(scheme))

	terminating := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name: "leaving", DeletionTimestamp: &metav1.Time{Time: time.Now()}, Finalizers: []string{"kubernetes"},
	}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kept"}},
		terminating,
		finOps("kept", "kept"),
		finOps("gone", "gone"),
		finOps("leaving", "leaving"),
	).Build()
	r := &NamespaceDiscoveryReconciler{Client: c, Scheme: scheme}

	for _, ns := range []string{"gone", "leaving", "kept"} {
		if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: ns}}); err != nil {
			t.Fatalf("Reconcile(%s): %v", ns, err)
		}
	}

	for name, wantGone := range map[string]bool{"gone": true, "leaving": true, "kept": false} {
		err := c.Get(context.Background(), client.ObjectKey{Namespace: "costdeck", Name: name}, &finopsv1.NamespaceFinOps{})
		if gone := apierrors.IsNotFound(err); gone != wantGone {
			t.Errorf("NamespaceFinOps %s: deleted = %v, want %v (err %v)", name, gone, wantGone, err)
		}
	}
}
