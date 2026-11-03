package scaling

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestGitOpsOwnerOf(t *testing.T) {
	obj := func(labels, annotations map[string]string) *metav1.ObjectMeta {
		return &metav1.ObjectMeta{Labels: labels, Annotations: annotations}
	}
	cases := []struct {
		name string
		obj  *metav1.ObjectMeta
		want *GitOpsOwner
		text string
	}{
		{"nothing", obj(nil, nil), nil, ""},
		{"a Flux Kustomization", obj(map[string]string{fluxKustomizationName: "apps", fluxKustomizationNS: "flux-system"}, nil),
			&GitOpsOwner{Kind: "Kustomization", Name: "apps", Namespace: "flux-system"}, "Flux Kustomization flux-system/apps"},
		{"a Flux HelmRelease", obj(map[string]string{fluxHelmReleaseName: "web", fluxHelmReleaseNS: "shop"}, nil),
			&GitOpsOwner{Kind: "HelmRelease", Name: "web", Namespace: "shop"}, "Flux HelmRelease shop/web"},
		{"an Argo CD application in its own namespace", obj(nil, map[string]string{argoTrackingAnnotation: "shop:apps/Deployment:shop-dev/api"}),
			&GitOpsOwner{Kind: "Application", Name: "shop"}, "Argo CD application shop"},
		{"an Argo CD application elsewhere", obj(nil, map[string]string{argoTrackingAnnotation: "team-a_shop:/Service:shop-dev/api"}),
			&GitOpsOwner{Kind: "Application", Name: "shop", Namespace: "team-a"}, "Argo CD application team-a/shop"},
		// Helm sets app.kubernetes.io/instance too, so the label alone says nothing about Argo CD.
		{"a Helm release label is not Argo CD", obj(map[string]string{"app.kubernetes.io/instance": "shop"}, nil), nil, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := GitOpsOwnerOf(c.obj)
			if (got == nil) != (c.want == nil) || (got != nil && (*got != *c.want || got.String() != c.text)) {
				t.Errorf("got %+v (%v), want %+v (%s)", got, got, c.want, c.text)
			}
		})
	}
}

func TestInventoryOutside(t *testing.T) {
	k := &unstructured.Unstructured{Object: map[string]any{"status": map[string]any{"inventory": map[string]any{"entries": []any{
		map[string]any{"id": "shop-dev_api_apps_Deployment"},
		map[string]any{"id": "shop-qa_api_apps_Deployment"},
		map[string]any{"id": "monitoring_grafana_apps_Deployment"},
		map[string]any{"id": "monitoring_grafana__Service"},
		map[string]any{"id": "_shop-dev__Namespace"}, // cluster-scoped
	}}}}}
	if got := inventoryOutside(k, []string{"shop-dev", "shop-qa"}); !slices.Equal(got, []string{"monitoring"}) {
		t.Errorf("outside = %v", got)
	}
	if got := inventoryOutside(&unstructured.Unstructured{Object: map[string]any{}}, nil); len(got) != 0 {
		t.Errorf("no inventory: %v", got)
	}
}

func gitOpsResource(gvk schema.GroupVersionKind, ns, name string, body map[string]any) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: body}
	u.SetGroupVersionKind(gvk)
	u.SetNamespace(ns)
	u.SetName(name)
	return u
}

func managedDeployment(name string, labels, annotations map[string]string) *appsv1.Deployment {
	return &appsv1.Deployment{Name: name, Namespace: "shop-dev", Labels: labels, Annotations: annotations}
}

func byKustomization(name string) map[string]string {
	return map[string]string{fluxKustomizationName: name, fluxKustomizationNS: "flux-system"}
}

func byArgoApp(instance string) map[string]string {
	return map[string]string{argoTrackingAnnotation: instance + ":apps/Deployment:shop-dev/x"}
}

func inventoryOf(ids ...string) map[string]any {
	entries := make([]any, 0, len(ids))
	for _, id := range ids {
		entries = append(entries, map[string]any{"id": id})
	}
	return map[string]any{"inventory": map[string]any{"entries": entries}}
}

