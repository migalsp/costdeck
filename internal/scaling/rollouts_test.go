package scaling

import (
	"context"
	"errors"
	"slices"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
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

func TestWorkloadsListsWhatSchedulesScale(t *testing.T) {
	api := deploymentWith("api", 2, "50m")
	api.Annotations = map[string]string{"team": "shop"}
	api.Status.ReadyReplicas = 1
	dbReplicas := int32(1)
	db := &appsv1.StatefulSet{Name: "db", Namespace: "shop", Spec: appsv1.StatefulSetSpec{Replicas: &dbReplicas,
		Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "main", Image: "postgres"}}}}},
		Status: appsv1.StatefulSetStatus{ReadyReplicas: 1}}
	web := rollout("web", map[string]any{"replicas": int64(3), "template": podSpec("150m")})
	web.Object["status"] = map[string]any{"readyReplicas": int64(3)}
	ref := rollout("ref", map[string]any{"workloadRef": map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "name": "legacy"}})
	e := hibernateEngine(t, []schema.GroupVersionKind{rolloutGVK}, nil, api, db, web, ref, deploymentWith("legacy", 0, "200m"))

	got, err := e.Workloads(context.Background(), "shop")
	if err != nil {
		t.Fatal(err)
	}
	type row struct {
		kind, name, image, team string
		replicas, ready         int32
	}
	rows := make([]row, 0, len(got))
	for _, w := range got {
		image := ""
		if cs := w.Template.Spec.Containers; len(cs) > 0 {
			image = cs[0].Image
		}
		rows = append(rows, row{w.Kind, w.Name, image, w.Annotations["team"], w.Replicas, w.Ready})
	}
	want := []row{
		{"Deployment", "api", "api", "shop", 2, 1},
		{"StatefulSet", "db", "postgres", "", 1, 1},
		{"Rollout", "ref", "legacy", "", 1, 0},
		{"Rollout", "web", "web", "", 3, 3},
	}
	if !slices.Equal(rows, want) {
		t.Errorf("Workloads =\n%+v\nwant\n%+v\n(legacy runs ref's pods, so it is not listed; ref shows legacy's template)", rows, want)
	}

	failing := hibernateEngine(t, nil, &interceptor.Funcs{List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error {
		return errors.New("apiserver unavailable")
	}})
	if got, err := failing.Workloads(context.Background(), "shop"); err == nil || got != nil {
		t.Errorf("Workloads with a failing list = %v, %v", got, err)
	}
}

func TestSetWorkloadReplicasScalesOneWorkloadByHand(t *testing.T) {
	ctx := context.Background()
	api := deploymentWith("api", 2, "50m")
	api.Annotations = map[string]string{OriginalReplicasAnnotation: "5"}
	dbReplicas := int32(1)
	db := &appsv1.StatefulSet{Name: "db", Namespace: "shop", Spec: appsv1.StatefulSetSpec{Replicas: &dbReplicas}}
	web := rollout("web", map[string]any{"replicas": int64(3), "template": podSpec("250m")})
	e := hibernateEngine(t, []schema.GroupVersionKind{rolloutGVK}, nil, api, db, web)

	must(t, e.SetWorkloadReplicas(ctx, "shop", "Deployment", "api", 4))
	must(t, e.SetWorkloadReplicas(ctx, "shop", "StatefulSet", "db", 0))
	must(t, e.SetWorkloadReplicas(ctx, "shop", "Rollout", "web", 1))

	var gotAPI appsv1.Deployment
	must(t, e.Client.Get(ctx, client.ObjectKeyFromObject(api), &gotAPI))
	if *gotAPI.Spec.Replicas != 4 || gotAPI.Annotations[OriginalReplicasAnnotation] != "5" || gotAPI.Spec.Template.Spec.Containers[0].Image != "api" {
		t.Errorf("Deployment = %d replicas, record %q, image %s; want 4 with the rest untouched",
			*gotAPI.Spec.Replicas, gotAPI.Annotations[OriginalReplicasAnnotation], gotAPI.Spec.Template.Spec.Containers[0].Image)
	}
	var gotDB appsv1.StatefulSet
	must(t, e.Client.Get(ctx, client.ObjectKeyFromObject(db), &gotDB))
	if *gotDB.Spec.Replicas != 0 {
		t.Errorf("StatefulSet = %d replicas, want 0", *gotDB.Spec.Replicas)
	}
	cur := reread(t, e, web)
	if n, _, _ := unstructured.NestedInt64(cur.Object, "spec", "replicas"); n != 1 || rolloutTemplate(cur).Spec.Containers[0].Image != "web" {
		t.Errorf("Rollout = %d replicas, template %+v; want 1 with its template kept", n, rolloutTemplate(cur))
	}

	if err := e.SetWorkloadReplicas(ctx, "shop", "DaemonSet", "agent", 1); !errors.Is(err, ErrUnknownKind) {
		t.Errorf("DaemonSet = %v, want ErrUnknownKind", err)
	}
	if err := e.SetWorkloadReplicas(ctx, "shop", "Deployment", "absent", 1); !apierrors.IsNotFound(err) {
		t.Errorf("missing Deployment = %v, want NotFound", err)
	}
	withoutArgo := hibernateEngine(t, nil, nil, web.DeepCopy())
	err := withoutArgo.SetWorkloadReplicas(ctx, "shop", "Rollout", "web", 1)
	if !errors.Is(err, ErrUnknownKind) || err.Error() != "unknown workload kind: use Deployment, StatefulSet or Rollout: Argo Rollouts is not installed" {
		t.Errorf("Rollout without Argo Rollouts = %v", err)
	}
}
