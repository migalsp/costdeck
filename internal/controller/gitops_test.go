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
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/scaling"
)

var _ = Describe("Pausing GitOps", func() {
	ctx := context.Background()
	ns := "gitops-app"
	key := types.NamespacedName{Name: "gitops", Namespace: "default"}
	closed := []finopsv1.ScalingSchedule{{StartDay: new(0), StartTime: "03:00", EndDay: new(0), EndTime: "03:01", Timezone: "Pacific/Kiritimati"}}
	open := []finopsv1.ScalingSchedule{{Days: []int{0, 1, 2, 3, 4, 5, 6}, StartTime: "00:00", EndTime: "23:59", Timezone: "UTC"}}
	gvk := map[string]schema.GroupVersionKind{
		"Kustomization": {Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Kind: "Kustomization"},
		"HelmRelease":   {Group: "helm.toolkit.fluxcd.io", Version: "v2", Kind: "HelmRelease"},
		"Application":   {Group: "argoproj.io", Version: "v1alpha1", Kind: "Application"},
	}
	gitops := func(kind, name string, body map[string]any) *unstructured.Unstructured {
		u := &unstructured.Unstructured{Object: body}
		u.SetGroupVersionKind(gvk[kind])
		u.SetName(name)
		u.SetNamespace("default")
		return u
	}
	read := func(kind, name string) *unstructured.Unstructured {
		GinkgoHelper()
		u := &unstructured.Unstructured{}
		u.SetGroupVersionKind(gvk[kind])
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: "default", Name: name}, u)).To(Succeed())
		return u
	}
	workload := func(name string, labels, annotations map[string]string) {
		GinkgoHelper()
		sel := map[string]string{"app": name}
		Expect(k8sClient.Create(ctx, &appsv1.Deployment{
			Name: name, Namespace: ns, Labels: labels, Annotations: annotations,
			Spec: appsv1.DeploymentSpec{
				Replicas: new(int32(1)), Selector: &metav1.LabelSelector{MatchLabels: sel},
				Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: sel}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Image: "busybox"}}}},
			},
		})).To(Succeed())
	}
	var objects []client.Object

	BeforeEach(func() {
		Expect(client.IgnoreAlreadyExists(k8sClient.Create(ctx, &corev1.Namespace{Name: ns}))).To(Succeed())
		workload("kustomized", map[string]string{"kustomize.toolkit.fluxcd.io/name": "apps", "kustomize.toolkit.fluxcd.io/namespace": "default"}, nil)
		workload("released", map[string]string{"helm.toolkit.fluxcd.io/name": "web", "helm.toolkit.fluxcd.io/namespace": "default"}, nil)
		workload("argo", nil, map[string]string{"argocd.argoproj.io/tracking-id": "default_shop:apps/Deployment:gitops-app/argo"})
		workload("generated", nil, map[string]string{"argocd.argoproj.io/tracking-id": "default_preview:apps/Deployment:gitops-app/generated"})
		workload("shared", map[string]string{"kustomize.toolkit.fluxcd.io/name": "platform", "kustomize.toolkit.fluxcd.io/namespace": "default"}, nil)
		workload("manual", map[string]string{"kustomize.toolkit.fluxcd.io/name": "held", "kustomize.toolkit.fluxcd.io/namespace": "default"}, nil)
		preview := gitops("Application", "preview", map[string]any{"spec": map[string]any{"syncPolicy": map[string]any{"automated": map[string]any{"selfHeal": true}}}})
		preview.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "argoproj.io/v1alpha1", Kind: "ApplicationSet", Name: "previews", UID: "1"}})
		objects = []client.Object{
			gitops("Kustomization", "apps", map[string]any{"spec": map[string]any{"suspend": false},
				"status": map[string]any{"inventory": map[string]any{"entries": []any{map[string]any{"id": ns + "_kustomized_apps_Deployment"}}}}}),
			gitops("HelmRelease", "web", map[string]any{"spec": map[string]any{}}),
			gitops("Application", "shop", map[string]any{"spec": map[string]any{"syncPolicy": map[string]any{"automated": map[string]any{"selfHeal": true}}}}),
			preview,
			gitops("Kustomization", "platform", map[string]any{"spec": map[string]any{},
				"status": map[string]any{"inventory": map[string]any{"entries": []any{map[string]any{"id": ns + "_shared_apps_Deployment"}, map[string]any{"id": "monitoring_grafana_apps_Deployment"}}}}}),
			gitops("Kustomization", "held", map[string]any{"spec": map[string]any{"suspend": true}}),
		}
		for _, o := range objects {
			Expect(k8sClient.Create(ctx, o)).To(Succeed())
		}
		Expect(k8sClient.Create(ctx, &finopsv1.ScalingGroup{Name: key.Name, Namespace: key.Namespace,
			Spec: finopsv1.ScalingGroupSpec{Category: "Test", Namespaces: []string{ns}, Schedules: closed, PauseGitOps: true}})).To(Succeed())
	})

	AfterEach(func() {
		Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, &finopsv1.ScalingGroup{Name: key.Name, Namespace: key.Namespace}))).To(Succeed())
		for _, o := range objects {
			Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, o))).To(Succeed())
		}
		Expect(k8sClient.DeleteAllOf(ctx, &appsv1.Deployment{}, client.InNamespace(ns))).To(Succeed())
	})

	It("pauses what would bring the workloads back, and resumes only that", func() {
		r := &ScalingGroupReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), Engine: &scaling.Engine{Client: k8sClient}, Recorder: events.NewFakeRecorder(100)}
		reconcileGroup := func() *finopsv1.ScalingGroup {
			GinkgoHelper()
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
			g := &finopsv1.ScalingGroup{}
			Expect(k8sClient.Get(ctx, key, g)).To(Succeed())
			return g
		}
		paused := func(kind, name string) string {
			return read(kind, name).GetAnnotations()[scaling.GitOpsPausedAnnotation]
		}
		boolAt := func(kind, name string, path ...string) bool {
			v, _, _ := unstructured.NestedBool(read(kind, name).Object, path...)
			return v
		}

		By("pausing Flux and Argo CD before scaling down")
		g := reconcileGroup()
		Expect(boolAt("Kustomization", "apps", "spec", "suspend")).To(BeTrue())
		Expect(paused("Kustomization", "apps")).To(Equal("suspend"))
		Expect(boolAt("HelmRelease", "web", "spec", "suspend")).To(BeTrue())
		Expect(boolAt("Application", "shop", "spec", "syncPolicy", "automated", "selfHeal")).To(BeFalse())
		Expect(paused("Application", "shop")).To(Equal("self-heal"))

		By("leaving alone what it must not pause")
		Expect(boolAt("Application", "preview", "spec", "syncPolicy", "automated", "selfHeal")).To(BeTrue(), "an ApplicationSet would undo it")
		Expect(boolAt("Kustomization", "platform", "spec", "suspend")).To(BeFalse(), "it also applies to monitoring")
		Expect(paused("Kustomization", "held")).To(BeEmpty(), "someone else suspended it")

		cond := meta.FindStatusCondition(g.Status.Conditions, ConditionGitOps)
		Expect(cond).NotTo(BeNil())
		Expect(cond.Reason).To(Equal("PartlyPaused"))
		Expect(cond.Message).To(ContainSubstring("ApplicationSet previews"))
		Expect(cond.Message).To(ContainSubstring("also applies to monitoring"))

		By("writing replicas under CostDeck's field manager")
		var d appsv1.Deployment
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: "kustomized"}, &d)).To(Succeed())
		managers := make([]string, 0, len(d.ManagedFields))
		for _, f := range d.ManagedFields {
			managers = append(managers, f.Manager)
		}
		Expect(managers).To(ContainElement(scaling.FieldManager))

		By("resuming what it paused once everything is up")
		g.Spec.Schedules = open
		Expect(k8sClient.Update(ctx, g)).To(Succeed())
		reconcileGroup()
		Expect(boolAt("Kustomization", "apps", "spec", "suspend")).To(BeTrue(), "resumed only once everything is up")
		// envtest runs no controllers: report the scaled-up workloads ready by hand.
		var list appsv1.DeploymentList
		Expect(k8sClient.List(ctx, &list, client.InNamespace(ns))).To(Succeed())
		for i := range list.Items {
			d := &list.Items[i]
			d.Status.Replicas, d.Status.ReadyReplicas = *d.Spec.Replicas, *d.Spec.Replicas
			Expect(k8sClient.Status().Update(ctx, d)).To(Succeed())
		}
		reconcileGroup()
		Expect(boolAt("Kustomization", "apps", "spec", "suspend")).To(BeFalse())
		Expect(paused("Kustomization", "apps")).To(BeEmpty())
		Expect(boolAt("Application", "shop", "spec", "syncPolicy", "automated", "selfHeal")).To(BeTrue())
		Expect(boolAt("Kustomization", "held", "spec", "suspend")).To(BeTrue(), "still the owner's to resume")
	})

	It("only reports the GitOps tools when the schedule does not pause them", func() {
		g := &finopsv1.ScalingGroup{}
		Expect(k8sClient.Get(ctx, key, g)).To(Succeed())
		g.Spec.PauseGitOps = false
		Expect(k8sClient.Update(ctx, g)).To(Succeed())
		r := &ScalingGroupReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), Engine: &scaling.Engine{Client: k8sClient}, Recorder: events.NewFakeRecorder(100)}
		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Get(ctx, key, g)).To(Succeed())
		cond := meta.FindStatusCondition(g.Status.Conditions, ConditionGitOps)
		Expect(cond.Reason).To(Equal("NotPaused"))
		Expect(cond.Message).To(ContainSubstring("Argo CD application default/shop"))
		Expect(cond.Message).To(ContainSubstring("managedFieldsManagers [costdeck]"))
		Expect(read("Kustomization", "apps").GetAnnotations()[scaling.GitOpsPausedAnnotation]).To(BeEmpty())
	})
})