func argoResource(group, kind, ns, name string) map[string]any {
	return map[string]any{"group": group, "kind": kind, "namespace": ns, "name": name}
}

func listing(resources ...map[string]any) []any {
	out := make([]any, 0, len(resources))
	for _, r := range resources {
		out = append(out, r)
	}
	return out
}

func gitOpsEngine(t *testing.T, funcs *interceptor.Funcs, objs ...client.Object) *Engine {
	t.Helper()
	return hibernateEngine(t, []schema.GroupVersionKind{kustomizationGVK, helmReleaseGVK, applicationGVK}, funcs, objs...)
}

func TestGitOpsOwnersListsEachManagerOnceInOrder(t *testing.T) {
	cache := &appsv1.StatefulSet{Name: "cache", Namespace: "shop-dev", Annotations: byArgoApp("shop")}
	e := gitOpsEngine(t, nil,
		managedDeployment("api", byKustomization("apps"), nil),
		managedDeployment("worker", byKustomization("apps"), nil),
		managedDeployment("web", map[string]string{fluxHelmReleaseName: "web", fluxHelmReleaseNS: "shop-dev"}, nil),
		managedDeployment("batch", byKustomization("batch"), nil),
		managedDeployment("plain", nil, nil),
		cache,
	)
	got, err := e.GitOpsOwners(context.Background(), "shop-dev", []string{"batch"})
	if err != nil {
		t.Fatal(err)
	}
	want := []GitOpsOwner{
		{Kind: "Application", Name: "shop"},
		{Kind: "HelmRelease", Name: "web", Namespace: "shop-dev"},
		{Kind: "Kustomization", Name: "apps", Namespace: "flux-system"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("GitOpsOwners = %+v, want %+v (apps once, the excluded batch's owner left out)", got, want)
	}
}

func TestPauseAndResumeGitOpsRoundTrip(t *testing.T) {
	ctx := context.Background()
	apps := gitOpsResource(kustomizationGVK, "flux-system", "apps", map[string]any{
		"spec":   map[string]any{"interval": "10m", "path": "./shop"},
		"status": inventoryOf("shop-dev_api_apps_Deployment", "shop-dev_api__Service", "_shop-dev__Namespace"),
	})
	web := gitOpsResource(helmReleaseGVK, "shop-dev", "web", map[string]any{"spec": map[string]any{"interval": "5m"}})
	shop := gitOpsResource(applicationGVK, "argocd", "shop", map[string]any{
		"spec": map[string]any{
			"project": "default", "destination": map[string]any{"namespace": "shop-dev"},
			"syncPolicy": map[string]any{"automated": map[string]any{"prune": true, "selfHeal": true}},
		},
		"status": map[string]any{"resources": listing(argoResource("apps", "Deployment", "shop-dev", "ui"), argoResource("", "Service", "shop-dev", "ui"))},
	})
	e := gitOpsEngine(t, nil, apps, web, shop,
		managedDeployment("api", byKustomization("apps"), nil),
		managedDeployment("web", map[string]string{fluxHelmReleaseName: "web", fluxHelmReleaseNS: "shop-dev"}, nil),
		managedDeployment("ui", nil, byArgoApp("shop")),
	)
	field := func(u *unstructured.Unstructured, path ...string) any {
		v, _, _ := unstructured.NestedFieldNoCopy(reread(t, e, u).Object, path...)
		return v
	}
	marker := func(u *unstructured.Unstructured) string {
		return reread(t, e, u).GetAnnotations()[GitOpsPausedAnnotation]
	}

	notes, err := e.PauseGitOps(ctx, "shop-dev", nil, []string{"shop-dev"})
	if err != nil || len(notes) != 0 {
		t.Fatalf("PauseGitOps = %v, %v; want everything paused without notes", notes, err)
	}
	if field(apps, "spec", "suspend") != true || marker(apps) != "suspend" || field(apps, "spec", "path") != "./shop" {
		t.Errorf("Kustomization after pause: suspend=%v marker=%q path=%v", field(apps, "spec", "suspend"), marker(apps), field(apps, "spec", "path"))
	}
	if field(web, "spec", "suspend") != true || marker(web) != "suspend" {
		t.Errorf("HelmRelease after pause: suspend=%v marker=%q", field(web, "spec", "suspend"), marker(web))
	}
	if field(shop, "spec", "syncPolicy", "automated", "selfHeal") != false || field(shop, "spec", "syncPolicy", "automated", "prune") != true || marker(shop) != "self-heal" {
		t.Errorf("Application after pause: selfHeal=%v prune=%v marker=%q", field(shop, "spec", "syncPolicy", "automated", "selfHeal"),
			field(shop, "spec", "syncPolicy", "automated", "prune"), marker(shop))
	}

	versions := []string{reread(t, e, apps).GetResourceVersion(), reread(t, e, web).GetResourceVersion(), reread(t, e, shop).GetResourceVersion()}
	if notes, err := e.PauseGitOps(ctx, "shop-dev", nil, []string{"shop-dev"}); err != nil || len(notes) != 0 {
		t.Fatalf("second PauseGitOps = %v, %v", notes, err)
	}
	if again := []string{reread(t, e, apps).GetResourceVersion(), reread(t, e, web).GetResourceVersion(), reread(t, e, shop).GetResourceVersion()}; !slices.Equal(again, versions) {
		t.Errorf("pausing what is already paused wrote it again: %v -> %v", versions, again)
	}

	if err := e.ResumeGitOps(ctx, "shop-dev", nil); err != nil {
		t.Fatal(err)
	}
	for _, u := range []*unstructured.Unstructured{apps, web} {
		if field(u, "spec", "suspend") != false || marker(u) != "" {
			t.Errorf("%s after resume: suspend=%v marker=%q", u.GetKind(), field(u, "spec", "suspend"), marker(u))
		}
	}
	if field(shop, "spec", "syncPolicy", "automated", "selfHeal") != true || field(shop, "spec", "syncPolicy", "automated", "prune") != true || marker(shop) != "" {
		t.Errorf("Application after resume: selfHeal=%v prune=%v marker=%q", field(shop, "spec", "syncPolicy", "automated", "selfHeal"),
			field(shop, "spec", "syncPolicy", "automated", "prune"), marker(shop))
	}
}

func TestPauseGitOpsLeavesWhatItMustNotPause(t *testing.T) {
	ctx := context.Background()
	shared := gitOpsResource(kustomizationGVK, "flux-system", "shared", map[string]any{
		"spec":   map[string]any{"interval": "10m"},
		"status": inventoryOf("shop-dev_a1_apps_Deployment", "shop-qa_x_apps_Deployment", "monitoring_x_apps_Deployment", "monitoring_y__Service"),
	})
	manual := gitOpsResource(kustomizationGVK, "flux-system", "manual", map[string]any{"spec": map[string]any{"suspend": true}})
	fromSet := gitOpsResource(applicationGVK, "argocd", "from-set", map[string]any{
		"spec":   map[string]any{"syncPolicy": map[string]any{"automated": map[string]any{"selfHeal": true}}},
		"status": map[string]any{"resources": listing(argoResource("apps", "Deployment", "shop-dev", "a3"))},
	})
	fromSet.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "argoproj.io/v1alpha1", Kind: "ApplicationSet", Name: "team-apps", UID: "u1"}})
	manualSync := gitOpsResource(applicationGVK, "argocd", "manual-sync", map[string]any{"spec": map[string]any{"project": "default"}})
	e := gitOpsEngine(t, nil, shared, manual, fromSet, manualSync,
		managedDeployment("a1", byKustomization("shared"), nil),
		managedDeployment("a2", byKustomization("manual"), nil),
		managedDeployment("a3", nil, byArgoApp("from-set")),
		managedDeployment("a4", nil, byArgoApp("manual-sync")),
		managedDeployment("a5", byKustomization("gone"), nil),
		managedDeployment("a6", nil, byArgoApp("team-a_orphan")),
	)
	versions := func() []string {
		watched := []*unstructured.Unstructured{shared, manual, fromSet, manualSync}
		out := make([]string, 0, len(watched))
		for _, u := range watched {
			out = append(out, reread(t, e, u).GetResourceVersion())
		}
		return out
	}
	before := versions()

	notes, err := e.PauseGitOps(ctx, "shop-dev", nil, []string{"shop-dev", "shop-qa"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"Argo CD application from-set comes from the ApplicationSet team-apps, which would undo a pause; ignore the replicas in Argo CD instead",
		"Argo CD application team-a/orphan: not found",
		"Flux Kustomization flux-system/gone: not found",
		"Flux Kustomization flux-system/shared also applies to monitoring, so it is left running",
	}
	if !slices.Equal(notes, want) {
		t.Errorf("notes =\n%s\nwant\n%s", strings.Join(notes, "\n"), strings.Join(want, "\n"))
	}
	if after := versions(); !slices.Equal(after, before) {
		t.Errorf("objects that must be left alone were written: %v -> %v", before, after)
	}

	if err := e.ResumeGitOps(ctx, "shop-dev", nil); err != nil {
		t.Fatalf("ResumeGitOps with missing objects = %v, want nothing to resume", err)
	}
	if suspended, _, _ := unstructured.NestedBool(reread(t, e, manual).Object, "spec", "suspend"); !suspended {
		t.Error("a Kustomization someone else suspended was resumed")
	}
	if after := versions(); !slices.Equal(after, before) {
		t.Errorf("ResumeGitOps wrote objects CostDeck never paused: %v -> %v", before, after)
	}
}

