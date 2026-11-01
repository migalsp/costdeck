package controller

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/scaling"
)

var _ = Describe("Realized savings", func() {
	ctx := context.Background()
	ns, key := "real-shop", types.NamespacedName{Name: "real-shop", Namespace: "default"}
	closed := []finopsv1.ScalingSchedule{{StartDay: new(0), StartTime: "03:00", EndDay: new(0), EndTime: "03:01", Timezone: "Pacific/Kiritimati"}}
	open := []finopsv1.ScalingSchedule{{Days: []int{0, 1, 2, 3, 4, 5, 6}, StartTime: "00:00", EndTime: "23:59", Timezone: "UTC"}}

	workload := func(name, node string) {
		GinkgoHelper()
		sel := map[string]string{"app": name}
		Expect(k8sClient.Create(ctx, &appsv1.Deployment{
			Name: name, Namespace: ns,
			Spec: appsv1.DeploymentSpec{Replicas: new(int32(1)), Selector: &metav1.LabelSelector{MatchLabels: sel},
				Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: sel}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Image: "busybox"}}}}},
		})).To(Succeed())
		// envtest schedules nothing: bind the pod to its node and report it running.
		p := &corev1.Pod{Name: name + "-1", Namespace: ns, Labels: sel,
			Spec: corev1.PodSpec{NodeName: node, Containers: []corev1.Container{{Name: "c", Image: "busybox"}}}}
		Expect(k8sClient.Create(ctx, p)).To(Succeed())
		p.Status.Phase = corev1.PodRunning
		Expect(k8sClient.Status().Update(ctx, p)).To(Succeed())
	}

	It("records the nodes a scale-down should free, once, and forgets them when up", func() {
		Expect(client.IgnoreAlreadyExists(k8sClient.Create(ctx, &corev1.Namespace{Name: ns}))).To(Succeed())
		node := &corev1.Node{Name: "real-node-a"}
		Expect(k8sClient.Create(ctx, node)).To(Succeed())
		node.Status.Capacity = corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4"), corev1.ResourceMemory: resource.MustParse("16Gi")}
		Expect(k8sClient.Status().Update(ctx, node)).To(Succeed())
		workload("app", "real-node-a")
		workload("cache", "real-node-b") // kept running by the workload rules
		rules := &finopsv1.ScalingConfig{Name: "real-shop-rules", Namespace: "default",
			Spec: finopsv1.ScalingConfigSpec{TargetNamespace: ns, Exclusions: []string{"cache"}}}
		Expect(k8sClient.Create(ctx, rules)).To(Succeed())
		g := &finopsv1.ScalingGroup{Name: key.Name, Namespace: key.Namespace,
			Spec: finopsv1.ScalingGroupSpec{Category: "Test", Namespaces: []string{ns}, Schedules: closed}}
		Expect(k8sClient.Create(ctx, g)).To(Succeed())
		DeferCleanup(func() {
			Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, g))).To(Succeed())
			Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, rules))).To(Succeed())
			Expect(k8sClient.DeleteAllOf(ctx, &appsv1.Deployment{}, client.InNamespace(ns))).To(Succeed())
			Expect(k8sClient.DeleteAllOf(ctx, &corev1.Pod{}, client.InNamespace(ns))).To(Succeed())
			Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, node))).To(Succeed())
		})
		r := &ScalingGroupReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), Engine: &scaling.Engine{Client: k8sClient}, Recorder: events.NewFakeRecorder(100)}
		reconcileGroup := func() *finopsv1.ScalingGroup {
			GinkgoHelper()
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
			out := &finopsv1.ScalingGroup{}
			Expect(k8sClient.Get(ctx, key, out)).To(Succeed())
			return out
		}

		By("recording the node of the workload it stops, not the one it keeps")
		g = reconcileGroup()
		Expect(g.Status.ScaleDownNodes).To(Equal([]finopsv1.ScaleDownNode{{Name: "real-node-a", HourlyCost: "0.0000"}}))
		Expect(g.Status.ScaleDownAt).NotTo(BeNil())
		at := g.Status.ScaleDownAt.Time

		By("keeping the first record while the scale-down goes on")
		g = reconcileGroup()
		Expect(g.Status.ScaleDownAt.Time).To(BeTemporally("==", at))

		By("forgetting it once the schedule is up")
		g.Spec.Schedules = open
		Expect(k8sClient.Update(ctx, g)).To(Succeed())
		g = reconcileGroup()
		Expect(g.Status.ScaleDownNodes).To(BeEmpty())
		Expect(g.Status.ScaleDownAt).To(BeNil())

		By("recording nothing for a group that was down before")
		g.Spec.Schedules = closed
		Expect(k8sClient.Update(ctx, g)).To(Succeed())
		g.Status.Phase = scaling.PhaseScaledDown
		Expect(k8sClient.Status().Update(ctx, g)).To(Succeed())
		g = reconcileGroup()
		Expect(g.Status.ScaleDownAt).To(BeNil())
	})
})
