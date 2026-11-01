package controller

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/scaling"
)

var _ = Describe("Dry runs", func() {
	ctx := context.Background()
	closed := []finopsv1.ScalingSchedule{{StartDay: new(0), StartTime: "03:00", EndDay: new(0), EndTime: "03:01", Timezone: "Pacific/Kiritimati"}}
	replicasOf := func(ns string) int32 {
		GinkgoHelper()
		var d appsv1.Deployment
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "app", Namespace: ns}, &d)).To(Succeed())
		return ptr.Deref(d.Spec.Replicas, 0)
	}
	workload := func(ns string, replicas int32) {
		GinkgoHelper()
		Expect(client.IgnoreAlreadyExists(k8sClient.Create(ctx, &corev1.Namespace{Name: ns}))).To(Succeed())
		labels := map[string]string{"app": "app"}
		Expect(k8sClient.Create(ctx, &appsv1.Deployment{
			Name: "app", Namespace: ns,
			Spec: appsv1.DeploymentSpec{
				Replicas: new(replicas), Selector: &metav1.LabelSelector{MatchLabels: labels},
				Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "busybox"}}}},
			},
		})).To(Succeed())
	}
	AfterEach(func() {
		for _, ns := range []string{"dry-group", "dry-config"} {
			Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, &appsv1.Deployment{Name: "app", Namespace: ns}))).To(Succeed())
		}
		Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, &finopsv1.ScalingGroup{Name: "dry", Namespace: "default"}))).To(Succeed())
		Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, &finopsv1.ScalingConfig{Name: "dry", Namespace: "default"}))).To(Succeed())
	})

	It("works out what a group would scale, changes nothing, and acts once switched off", func() {
		workload("dry-group", 3)
		key := types.NamespacedName{Name: "dry", Namespace: "default"}
		Expect(k8sClient.Create(ctx, &finopsv1.ScalingGroup{Name: key.Name, Namespace: key.Namespace,
			Spec: finopsv1.ScalingGroupSpec{Category: "Test", Namespaces: []string{"dry-group"}, Schedules: closed, DryRun: true}})).To(Succeed())
		r := &ScalingGroupReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), Engine: &scaling.Engine{Client: k8sClient}, Recorder: events.NewFakeRecorder(100)}
		reconcileGroup := func() *finopsv1.ScalingGroup {
			GinkgoHelper()
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
			g := &finopsv1.ScalingGroup{}
			Expect(k8sClient.Get(ctx, key, g)).To(Succeed())
			return g
		}

		g := reconcileGroup()
		Expect(replicasOf("dry-group")).To(Equal(int32(3)))
		Expect(g.Status.Phase).To(Equal(scaling.PhaseDryRun))
		Expect(g.Status.DesiredState).To(Equal(scaling.StateDown))
		cond := meta.FindStatusCondition(g.Status.Conditions, ConditionDryRun)
		Expect(cond).NotTo(BeNil())
		Expect(cond.Message).To(HavePrefix("Would scale down 1 workload(s) in 1 namespace(s) now."))

		g.Spec.DryRun = false
		Expect(k8sClient.Update(ctx, g)).To(Succeed())
		g = reconcileGroup()
		Expect(replicasOf("dry-group")).To(BeZero())
		Expect(meta.FindStatusCondition(g.Status.Conditions, ConditionDryRun)).To(BeNil())
	})

	It("does the same for a single-namespace schedule", func() {
		workload("dry-config", 2)
		key := types.NamespacedName{Name: "dry", Namespace: "default"}
		Expect(k8sClient.Create(ctx, &finopsv1.ScalingConfig{Name: key.Name, Namespace: key.Namespace,
			Spec: finopsv1.ScalingConfigSpec{TargetNamespace: "dry-config", Schedules: closed, DryRun: true}})).To(Succeed())
		r := &ScalingConfigReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), Engine: &scaling.Engine{Client: k8sClient}}
		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
		c := &finopsv1.ScalingConfig{}
		Expect(k8sClient.Get(ctx, key, c)).To(Succeed())
		Expect(replicasOf("dry-config")).To(Equal(int32(2)))
		Expect(c.Status.Phase).To(Equal(scaling.PhaseDryRun))
		Expect(meta.FindStatusCondition(c.Status.Conditions, ConditionDryRun).Message).To(ContainSubstring("Would scale down 1 workload(s)"))
	})
})
