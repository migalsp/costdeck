package controller

import (
	"context"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
)

func finOps(name, target string) *finopsv1.NamespaceFinOps {
	return &finopsv1.NamespaceFinOps{
		Name: name, Namespace: "costdeck",
		Spec: finopsv1.NamespaceFinOpsSpec{TargetNamespace: target},
	}
}

func TestDiscoveryForgetsDeletedNamespaces(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "costdeck")
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(finopsv1.AddToScheme(scheme))

	terminating := &corev1.Namespace{
		Name: "leaving", DeletionTimestamp: &metav1.Time{Time: time.Now()}, Finalizers: []string{"kubernetes"}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		&corev1.Namespace{Name: "kept"},
		terminating,
		finOps("kept", "kept"),
		finOps("gone", "gone"),
		finOps("leaving", "leaving"),
	).Build()
	r := &NamespaceDiscoveryReconciler{Client: c, Scheme: scheme}

	for _, ns := range []string{"gone", "leaving", "kept"} {
		if _, err := r.Reconcile(context.Background(), ctrl.Request{Name: ns}); err != nil {
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

func TestDiscoveryTracksNamespacesThatRunPods(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "costdeck")
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(finopsv1.AddToScheme(scheme))
	tracked := finOps("tracked", "tracked")
	tracked.Labels = map[string]string{"kept": "as-is"}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		&corev1.Namespace{Name: "busy"},
		&corev1.Pod{Name: "web-0", Namespace: "busy"},
		&corev1.Namespace{Name: "idle"},
		&corev1.Namespace{Name: "default"},
		&corev1.Namespace{Name: "tracked"},
		&corev1.Pod{Name: "db-0", Namespace: "tracked"},
		tracked,
	).Build()
	r := &NamespaceDiscoveryReconciler{Client: c, Scheme: scheme}
	ctx := context.Background()

	for _, ns := range []string{"busy", "idle", "default", "tracked"} {
		res, err := r.Reconcile(ctx, ctrl.Request{Name: ns})
		if err != nil || res != (ctrl.Result{}) {
			t.Fatalf("Reconcile(%s) = %+v, %v", ns, res, err)
		}
	}

	for name, want := range map[string]bool{"busy": true, "idle": false, "default": true} {
		var nf finopsv1.NamespaceFinOps
		err := c.Get(ctx, client.ObjectKey{Namespace: "costdeck", Name: name}, &nf)
		switch {
		case want && err != nil:
			t.Errorf("namespace %s is not tracked: %v", name, err)
		case want && nf.Spec.TargetNamespace != name:
			t.Errorf("NamespaceFinOps %s targets %q", name, nf.Spec.TargetNamespace)
		case !want && !apierrors.IsNotFound(err):
			t.Errorf("namespace %s without pods is tracked (err %v)", name, err)
		}
	}
	var kept finopsv1.NamespaceFinOps
	if err := c.Get(ctx, client.ObjectKey{Namespace: "costdeck", Name: "tracked"}, &kept); err != nil || kept.Labels["kept"] != "as-is" || kept.ResourceVersion != tracked.ResourceVersion {
		t.Errorf("an existing NamespaceFinOps was replaced: %+v, %v", kept.ObjectMeta, err)
	}
	var list finopsv1.NamespaceFinOpsList
	if err := c.List(ctx, &list); err != nil || len(list.Items) != 3 {
		t.Errorf("NamespaceFinOps = %d, want busy, default and tracked once each (err %v)", len(list.Items), err)
	}
}

var _ = Describe("Namespace discovery in a manager", func() {
	const operatorNamespace = "discovery-operator"
	ctx := context.Background()
	watched := "discovery-watched"

	It("tracks a namespace once a pod runs in it and forgets it once it is deleted", func() {
		GinkgoT().Setenv("POD_NAMESPACE", operatorNamespace)
		for _, ns := range []string{operatorNamespace, watched} {
			Expect(client.IgnoreAlreadyExists(k8sClient.Create(ctx, &corev1.Namespace{Name: ns}))).To(Succeed())
		}
		mgr := newTestManager()
		Expect((&NamespaceDiscoveryReconciler{Client: mgr.GetClient(), Scheme: mgr.GetScheme()}).SetupWithManager(mgr)).To(Succeed())
		mgrCtx, stop := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() {
			defer GinkgoRecover()
			defer close(done)
			Expect(mgr.Start(mgrCtx)).To(Succeed())
		}()
		DeferCleanup(func() {
			stop()
			Eventually(done, time.Minute).Should(BeClosed())
			var left finopsv1.NamespaceFinOpsList
			Expect(k8sClient.List(ctx, &left, client.InNamespace(operatorNamespace))).To(Succeed())
			for i := range left.Items {
				Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, &left.Items[i]))).To(Succeed())
			}
		})
		tracked := func() error {
			return k8sClient.Get(ctx, client.ObjectKey{Name: watched, Namespace: operatorNamespace}, &finopsv1.NamespaceFinOps{})
		}

		Consistently(tracked, 2*time.Second, 200*time.Millisecond).Should(Satisfy(apierrors.IsNotFound), "a namespace without pods is not tracked")

		Expect(k8sClient.Create(ctx, &corev1.Pod{Name: "web", Namespace: watched,
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "web", Image: "busybox"}}}})).To(Succeed())
		Eventually(tracked, 20*time.Second, 200*time.Millisecond).Should(Succeed())

		Expect(k8sClient.Delete(ctx, &corev1.Namespace{Name: watched})).To(Succeed())
		Eventually(tracked, 20*time.Second, 200*time.Millisecond).Should(Satisfy(apierrors.IsNotFound))
	})
})
