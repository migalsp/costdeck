package controller

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/scaling"
)

var _ = Describe("Sleep page", func() {
	ctx := context.Background()
	ns, key := "sleep-shop", types.NamespacedName{Name: "sleep-shop", Namespace: "default"}
	closed := []finopsv1.ScalingSchedule{{StartDay: new(0), StartTime: "03:00", EndDay: new(0), EndTime: "03:01", Timezone: "Pacific/Kiritimati"}}
	open := []finopsv1.ScalingSchedule{{Days: []int{0, 1, 2, 3, 4, 5, 6}, StartTime: "00:00", EndTime: "23:59", Timezone: "UTC"}}
	routeGVK := schema.GroupVersionKind{Group: "gateway.networking.k8s.io", Version: "v1", Kind: "HTTPRoute"}
	grantGVK := schema.GroupVersionKind{Group: "gateway.networking.k8s.io", Version: "v1", Kind: "ReferenceGrant"}

	It("shows the sleep page once the namespace is down, and the application once it is up", func() {
		GinkgoT().Setenv("POD_NAMESPACE", "default")
		Expect(client.IgnoreAlreadyExists(k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}))).To(Succeed())
		original := []any{map[string]any{"backendRefs": []any{map[string]any{"name": "web", "port": int64(80)}}}}
		route := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{"hostnames": []any{"shop.example.com"}, "rules": original}}}
		route.SetGroupVersionKind(routeGVK)
		route.SetNamespace(ns)
		route.SetName("shop")
		Expect(k8sClient.Create(ctx, route)).To(Succeed())
		g := &finopsv1.ScalingGroup{ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace},
			Spec: finopsv1.ScalingGroupSpec{Category: "Test", Namespaces: []string{ns}, Schedules: closed, SleepPage: true}}
		Expect(k8sClient.Create(ctx, g)).To(Succeed())
		DeferCleanup(func() {
			Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, g))).To(Succeed())
			Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, route))).To(Succeed())
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
		read := func() *unstructured.Unstructured {
			GinkgoHelper()
			out := &unstructured.Unstructured{}
			out.SetGroupVersionKind(routeGVK)
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(route), out)).To(Succeed())
			return out
		}
		grantExists := func() bool {
			grant := &unstructured.Unstructured{}
			grant.SetGroupVersionKind(grantGVK)
			return k8sClient.Get(ctx, types.NamespacedName{Namespace: "default", Name: scaling.SleepGrantName}, grant) == nil
		}

		By("pointing the route at the sleep page once the namespace is down")
		g = reconcileGroup()
		asleep := read()
		Expect(asleep.GetAnnotations()).To(HaveKey(scaling.SleepingAnnotation))
		refs, _, _ := unstructured.NestedSlice(asleep.Object, "spec", "rules")
		backend := refs[0].(map[string]any)["backendRefs"].([]any)[0].(map[string]any)
		Expect(backend["name"]).To(Equal("costdeck-operator-api"))
		Expect(backend["namespace"]).To(Equal("default"))
		Expect(grantExists()).To(BeTrue())
		cond := meta.FindStatusCondition(g.Status.Conditions, ConditionSleepPage)
		Expect(cond).NotTo(BeNil())
		Expect(cond.Reason).To(Equal("Asleep"))
		Expect(cond.Message).To(ContainSubstring("1 HTTPRoute(s) in sleep-shop"))

		By("putting the route back once the namespace is up")
		g.Spec.Schedules = open
		Expect(k8sClient.Update(ctx, g)).To(Succeed())
		g = reconcileGroup()
		woken := read()
		Expect(woken.GetAnnotations()).NotTo(HaveKey(scaling.SleepingAnnotation))
		rules, _, _ := unstructured.NestedSlice(woken.Object, "spec", "rules")
		Expect(rules).To(Equal(original))
		Expect(grantExists()).To(BeFalse())
		Expect(meta.FindStatusCondition(g.Status.Conditions, ConditionSleepPage).Reason).To(Equal("Awake"))

		By("dropping the condition when the option is switched off")
		g.Spec.SleepPage = false
		Expect(k8sClient.Update(ctx, g)).To(Succeed())
		g = reconcileGroup()
		Expect(meta.FindStatusCondition(g.Status.Conditions, ConditionSleepPage)).To(BeNil())
	})
})
