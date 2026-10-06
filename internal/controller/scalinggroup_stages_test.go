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

// envtest runs no kube-controller-manager, so a Deployment scaled up never becomes ready:
// every stage here is stuck until its timeout lets the group move on.
var _ = Describe("ScalingGroup stages", func() {
	ctx := context.Background()
	key := types.NamespacedName{Name: "stages", Namespace: "default"}
	namespaces := []string{"stage-a", "stage-b"}

	replicasOf := func(ns string) int32 {
		var d appsv1.Deployment
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "app", Namespace: ns}, &d)).To(Succeed())
		return ptr.Deref(d.Spec.Replicas, 0)
	}

	BeforeEach(func() {
		for _, ns := range namespaces {
			err := k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})
			Expect(client.IgnoreAlreadyExists(err)).To(Succeed())
			labels := map[string]string{"app": "app"}
			Expect(k8sClient.Create(ctx, &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: ns},
				Spec: appsv1.DeploymentSpec{
					Replicas: ptr.To[int32](0),
					Selector: &metav1.LabelSelector{MatchLabels: labels},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{Labels: labels},
						Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "busybox"}}},
					},
				},
			})).To(Succeed())
		}
		Expect(k8sClient.Create(ctx, &finopsv1.ScalingGroup{
			ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace},
			Spec: finopsv1.ScalingGroupSpec{
				Namespaces: namespaces,
				Sequence:   []string{"stage-a", "stage-b"},
			},
		})).To(Succeed())
	})

	AfterEach(func() {
		Expect(k8sClient.Delete(ctx, &finopsv1.ScalingGroup{ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace}})).To(Succeed())
		for _, ns := range namespaces {
			err := k8sClient.Delete(ctx, &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: ns}})
			Expect(client.IgnoreNotFound(err)).To(Succeed())
		}
	})

	It("moves on from a stage that misses its timeout only with skipOnTimeout", func() {
		recorder := events.NewFakeRecorder(100)
		r := &ScalingGroupReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), Engine: &scaling.Engine{Client: k8sClient}, Recorder: recorder}
		reconcileGroup := func() *finopsv1.ScalingGroup {
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
			group := &finopsv1.ScalingGroup{}
			Expect(k8sClient.Get(ctx, key, group)).To(Succeed())
			return group
		}

		By("starting the first stage and holding back the second")
		group := reconcileGroup()
		Expect(replicasOf("stage-a")).To(Equal(int32(1)))
		Expect(replicasOf("stage-b")).To(Equal(int32(0)))
		Expect(group.Status.Phase).To(Equal(scaling.PhaseScalingUp))

		By("waiting past the timeout while skipOnTimeout is off")
		group.Status.LastAction = metav1.NewTime(time.Now().Add(-10 * time.Minute))
		Expect(k8sClient.Status().Update(ctx, group)).To(Succeed())
		group = reconcileGroup()
		Expect(replicasOf("stage-b")).To(Equal(int32(0)))

		By("moving on once skipOnTimeout is on: the stage has been waiting for 10 minutes")
		group.Spec.FeatureFlags = &finopsv1.ScalingGroupFeatureFlags{SkipOnTimeout: true, TimeoutMinutes: 5}
		Expect(k8sClient.Update(ctx, group)).To(Succeed())
		group = reconcileGroup()
		Expect(replicasOf("stage-b")).To(Equal(int32(1)))
		Expect(group.Status.Phase).To(Equal(scaling.PhaseScalingUp), "a skipped namespace is still not up")
		Expect(group.Status.CurrentStage).To(Equal(1))
		Expect(group.Status.SkippedNamespaces).To(Equal([]string{"stage-a"}))
		Expect(time.Since(group.Status.LastAction.Time)).To(BeNumerically("<", time.Minute), "the second stage gets its own timeout")
		var recorded []string
		for len(recorder.Events) > 0 {
			recorded = append(recorded, <-recorder.Events)
		}
		Expect(recorded).To(ContainElement("Warning ScalingTimeout Stage 1 not ready after 5 min, moving on without: stage-a"))

		By("keeping its place on the next reconcile instead of starting over")
		started := group.Status.LastAction
		group = reconcileGroup()
		Expect(group.Status.CurrentStage).To(Equal(1))
		Expect(group.Status.LastAction.Equal(&started)).To(BeTrue())
		Expect(group.Status.Phase).To(Equal(scaling.PhaseScalingUp))
	})

	It("keeps no stage state once the group is settled", func() {
		Expect(k8sClient.Delete(ctx, &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "stage-a"}})).To(Succeed())
		Expect(k8sClient.Delete(ctx, &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "stage-b"}})).To(Succeed())
		r := &ScalingGroupReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), Engine: &scaling.Engine{Client: k8sClient}, Recorder: events.NewFakeRecorder(100)}

		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
		first := &finopsv1.ScalingGroup{}
		Expect(k8sClient.Get(ctx, key, first)).To(Succeed())
		Expect(first.Status.Phase).To(Equal(scaling.PhaseScaledUp))
		Expect(first.Status.CurrentStage).To(BeZero())

		// A settled reconcile must not rewrite the status, or every write would trigger
		// another reconcile.
		time.Sleep(1100 * time.Millisecond)
		_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
		second := &finopsv1.ScalingGroup{}
		Expect(k8sClient.Get(ctx, key, second)).To(Succeed())
		Expect(second.ResourceVersion).To(Equal(first.ResourceVersion))
	})
})