func TestPauseGitOpsExplainsWhyItCouldNotPause(t *testing.T) {
	apps := gitOpsResource(kustomizationGVK, "flux-system", "apps", map[string]any{"spec": map[string]any{}, "status": inventoryOf("shop-dev_api_apps_Deployment")})
	web := gitOpsResource(helmReleaseGVK, "shop-dev", "web", map[string]any{"spec": map[string]any{}})
	shop := gitOpsResource(applicationGVK, "argocd", "shop", map[string]any{"spec": map[string]any{
		"syncPolicy": map[string]any{"automated": map[string]any{"selfHeal": true}},
	}})
	funcs := &interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if u, ok := obj.(*unstructured.Unstructured); ok && u.GetKind() == helmReleaseGVK.Kind {
				return errors.New("etcd request timed out")
			}
			return c.Get(ctx, key, obj, opts...)
		},
		List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			if u, ok := list.(*unstructured.UnstructuredList); ok && u.GetKind() == "ApplicationList" {
				return errors.New("list applications refused")
			}
			return c.List(ctx, list, opts...)
		},
		Patch: func(_ context.Context, _ client.WithWatch, obj client.Object, _ client.Patch, _ ...client.PatchOption) error {
			return apierrors.NewForbidden(schema.GroupResource{Group: kustomizationGVK.Group, Resource: "kustomizations"}, obj.GetName(), errors.New("RBAC"))
		},
	}
	e := gitOpsEngine(t, funcs, apps, web, shop,
		managedDeployment("api", byKustomization("apps"), nil),
		managedDeployment("web", map[string]string{fluxHelmReleaseName: "web", fluxHelmReleaseNS: "shop-dev"}, nil),
		managedDeployment("ui", nil, byArgoApp("shop")),
	)

	notes, err := e.PauseGitOps(context.Background(), "shop-dev", nil, []string{"shop-dev"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"Argo CD application shop: list applications refused",
		"Flux HelmRelease shop-dev/web: etcd request timed out",
		"Flux Kustomization flux-system/apps: no permission to pause it; enable gitops.pause in the Helm chart",
	}
	if !slices.Equal(notes, want) {
		t.Errorf("notes =\n%s\nwant\n%s", strings.Join(notes, "\n"), strings.Join(want, "\n"))
	}
}

