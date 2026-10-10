package controller

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/scaling"
)

// envtest runs no kube-controller-manager, so the test plays its part and sets how many
// replicas are ready.
var _ = Describe("ScalingGroup notifications", func() {
	ctx := context.Background()
	key := types.NamespacedName{Name: "notify", Namespace: "default"}
	app := types.NamespacedName{Name: "app", Namespace: "notify-a"}

	setReady := func(n int32) {
		var d appsv1.Deployment
		Expect(k8sClient.Get(ctx, app, &d)).To(Succeed())
		d.Status.Replicas, d.Status.ReadyReplicas = n, n
		Expect(k8sClient.Status().Update(ctx, &d)).To(Succeed())
	}

	BeforeEach(func() {
		err := k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: app.Namespace}})
		Expect(client.IgnoreAlreadyExists(err)).To(Succeed())
		labels := map[string]string{"app": "app"}
		Expect(k8sClient.Create(ctx, &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: app.Name, Namespace: app.Namespace},
			Spec: appsv1.DeploymentSpec{
				Replicas: ptr.To[int32](1),
				Selector: &metav1.LabelSelector{MatchLabels: labels},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: labels},
					Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "busybox"}}},
				},
			},
		})).To(Succeed())
		Expect(k8sClient.Create(ctx, &finopsv1.ScalingGroup{
			ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace},
			Spec:       finopsv1.ScalingGroupSpec{Category: "Test", Namespaces: []string{app.Namespace}},
		})).To(Succeed())
	})

	AfterEach(func() {
		Expect(k8sClient.Delete(ctx, &finopsv1.ScalingGroup{ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace}})).To(Succeed())
		Expect(k8sClient.Delete(ctx, &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: app.Name, Namespace: app.Namespace}})).To(Succeed())
	})

	It("does not announce a group that is up again after a pod restarted, only real changes", func() {
		notifier := &recordingNotifier{done: make(chan struct{}, 10)}
		r := &ScalingGroupReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), Engine: &scaling.Engine{Client: k8sClient},
			Recorder: events.NewFakeRecorder(100), Notifier: notifier}
		phase := func() string {
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
			var g finopsv1.ScalingGroup
			Expect(k8sClient.Get(ctx, key, &g)).To(Succeed())
			return g.Status.Phase
		}

		By("settling up: the first reconcile of a group is not announced")
		setReady(1)
		Expect(phase()).To(Equal(scaling.PhaseScaledUp))

		By("a pod crashing and recovering, twice")
		for range 2 {
			setReady(0)
			Expect(phase()).To(Equal(scaling.PhaseScalingUp))
			setReady(1)
			Expect(phase()).To(Equal(scaling.PhaseScaledUp))
		}
		Consistently(notifier.done, 300*time.Millisecond).ShouldNot(Receive(), "a recovered group must not be announced as up again")

		By("scaling it down by hand is announced once")
		var g finopsv1.ScalingGroup
		Expect(k8sClient.Get(ctx, key, &g)).To(Succeed())
		g.Spec.Active = new(false)
		Expect(k8sClient.Update(ctx, &g)).To(Succeed())
		Expect(phase()).To(Equal(scaling.PhaseScalingDown))
		setReady(0)
		Expect(phase()).To(Equal(scaling.PhaseScaledDown))
		Eventually(notifier.done).Should(Receive())
		Consistently(notifier.done, 300*time.Millisecond).ShouldNot(Receive())
		notifier.mu.Lock()
		defer notifier.mu.Unlock()
		Expect(notifier.msgs).To(ConsistOf(ContainSubstring("Group `notify` is scaled down by a manual override")))
	})
})
