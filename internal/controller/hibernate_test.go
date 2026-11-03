package controller

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/scaling"
)

var _ = Describe("Hibernate rules", func() {
	ctx := context.Background()
	closed := []finopsv1.ScalingSchedule{{StartDay: new(0), StartTime: "03:00", EndDay: new(0), EndTime: "03:01", Timezone: "Pacific/Kiritimati"}}
	open := []finopsv1.ScalingSchedule{{Days: []int{0, 1, 2, 3, 4, 5, 6}, StartTime: "00:00", EndTime: "23:59", Timezone: "UTC"}}
	cnpg := schema.GroupVersionKind{Group: "postgresql.cnpg.io", Version: "v1", Kind: "Cluster"}
	zalando := schema.GroupVersionKind{Group: "acid.zalan.do", Version: "v1", Kind: "postgresql"}

	namespace := func(name string, labels map[string]string) {
		GinkgoHelper()
		Expect(client.IgnoreAlreadyExists(k8sClient.Create(ctx, &corev1.Namespace{Name: name, Labels: labels}))).To(Succeed())
	}
	deployment := func(ns, name string, replicas int32, owner *metav1.OwnerReference) {
		GinkgoHelper()
		sel := map[string]string{"app": name}
		d := &appsv1.Deployment{
			Name: name, Namespace: ns,
			Spec: appsv1.DeploymentSpec{
				Replicas: new(replicas), Selector: &metav1.LabelSelector{MatchLabels: sel},
				Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: sel}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Image: "busybox"}}}},
			},
		}
		if owner != nil {
			d.OwnerReferences = []metav1.OwnerReference{*owner}
		}
		Expect(k8sClient.Create(ctx, d)).To(Succeed())
	}
	replicasOf := func(ns, name string) int32 {
		GinkgoHelper()
		var d appsv1.Deployment
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &d)).To(Succeed())
		return ptr.Deref(d.Spec.Replicas, 0)
	}
	// envtest runs no controllers: report every workload in a namespace as having its
	// replicas ready, as the Deployment controller would.
	settle := func(ns string) {
		GinkgoHelper()
		var list appsv1.DeploymentList
		Expect(k8sClient.List(ctx, &list, client.InNamespace(ns))).To(Succeed())
		for i := range list.Items {
			d := &list.Items[i]
			d.Status.Replicas, d.Status.ReadyReplicas, d.Status.AvailableReplicas = *d.Spec.Replicas, *d.Spec.Replicas, *d.Spec.Replicas
			Expect(k8sClient.Status().Update(ctx, d)).To(Succeed())
		}
	}
	service := func(gvk schema.GroupVersionKind, ns, name string, spec map[string]any) *unstructured.Unstructured {
		GinkgoHelper()
		u := &unstructured.Unstructured{Object: map[string]any{"spec": spec}}
		u.SetGroupVersionKind(gvk)
		u.SetNamespace(ns)
		u.SetName(name)
		Expect(k8sClient.Create(ctx, u)).To(Succeed())
		return u
	}
	read := func(u *unstructured.Unstructured) *unstructured.Unstructured {
		GinkgoHelper()
		out := &unstructured.Unstructured{}
		out.SetGroupVersionKind(u.GroupVersionKind())
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(u), out)).To(Succeed())
		return out
	}
	// playOperator sets a resource's status, as its operator would.
	playOperator := func(u *unstructured.Unstructured, status map[string]any) {
		GinkgoHelper()
		cur := read(u)
		cur.Object["status"] = status
		Expect(k8sClient.Update(ctx, cur)).To(Succeed())
	}
	hibernated := func(u *unstructured.Unstructured) string { return read(u).GetAnnotations()["cnpg.io/hibernation"] }
	condition := func(t string, s string) map[string]any { return map[string]any{"type": t, "status": s} }

	It("stops a database after the workloads and starts it before them", func() {
		ns, key := "hib-shop", types.NamespacedName{Name: "hib-shop", Namespace: "default"}
		namespace(ns, nil)
		deployment(ns, "app", 2, nil)
		db := service(cnpg, ns, "db", map[string]any{"instances": int64(1)})
		// A Deployment CloudNativePG runs for the cluster is the operator's to scale.
		deployment(ns, "db-pooler", 1, &metav1.OwnerReference{APIVersion: "postgresql.cnpg.io/v1", Kind: "Cluster", Name: "db", UID: read(db).GetUID(), Controller: new(true)})
		settle(ns)
		g := &finopsv1.ScalingGroup{Name: key.Name, Namespace: key.Namespace,
			Spec: finopsv1.ScalingGroupSpec{Category: "Test", Namespaces: []string{ns}, Schedules: closed, Hibernate: []finopsv1.HibernateRule{{Preset: "cnpg"}}}}
		Expect(k8sClient.Create(ctx, g)).To(Succeed())
		DeferCleanup(func() {
			Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, g))).To(Succeed())
			Expect(k8sClient.DeleteAllOf(ctx, &appsv1.Deployment{}, client.InNamespace(ns))).To(Succeed())
			Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, db))).To(Succeed())
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

		By("scaling the workloads down first, leaving the operator's own alone")
		reconcileGroup()
		Expect(replicasOf(ns, "app")).To(BeZero())
		Expect(replicasOf(ns, "db-pooler")).To(Equal(int32(1)))
		Expect(hibernated(db)).To(BeEmpty(), "the database waits until the workloads are down")

		By("hibernating the database once they are down, and waiting for it")
		settle(ns)
		g = reconcileGroup()
		Expect(hibernated(db)).To(Equal("on"))
		Expect(read(db).GetAnnotations()[scaling.HibernatedAnnotation]).To(Equal("stopped"))
		Expect(g.Status.Phase).NotTo(Equal(scaling.PhaseScaledDown))
		playOperator(db, map[string]any{"readyInstances": int64(0), "conditions": []any{condition("cnpg.io/hibernation", "True")}})
		g = reconcileGroup()
		Expect(g.Status.Phase).To(Equal(scaling.PhaseScaledDown))

		By("waking the database first when the schedule opens")
		g.Spec.Schedules = open
		Expect(k8sClient.Update(ctx, g)).To(Succeed())
		reconcileGroup()
		Expect(hibernated(db)).To(BeEmpty())
		Expect(replicasOf(ns, "app")).To(BeZero(), "the workloads wait for the database")
		playOperator(db, map[string]any{"readyInstances": int64(1), "conditions": []any{condition("Ready", "True")}})
		reconcileGroup()
		Expect(replicasOf(ns, "app")).To(Equal(int32(2)))
		Expect(read(db).GetAnnotations()).NotTo(HaveKey(scaling.HibernatedAnnotation))
	})

	It("counts services in a dry run and stops them with a namespace schedule", func() {
		ns, key := "hib-billing", types.NamespacedName{Name: "hib-billing", Namespace: "default"}
		namespace(ns, nil)
		deployment(ns, "api", 1, nil)
		pg := service(zalando, ns, "pg", map[string]any{"numberOfInstances": int64(2)})
		settle(ns)
		cfg := &finopsv1.ScalingConfig{Name: key.Name, Namespace: key.Namespace,
			Spec: finopsv1.ScalingConfigSpec{TargetNamespace: ns, Schedules: closed, DryRun: true, Hibernate: []finopsv1.HibernateRule{{Preset: "zalando-postgres"}}}}
		Expect(k8sClient.Create(ctx, cfg)).To(Succeed())
		DeferCleanup(func() {
			Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, cfg))).To(Succeed())
			Expect(k8sClient.DeleteAllOf(ctx, &appsv1.Deployment{}, client.InNamespace(ns))).To(Succeed())
			Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, pg))).To(Succeed())
		})
		r := &ScalingConfigReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), Engine: &scaling.Engine{Client: k8sClient}}
		reconcileConfig := func() *finopsv1.ScalingConfig {
			GinkgoHelper()
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
			out := &finopsv1.ScalingConfig{}
			Expect(k8sClient.Get(ctx, key, out)).To(Succeed())
			return out
		}
		instances := func() int64 {
			n, _, _ := unstructured.NestedInt64(read(pg).Object, "spec", "numberOfInstances")
			return n
		}

		By("counting the cluster with the workloads in a dry run, changing nothing")
		cfg = reconcileConfig()
		cond := meta.FindStatusCondition(cfg.Status.Conditions, ConditionDryRun)
		Expect(cond).NotTo(BeNil())
		Expect(cond.Message).To(HavePrefix("Would scale down 2 workload(s)"))
		Expect(instances()).To(Equal(int64(2)))

		By("stopping the cluster after the workloads once the dry run is off")
		cfg.Spec.DryRun = false
		Expect(k8sClient.Update(ctx, cfg)).To(Succeed())
		reconcileConfig()
		settle(ns)
		reconcileConfig()
		Expect(replicasOf(ns, "api")).To(BeZero())
		Expect(instances()).To(BeZero())
		Expect(read(pg).GetAnnotations()[scaling.OriginalReplicasAnnotation]).To(Equal("2"))
		// No StatefulSet exists for the cluster in envtest: nothing runs, so it is down.
		Expect(reconcileConfig().Status.Phase).To(Equal(scaling.PhaseScaledDown))
	})

	It("brings a namespace that leaves a group fully back, services included", func() {
		ns, key := "hib-preview", types.NamespacedName{Name: "hib-previews", Namespace: "default"}
		GinkgoT().Setenv("POD_NAMESPACE", "default")
		namespace(ns, map[string]string{"env": "hib-preview"})
		deployment(ns, "first", 1, nil)
		deployment(ns, "second", 1, nil)
		db := service(cnpg, ns, "db", map[string]any{"instances": int64(1)})
		settle(ns)
		// Two stages: "second" only starts once "first" is ready.
		rules := &finopsv1.ScalingConfig{Name: "hib-preview-rules", Namespace: "default",
			Spec: finopsv1.ScalingConfigSpec{TargetNamespace: ns, Sequence: []string{"first", "second"}}}
		Expect(k8sClient.Create(ctx, rules)).To(Succeed())
		g := &finopsv1.ScalingGroup{Name: key.Name, Namespace: key.Namespace,
			Spec: finopsv1.ScalingGroupSpec{Category: "Test", Schedules: closed, Hibernate: []finopsv1.HibernateRule{{Preset: "cnpg"}},
				NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"env": "hib-preview"}}}}
		Expect(k8sClient.Create(ctx, g)).To(Succeed())
		DeferCleanup(func() {
			Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, g))).To(Succeed())
			Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, rules))).To(Succeed())
			Expect(k8sClient.DeleteAllOf(ctx, &appsv1.Deployment{}, client.InNamespace(ns))).To(Succeed())
			Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, db))).To(Succeed())
		})
		r := &ScalingGroupReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), Engine: &scaling.Engine{Client: k8sClient}, Recorder: events.NewFakeRecorder(100)}
		reconcileGroup := func() {
			GinkgoHelper()
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
		}

		By("scaling everything down, the database last")
		for range 4 {
			reconcileGroup()
			settle(ns)
		}
		Expect(replicasOf(ns, "first")).To(BeZero())
		Expect(replicasOf(ns, "second")).To(BeZero())
		Expect(hibernated(db)).To(Equal("on"))

		By("restoring every stage and the database at once when the namespace leaves")
		n := &corev1.Namespace{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: ns}, n)).To(Succeed())
		n.Labels = nil
		Expect(k8sClient.Update(ctx, n)).To(Succeed())
		reconcileGroup()
		Expect(replicasOf(ns, "first")).To(Equal(int32(1)))
		Expect(replicasOf(ns, "second")).To(Equal(int32(1)), "nothing schedules the namespace any more to start later stages")
		Expect(hibernated(db)).To(BeEmpty())
		Expect(read(db).GetAnnotations()).NotTo(HaveKey(scaling.HibernatedAnnotation))
	})
})
