package controller

import (
	"context"
	"strconv"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus/testutil"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/pricing"
	"github.com/migalsp/costdeck-operator/internal/scaling"
	"github.com/migalsp/costdeck-operator/internal/telemetry"
)

var _ = Describe("Savings reporting", func() {
	ctx := context.Background()
	const ns = "savings-test"
	key := types.NamespacedName{Name: "savings-config", Namespace: "default"}

	BeforeEach(func() {
		Expect(k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})).To(Or(Succeed(), MatchError(ContainSubstring("already exists"))))
		two := int32(2)
		labels := map[string]string{"app": "api"}
		Expect(k8sClient.Create(ctx, &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: ns},
			Spec: appsv1.DeploymentSpec{
				Replicas: &two,
				Selector: &metav1.LabelSelector{MatchLabels: labels},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: labels},
					Spec: corev1.PodSpec{Containers: []corev1.Container{{
						Name: "api", Image: "nginx",
						Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("500m"), corev1.ResourceMemory: resource.MustParse("1Gi"),
						}},
					}}},
				},
			},
		})).To(Succeed())
		down := false
		Expect(k8sClient.Create(ctx, &finopsv1.ScalingConfig{
			ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace},
			Spec:       finopsv1.ScalingConfigSpec{TargetNamespace: ns, Active: &down},
		})).To(Succeed())
	})

	AfterEach(func() {
		cfg := &finopsv1.ScalingConfig{}
		if k8sClient.Get(ctx, key, cfg) == nil {
			Expect(k8sClient.Delete(ctx, cfg)).To(Succeed())
		}
		dep := &appsv1.Deployment{}
		if k8sClient.Get(ctx, types.NamespacedName{Name: "api", Namespace: ns}, dep) == nil {
			Expect(k8sClient.Delete(ctx, dep)).To(Succeed())
		}
	})

	It("reports what the scaled-down workloads would cost per hour", func() {
		r := &ScalingConfigReconciler{
			Client:  k8sClient,
			Scheme:  k8sClient.Scheme(),
			Engine:  &scaling.Engine{Client: k8sClient},
			Pricing: &pricing.Resolver{Client: k8sClient},
		}
		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())

		cfg := &finopsv1.ScalingConfig{}
		Expect(k8sClient.Get(ctx, key, cfg)).To(Succeed())
		Expect(cfg.Status.OriginalReplicas).To(HaveKeyWithValue("*v1.Deployment/api", int32(2)))

		// Two replicas of 500m / 1Gi at the default on-premises heuristic rates.
		rates := (&pricing.Resolver{Client: k8sClient}).Rates(ctx)
		want := rates.Hourly(resource.MustParse("1"), resource.MustParse("2Gi"))
		got, err := strconv.ParseFloat(cfg.Status.EstimatedHourlySavings, 64)
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(BeNumerically("~", want, 0.0001))
		Expect(cfg.Status.Currency).To(Equal("USD"))

		Expect(testutil.ToFloat64(telemetry.HourlySavings.WithLabelValues("ScalingConfig", key.Name, "USD"))).To(BeNumerically("~", want, 0.0001))
		Expect(testutil.ToFloat64(telemetry.OverrideActive.WithLabelValues("ScalingConfig", key.Name))).To(Equal(1.0))
		Expect(testutil.ToFloat64(telemetry.DesiredUp.WithLabelValues("ScalingConfig", key.Name))).To(Equal(0.0))
	})
})
