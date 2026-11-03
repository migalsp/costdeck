package controller

import (
	"context"

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

var _ = Describe("ScalingGroup namespaceSelector", func() {
	ctx := context.Background()
	key := types.NamespacedName{Name: "previews", Namespace: "default"}
	closed := []finopsv1.ScalingSchedule{{StartDay: new(0), StartTime: "03:00", EndDay: new(0), EndTime: "03:01", Timezone: "Pacific/Kiritimati"}}

	var reconciler *ScalingGroupReconciler
	reconcileGroup := func() *finopsv1.ScalingGroup {
		GinkgoHelper()
		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
		g := &finopsv1.ScalingGroup{}
		Expect(k8sClient.Get(ctx, key, g)).To(Succeed())
		return g
	}
	replicasOf := func(ns string) int32 {
		GinkgoHelper()
		var d appsv1.Deployment
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "app", Namespace: ns}, &d)).To(Succeed())
		return ptr.Deref(d.Spec.Replicas, 0)
	}
	label := func(ns string, labels map[string]string) {
		GinkgoHelper()
		n := &corev1.Namespace{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: ns}, n)).To(Succeed())
		n.Labels = labels
		Expect(k8sClient.Update(ctx, n)).To(Succeed())
	}

	BeforeEach(func() {
		GinkgoT().Setenv("POD_NAMESPACE", "default")
		reconciler = &ScalingGroupReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), Engine: &scaling.Engine{Client: k8sClient}, Recorder: events.NewFakeRecorder(100)}
		for _, ns := range []string{"sel-pr-1", "sel-pr-2", "sel-prod"} {
			env := "preview"
			if ns == "sel-prod" {
				env = "prod"
			}
			err := k8sClient.Create(ctx, &corev1.Namespace{Name: ns, Labels: map[string]string{"env": env}})
			Expect(client.IgnoreAlreadyExists(err)).To(Succeed())
			label(ns, map[string]string{"env": env})
			labels := map[string]string{"app": "app"}
			Expect(k8sClient.Create(ctx, &appsv1.Deployment{
				Name: "app", Namespace: ns,
				Spec: appsv1.DeploymentSpec{
					Replicas: ptr.To[int32](3),
					Selector: &metav1.LabelSelector{MatchLabels: labels},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{Labels: labels},
						Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "busybox"}}},
					},
				},
			})).To(Succeed())
		}
		Expect(k8sClient.Create(ctx, &finopsv1.ScalingGroup{
			Name: key.Name, Namespace: key.Namespace,
			Spec: finopsv1.ScalingGroupSpec{
				Category: "Previews", Schedules: closed,
				NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"env": "preview"}},
			},
		})).To(Succeed())
	})

	AfterEach(func() {
		Expect(k8sClient.Delete(ctx, &finopsv1.ScalingGroup{Name: key.Name, Namespace: key.Namespace})).To(Succeed())
		for _, ns := range []string{"sel-pr-1", "sel-pr-2", "sel-prod"} {
			Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, &appsv1.Deployment{Name: "app", Namespace: ns}))).To(Succeed())
		}
	})

	It("scales the namespaces it selects, and restores one that stops matching", func() {
		By("selecting the preview namespaces only")
		g := reconcileGroup()
		Expect(g.Status.SelectedNamespaces).To(Equal([]string{"sel-pr-1", "sel-pr-2"}))
		Expect(replicasOf("sel-pr-1")).To(BeZero())
		Expect(replicasOf("sel-pr-2")).To(BeZero())
		Expect(replicasOf("sel-prod")).To(Equal(int32(3)))

		By("mapping a namespace event to the group")
		Expect(reconciler.groupsWithSelectors(ctx, &corev1.Namespace{})).To(ContainElement(reconcile.Request{NamespacedName: key}))

		By("restoring a namespace that opts out")
		label("sel-pr-2", map[string]string{"env": "preview", finopsv1.OptOutLabel: "off"})
		g = reconcileGroup()
		Expect(g.Status.SelectedNamespaces).To(Equal([]string{"sel-pr-1"}))
		Expect(replicasOf("sel-pr-2")).To(Equal(int32(3)), "the group brings back what it scaled down")
		Expect(replicasOf("sel-pr-1")).To(BeZero())
		for k := range g.Status.OriginalReplicas {
			Expect(k).NotTo(HavePrefix("sel-pr-2/"))
		}

		By("leaving a released namespace alone afterwards")
		Expect(k8sClient.Patch(ctx, &appsv1.Deployment{Name: "app", Namespace: "sel-pr-2"},
			client.RawPatch(types.MergePatchType, []byte(`{"spec":{"replicas":1}}`)))).To(Succeed())
		reconcileGroup()
		Expect(replicasOf("sel-pr-2")).To(Equal(int32(1)))
	})

	It("restores everything it selected when the selector is removed", func() {
		reconcileGroup()
		g := &finopsv1.ScalingGroup{}
		Expect(k8sClient.Get(ctx, key, g)).To(Succeed())
		g.Spec.NamespaceSelector = nil
		g.Spec.Namespaces = []string{"sel-prod"}
		Expect(k8sClient.Update(ctx, g)).To(Succeed())
		g = reconcileGroup()
		Expect(g.Status.SelectedNamespaces).To(BeEmpty())
		Expect(replicasOf("sel-pr-1")).To(Equal(int32(3)))
		Expect(replicasOf("sel-pr-2")).To(Equal(int32(3)))
		Expect(replicasOf("sel-prod")).To(BeZero())
	})

	It("brings back every start stage of a namespace that leaves", func() {
		labels := map[string]string{"app": "worker"}
		Expect(k8sClient.Create(ctx, &appsv1.Deployment{
			Name: "worker", Namespace: "sel-pr-1",
			Spec: appsv1.DeploymentSpec{
				Replicas: ptr.To[int32](2), Selector: &metav1.LabelSelector{MatchLabels: labels},
				Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "worker", Image: "busybox"}}}},
			},
		})).To(Succeed())
		// Two stages: worker starts only once app is ready, which no controller reports here.
		rules := &finopsv1.ScalingConfig{Name: "sel-pr-1-rules", Namespace: "default",
			Spec: finopsv1.ScalingConfigSpec{TargetNamespace: "sel-pr-1", Sequence: []string{"app", "worker"}}}
		Expect(k8sClient.Create(ctx, rules)).To(Succeed())
		DeferCleanup(func() {
			Expect(k8sClient.Delete(ctx, rules)).To(Succeed())
			Expect(k8sClient.Delete(ctx, &appsv1.Deployment{Name: "worker", Namespace: "sel-pr-1"})).To(Succeed())
		})
		worker := func() int32 {
			GinkgoHelper()
			var d appsv1.Deployment
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "worker", Namespace: "sel-pr-1"}, &d)).To(Succeed())
			return ptr.Deref(d.Spec.Replicas, 0)
		}
		reconcileGroup()
		reconcileGroup()
		Expect(replicasOf("sel-pr-1")).To(BeZero())
		Expect(worker()).To(BeZero())

		label("sel-pr-1", map[string]string{"env": "preview", finopsv1.OptOutLabel: "off"})
		reconcileGroup()
		Expect(replicasOf("sel-pr-1")).To(Equal(int32(3)))
		Expect(worker()).To(Equal(int32(2)), "nothing schedules the namespace any more to start the later stage")
	})

	It("refuses a selector without labels to match, which would take every namespace", func() {
		g := &finopsv1.ScalingGroup{}
		Expect(k8sClient.Get(ctx, key, g)).To(Succeed())
		g.Spec.NamespaceSelector = &metav1.LabelSelector{}
		Expect(k8sClient.Update(ctx, g)).To(Succeed())
		g = reconcileGroup()
		Expect(g.Status.SelectedNamespaces).To(BeEmpty())
		Expect(replicasOf("sel-prod")).To(Equal(int32(3)))
	})
})
