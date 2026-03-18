package controller

import (
	"context"
	"os"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
)

// NamespaceDiscoveryReconciler watches namespaces and creates NamespaceFinOps CRs
type NamespaceDiscoveryReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups=finops.costdeck.io,resources=namespacefinops,verbs=get;list;watch;create;update;patch;delete

func (r *NamespaceDiscoveryReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	l := log.FromContext(ctx)

	// Fetch the Namespace
	var ns corev1.Namespace
	if err := r.Get(ctx, req.NamespacedName, &ns); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if ns.Name != "default" {
		// Skip system namespaces if needed, but User wanted them if they have resources.
		// Let's check if there are any pods in this namespace.
		var podList corev1.PodList
		if err := r.List(ctx, &podList, client.InNamespace(ns.Name), client.Limit(1)); err != nil {
			return ctrl.Result{}, err
		}

		if len(podList.Items) == 0 {
			return ctrl.Result{}, nil
		}
	}

	// It has pods! Check if NamespaceFinOps already exists for it in the operator namespace.
	operatorNs := os.Getenv("POD_NAMESPACE")
	if operatorNs == "" {
		operatorNs = "costdeck"
	}

	finOpsName := ns.Name // Use namespace name as CR name
	var existing finopsv1.NamespaceFinOps
	err := r.Get(ctx, client.ObjectKey{Name: finOpsName, Namespace: operatorNs}, &existing)
	if err == nil {
		return ctrl.Result{}, nil // Already exists
	}

	if !apierrors.IsNotFound(err) {
		return ctrl.Result{}, err
	}

	// Create it!
	l.Info("Auto-discovering namespace", "name", ns.Name)
	newFinOps := &finopsv1.NamespaceFinOps{
		ObjectMeta: metav1.ObjectMeta{
			Name:      finOpsName,
			Namespace: operatorNs,
		},
		Spec: finopsv1.NamespaceFinOpsSpec{
			TargetNamespace: ns.Name,
		},
	}

	if err := r.Create(ctx, newFinOps); err != nil {
		l.Error(err, "Failed to auto-create NamespaceFinOps", "name", ns.Name)
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *NamespaceDiscoveryReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Namespace{}).
		Watches(
			&corev1.Pod{},
			handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
				return []reconcile.Request{
					{NamespacedName: types.NamespacedName{Name: obj.GetNamespace()}},
				}
			}),
		).
		Complete(r)
}
