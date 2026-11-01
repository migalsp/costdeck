package scaling

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var rolloutGVK = schema.GroupVersionKind{Group: "argoproj.io", Version: "v1alpha1", Kind: "Rollout"}

func rollout(name string, spec map[string]any) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{"spec": spec}}
	u.SetGroupVersionKind(rolloutGVK)
	u.SetNamespace("shop")
	u.SetName(name)
	return u
}

func podSpec(cpu string) map[string]any {
	return map[string]any{
		"metadata": map[string]any{"labels": map[string]any{"app": "web"}},
		"spec": map[string]any{"containers": []any{map[string]any{"name": "main", "image": "web",
			"resources": map[string]any{"requests": map[string]any{"cpu": cpu, "memory": "128Mi"}}}}},
	}
}

func deploymentWith(name string, replicas int32, cpu string) *appsv1.Deployment {
	return &appsv1.Deployment{Name: name, Namespace: "shop", Spec: appsv1.DeploymentSpec{
		Replicas: &replicas, Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}},
		Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "main", Image: name,
			Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(cpu)}}}}}},
	}}
}

func TestRolloutsScaleLikeDeployments(t *testing.T) {
	ctx := context.Background()
	web := rollout("web", map[string]any{"replicas": int64(3), "selector": map[string]any{"matchLabels": map[string]any{"app": "web"}}, "template": podSpec("100m")})
	// ref takes its pods from the Deployment legacy, which its owner keeps at zero.
	ref := rollout("ref", map[string]any{"workloadRef": map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "name": "legacy"}})
	legacy := deploymentWith("legacy", 0, "200m")
	api := deploymentWith("api", 2, "50m")
	e := hibernateEngine(t, []schema.GroupVersionKind{rolloutGVK}, nil, web, ref, legacy, api)
	replicas := func(u *unstructured.Unstructured) (int64, string) {
		cur := reread(t, e, u)
		n, _, _ := unstructured.NestedInt64(cur.Object, "spec", "replicas")
		return n, cur.GetAnnotations()[OriginalReplicasAnnotation]
	}
	legacyReplicas := func() (int32, string) {
		var d appsv1.Deployment
		if err := e.Client.Get(ctx, client.ObjectKeyFromObject(legacy), &d); err != nil {
			t.Fatal(err)
		}
		return *d.Spec.Replicas, d.Annotations[OriginalReplicasAnnotation]
	}

	preview, err := e.PreviewNamespace(ctx, "shop", nil)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]WorkloadPreview{}
	for _, w := range preview.Workloads {
		kinds[w.Kind+"/"+w.Name] = w
	}
	if len(kinds) != 3 || kinds["Rollout/web"].Replicas != 3 || kinds["Rollout/ref"].CPU != "200m" || kinds["Deployment/api"].Replicas != 2 {
		t.Fatalf("preview = %+v: the Rollouts, with ref's template from legacy, and not legacy itself", preview.Workloads)
	}

	originals := map[string]int32{}
	if _, _, err := e.ScaleTarget(ctx, "shop", false, nil, nil, originals, true); err != nil {
		t.Fatal(err)
	}
	if n, rec := replicas(web); n != 0 || rec != "3" {
		t.Errorf("web after scale-down = %d, recorded %q", n, rec)
	}
	if n, rec := replicas(ref); n != 0 || rec != "1" {
		t.Errorf("ref after scale-down = %d, recorded %q", n, rec)
	}
	if n, rec := legacyReplicas(); n != 0 || rec != "" {
		t.Errorf("the referenced Deployment was touched: %d, %q", n, rec)
	}
	if cur := reread(t, e, ref); cur.Object["spec"].(map[string]any)["template"] != nil {
		t.Error("the borrowed template was written to the Rollout")
	}
	cpu, _, err := e.KeptDown(ctx, "shop", originals)
	if err != nil || cpu.String() != "600m" {
		t.Errorf("kept down = %s (%v), want 3×100m + 1×200m + 2×50m", cpu.String(), err)
	}

	if _, _, err := e.ScaleTarget(ctx, "shop", true, nil, nil, originals, true); err != nil {
		t.Fatal(err)
	}
	if n, rec := replicas(web); n != 3 || rec != "" {
		t.Errorf("web after scale-up = %d, recorded %q", n, rec)
	}
	if n, _ := replicas(ref); n != 1 {
		t.Errorf("ref after scale-up = %d", n)
	}
	if n, _ := legacyReplicas(); n != 0 {
		t.Errorf("the referenced Deployment was started: %d replicas, a copy of the Rollout's pods", n)
	}
}

func TestRolloutReadinessAndRules(t *testing.T) {
	ctx := context.Background()
	web := rollout("web", map[string]any{"replicas": int64(2), "template": podSpec("100m")})
	web.Object["status"] = map[string]any{"readyReplicas": int64(1), "replicas": int64(2), "selector": "app=web"}
	e := hibernateEngine(t, []schema.GroupVersionKind{rolloutGVK}, nil, web)
	if phase := e.ComputePhase(ctx, "shop", true, nil); phase != PhaseScalingUp {
		t.Errorf("one of two ready = %s", phase)
	}
	setStatus(t, e, web, map[string]any{"readyReplicas": int64(2), "replicas": int64(2), "selector": "app=web"})
	if phase := e.ComputePhase(ctx, "shop", true, nil); phase != PhaseScaledUp {
		t.Errorf("both ready = %s", phase)
	}
	if st := rolloutState(reread(t, e, web)); st.selector == nil || st.selector.MatchLabels["app"] != "web" {
		t.Errorf("selector from status = %+v", st.selector)
	}
	// Workload rules name Rollouts like any other kind.
	if n, _ := e.WouldChange(ctx, "shop", false, []string{"web"}); n != 0 {
		t.Errorf("an excluded Rollout would change: %d", n)
	}
	if !matchWorkloadPattern("Rollout/w*", workloadKind(web), "web") {
		t.Error("a Rollout/ sequence entry does not match")
	}
}

func TestWithoutArgoRollouts(t *testing.T) {
	e := hibernateEngine(t, nil, nil, deploymentWith("api", 2, "50m"))
	objs, err := e.listScalableResources(context.Background(), "shop", nil)
	if err != nil || len(objs) != 1 {
		t.Errorf("objects = %d, %v", len(objs), err)
	}
}
