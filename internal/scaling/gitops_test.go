package scaling

import (
	"slices"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
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