func TestResumeGitOpsReportsAFailedResume(t *testing.T) {
	apps := gitOpsResource(kustomizationGVK, "flux-system", "apps", map[string]any{"spec": map[string]any{"suspend": true}})
	apps.SetAnnotations(map[string]string{GitOpsPausedAnnotation: pausedSuspend})
	funcs := &interceptor.Funcs{Patch: func(context.Context, client.WithWatch, client.Object, client.Patch, ...client.PatchOption) error {
		return errors.New("admission webhook denied the request")
	}}
	e := gitOpsEngine(t, funcs, apps, managedDeployment("api", byKustomization("apps"), nil))

	err := e.ResumeGitOps(context.Background(), "shop-dev", nil)
	if err == nil || err.Error() != "resume Flux Kustomization flux-system/apps: admission webhook denied the request" {
		t.Errorf("ResumeGitOps = %v", err)
	}
}

func TestGitOpsWithoutTheToolInstalled(t *testing.T) {
	e := hibernateEngine(t, nil, nil, managedDeployment("api", byKustomization("apps"), nil), managedDeployment("ui", nil, byArgoApp("shop")))
	notes, err := e.PauseGitOps(context.Background(), "shop-dev", nil, []string{"shop-dev"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Argo CD application shop: not found", "Flux Kustomization flux-system/apps: not found"}
	if !slices.Equal(notes, want) {
		t.Errorf("notes = %q, want %q", notes, want)
	}
	if err := e.ResumeGitOps(context.Background(), "shop-dev", nil); err != nil {
		t.Errorf("ResumeGitOps without Flux or Argo CD = %v", err)
	}
}

func TestGitOpsReturnsWorkloadListingErrors(t *testing.T) {
	funcs := &interceptor.Funcs{List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error {
		return errors.New("apiserver unavailable")
	}}
	e := gitOpsEngine(t, funcs)
	if owners, err := e.GitOpsOwners(context.Background(), "shop-dev", nil); err == nil || owners != nil {
		t.Errorf("GitOpsOwners = %v, %v", owners, err)
	}
	if notes, err := e.PauseGitOps(context.Background(), "shop-dev", nil, nil); err == nil || err.Error() != "apiserver unavailable" || notes != nil {
		t.Errorf("PauseGitOps = %v, %v", notes, err)
	}
	if err := e.ResumeGitOps(context.Background(), "shop-dev", nil); err == nil || err.Error() != "apiserver unavailable" {
		t.Errorf("ResumeGitOps = %v", err)
	}
}

func TestPauseGitOpsPicksTheApplicationInArgoCDsOwnNamespace(t *testing.T) {
	selfHealing := func(ns string, resources []any) *unstructured.Unstructured {
		return gitOpsResource(applicationGVK, ns, "shop", map[string]any{
			"spec":   map[string]any{"syncPolicy": map[string]any{"automated": map[string]any{"selfHeal": true}}},
			"status": map[string]any{"resources": resources},
		})
	}
	controlPlane := selfHealing("gitops", listing(argoResource("apps", "Deployment", "shop-dev", "ui")))
	otherTeam := selfHealing("apps-team", listing(argoResource("apps", "Deployment", "apps-team", "ui")))
	e := gitOpsEngine(t, nil, controlPlane, otherTeam, managedDeployment("ui", nil, byArgoApp("shop")))

	if notes, err := e.PauseGitOps(context.Background(), "shop-dev", nil, []string{"shop-dev"}); err != nil || len(notes) != 0 {
		t.Fatalf("PauseGitOps = %v, %v", notes, err)
	}
	selfHeal := func(u *unstructured.Unstructured) bool {
		v, _, _ := unstructured.NestedBool(reread(t, e, u).Object, "spec", "syncPolicy", "automated", "selfHeal")
		return v
	}
	if selfHeal(controlPlane) || !selfHeal(otherTeam) {
		t.Errorf("self-heal gitops/shop=%v apps-team/shop=%v; the workload tracked as \"shop\" belongs to the Application in Argo CD's namespace, "+
			"apps-team/shop would be tracked as \"apps-team_shop\"", selfHeal(controlPlane), selfHeal(otherTeam))
	}
}

func TestPauseGitOpsRefusesObjectsAWorkloadOnlyClaims(t *testing.T) {
	ctx := context.Background()
	selfHeal := map[string]any{"syncPolicy": map[string]any{"automated": map[string]any{"selfHeal": true}}}
	security := gitOpsResource(applicationGVK, "argocd", "platform-security", map[string]any{
		"spec":   map[string]any{"destination": map[string]any{"namespace": "security"}, "syncPolicy": selfHeal["syncPolicy"]},
		"status": map[string]any{"resources": listing(argoResource("apps", "Deployment", "security", "falco"))},
	})
	wide := gitOpsResource(applicationGVK, "argocd", "wide", map[string]any{
		"spec": map[string]any{"syncPolicy": selfHeal["syncPolicy"]},
		"status": map[string]any{"resources": listing(
			argoResource("apps", "Deployment", "shop-dev", "w1"),
			argoResource("apps", "Deployment", "monitoring", "grafana"),
			argoResource("rbac.authorization.k8s.io", "ClusterRole", "", "wide-reader"),
		)},
	})
	ingress := gitOpsResource(helmReleaseGVK, "ingress", "ingress-nginx", map[string]any{"spec": map[string]any{"targetNamespace": "ingress"}})
	crds := gitOpsResource(kustomizationGVK, "flux-system", "crds", map[string]any{
		"spec":   map[string]any{},
		"status": inventoryOf("shop-dev_k1_apps_Deployment", "_widgets.example.com_apiextensions.k8s.io_CustomResourceDefinition"),
	})
	silent := gitOpsResource(kustomizationGVK, "flux-system", "silent", map[string]any{
		"spec": map[string]any{}, "status": inventoryOf("shop-dev_other_apps_Deployment"),
	})
	e := gitOpsEngine(t, nil, security, wide, ingress, crds, silent,
		managedDeployment("forged", nil, byArgoApp("platform-security")),
		managedDeployment("w1", nil, byArgoApp("wide")),
		managedDeployment("h1", map[string]string{fluxHelmReleaseName: "ingress-nginx", fluxHelmReleaseNS: "ingress"}, nil),
		managedDeployment("k1", byKustomization("crds"), nil),
		managedDeployment("k2", byKustomization("silent"), nil),
	)
	watched := []*unstructured.Unstructured{security, wide, ingress, crds, silent}
	versions := func() []string {
		out := make([]string, 0, len(watched))
		for _, u := range watched {
			out = append(out, reread(t, e, u).GetResourceVersion())
		}
		return out
	}
	before := versions()

	notes, err := e.PauseGitOps(ctx, "shop-dev", nil, []string{"shop-dev"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"Argo CD application platform-security does not list Deployment forged among its resources, so it is left running",
		"Argo CD application wide also manages cluster-scoped resources, monitoring, so it is left running",
		"Flux HelmRelease ingress/ingress-nginx installs into ingress, not shop-dev, so it is left running",
		"Flux Kustomization flux-system/crds also applies to cluster-scoped resources, so it is left running",
		"Flux Kustomization flux-system/silent does not list Deployment k2 in its inventory, so it is left running",
	}
	if !slices.Equal(notes, want) {
		t.Errorf("notes =\n%s\nwant\n%s", strings.Join(notes, "\n"), strings.Join(want, "\n"))
	}
	if after := versions(); !slices.Equal(after, before) {
		t.Errorf("objects the workloads only claimed were paused: %v -> %v", before, after)
	}
}

func TestResumeGitOpsLeavesASyncSwitchedToManualAlone(t *testing.T) {
	shop := gitOpsResource(applicationGVK, "argocd", "shop", map[string]any{
		"spec":   map[string]any{"syncPolicy": map[string]any{"syncOptions": []any{"CreateNamespace=true"}}},
		"status": map[string]any{"resources": listing(argoResource("apps", "Deployment", "shop-dev", "ui"))},
	})
	shop.SetAnnotations(map[string]string{GitOpsPausedAnnotation: pausedSelfHeal})
	e := gitOpsEngine(t, nil, shop, managedDeployment("ui", nil, byArgoApp("shop")))

	if err := e.ResumeGitOps(context.Background(), "shop-dev", nil); err != nil {
		t.Fatal(err)
	}
	after := reread(t, e, shop)
	if _, found, _ := unstructured.NestedFieldNoCopy(after.Object, "spec", "syncPolicy", "automated"); found {
		t.Error("resuming turned automated sync back on, though someone switched it to manual during the pause")
	}
	if after.GetAnnotations()[GitOpsPausedAnnotation] != "" {
		t.Error("the pause marker was left behind")
	}
}
