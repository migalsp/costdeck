package controller

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/scaling"
)

var _ = Describe("ScalingGroup dependencies", func() {
	ctx := context.Background()
	platformKey := types.NamespacedName{Name: "dep-platform", Namespace: "default"}
	envKey := types.NamespacedName{Name: "dep-env", Namespace: "default"}

	var reconciler *ScalingGroupReconciler

	reconcileGroup := func(key types.NamespacedName) *finopsv1.ScalingGroup {
		GinkgoHelper()
		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
		g := &finopsv1.ScalingGroup{}
		Expect(k8sClient.Get(ctx, key, g)).To(Succeed())
		return g
	}

	BeforeEach(func() {
		reconciler = &ScalingGroupReconciler{
			Client:   k8sClient,
			Scheme:   k8sClient.Scheme(),
			Engine:   &scaling.Engine{Client: k8sClient},
			Recorder: record.NewFakeRecorder(100),
		}
		Expect(k8sClient.Create(ctx, &finopsv1.ScalingGroup{
			ObjectMeta: metav1.ObjectMeta{Name: platformKey.Name, Namespace: platformKey.Namespace},
			Spec: finopsv1.ScalingGroupSpec{
				Category:   "Platform",
				Namespaces: []string{"dep-test-platform"},
				Activation: finopsv1.ActivationOnDemand,
			},
		})).To(Succeed())
		Expect(k8sClient.Create(ctx, &finopsv1.ScalingGroup{
			ObjectMeta: metav1.ObjectMeta{Name: envKey.Name, Namespace: envKey.Namespace},
			Spec: finopsv1.ScalingGroupSpec{
				Category:   "Solution",
				Namespaces: []string{"dep-test-env"},
				DependsOn:  []string{platformKey.Name},
			},
		})).To(Succeed())
	})

	AfterEach(func() {
		for _, key := range []types.NamespacedName{platformKey, envKey} {
			g := &finopsv1.ScalingGroup{}
			if err := k8sClient.Get(ctx, key, g); err == nil {
				Expect(k8sClient.Delete(ctx, g)).To(Succeed())
			}
		}
	})

	It("starts the platform on demand, holds the environment back until it is up, and releases it afterwards", func() {
		By("holding the environment back while the platform is not up")
		env := reconcileGroup(envKey)
		Expect(env.Status.Phase).To(Equal(scaling.PhaseWaitingForDependencies))
		deps := meta.FindStatusCondition(env.Status.Conditions, ConditionDependenciesReady)
		Expect(deps).NotTo(BeNil())
		Expect(deps.Status).To(Equal(metav1.ConditionFalse))

		By("pulling the OnDemand platform up for its dependent")
		platform := reconcileGroup(platformKey)
		Expect(platform.Status.Mode).To(Equal(scaling.ModeDependency))
		Expect(platform.Status.RequiredBy).To(ConsistOf(envKey.Name))
		Expect(platform.Status.Phase).To(Equal(scaling.PhaseScaledUp))

		By("letting the environment come up once the platform is ScaledUp")
		env = reconcileGroup(envKey)
		Expect(env.Status.Phase).To(Equal(scaling.PhaseScaledUp))
		Expect(meta.IsStatusConditionTrue(env.Status.Conditions, ConditionDependenciesReady)).To(BeTrue())

		By("scaling the platform down only after the environment is down")
		down := false
		env.Spec.Active = &down
		Expect(k8sClient.Update(ctx, env)).To(Succeed())
		env = reconcileGroup(envKey)
		Expect(env.Status.Phase).To(Equal(scaling.PhaseScaledDown))

		platform = reconcileGroup(platformKey)
		Expect(platform.Status.Mode).To(Equal(scaling.ModeOnDemand))
		Expect(platform.Status.RequiredBy).To(BeEmpty())
		Expect(platform.Status.Phase).To(Equal(scaling.PhaseScaledDown))
	})
})
