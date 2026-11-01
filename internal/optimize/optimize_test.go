package optimize

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/rightsizing"
)

var now = time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)

func q(s string) resource.Quantity { return resource.MustParse(s) }

func deploy(name string, containers ...corev1.Container) *appsv1.Deployment {
	return &appsv1.Deployment{Name: name, Namespace: "shop", Spec: appsv1.DeploymentSpec{
		Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}},
		Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": name}}, Spec: corev1.PodSpec{Containers: containers}},
	}}
}

func container(name string, req, lim corev1.ResourceList) corev1.Container {
	return corev1.Container{Name: name, Image: name, Resources: corev1.ResourceRequirements{Requests: req, Limits: lim}}
}

func client_(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	t.Setenv("POD_NAMESPACE", "costdeck")
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = finopsv1.AddToScheme(scheme)
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).WithStatusSubresource(&finopsv1.NamespaceOptimization{}).Build()
}

func reduce(cpu, mem string) rightsizing.ContainerAdvice {
	return rightsizing.ContainerAdvice{Name: "main",
		CPU:    rightsizing.ResourceAdvice{Action: rightsizing.ActionReduce, Recommended: cpu},
		Memory: rightsizing.ResourceAdvice{Action: rightsizing.ActionReduce, Recommended: mem}}
}

func TestApplyAndRevert(t *testing.T) {
	ctx := context.Background()
	api := deploy("api",
		container("main", corev1.ResourceList{"cpu": q("2"), "memory": q("4Gi")}, corev1.ResourceList{"memory": q("4Gi")}),
		container("sidecar", corev1.ResourceList{"cpu": q("100m")}, nil))
	c := client_(t, api)
	advice := rightsizing.WorkloadAdvice{Kind: "Deployment", Name: "api", Containers: []rightsizing.ContainerAdvice{reduce("250m", "512Mi"),
		{Name: "sidecar", CPU: rightsizing.ResourceAdvice{Action: rightsizing.ActionKeep}}}}

	rec, err := Apply(ctx, c, "shop", advice, "ops", now)
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.Containers) != 1 || rec.Containers[0].Original.CPURequest != "2" || rec.Containers[0].Optimized.CPURequest != "250m" || rec.State != StateApplied || rec.AppliedBy != "ops" {
		t.Fatalf("record = %+v", rec)
	}
	var got appsv1.Deployment
	_ = c.Get(ctx, client.ObjectKeyFromObject(api), &got)
	main := got.Spec.Template.Spec.Containers[0].Resources
	if main.Requests.Cpu().String() != "250m" || main.Requests.Memory().String() != "512Mi" || main.Limits.Memory().String() != "4Gi" {
		t.Errorf("main = %+v", main)
	}
	if got.Spec.Template.Spec.Containers[1].Resources.Requests.Cpu().String() != "100m" {
		t.Error("the sidecar was changed")
	}
	var opt finopsv1.NamespaceOptimization
	if err := c.Get(ctx, client.ObjectKey{Namespace: "costdeck", Name: "shop"}, &opt); err != nil || !opt.Status.Active || len(opt.Status.Workloads) != 1 {
		t.Fatalf("record in the cluster = %+v, %v", opt.Status, err)
	}

	back, err := Revert(ctx, c, "shop", "api", "reverted by ops")
	if err != nil || back.State != StateReverted || back.Reason != "reverted by ops" {
		t.Fatalf("revert = %+v, %v", back, err)
	}
	_ = c.Get(ctx, client.ObjectKeyFromObject(api), &got)
	if r := got.Spec.Template.Spec.Containers[0].Resources; r.Requests.Cpu().String() != "2" || r.Requests.Memory().String() != "4Gi" {
		t.Errorf("not restored: %+v", r)
	}
	_ = c.Get(ctx, client.ObjectKey{Namespace: "costdeck", Name: "shop"}, &opt)
	if opt.Status.Active {
		t.Error("the namespace still counts as optimized")
	}
}

func TestApplyRefusesAndCaps(t *testing.T) {
	ctx := context.Background()
	gitops := deploy("managed", container("main", corev1.ResourceList{"cpu": q("2")}, nil))
	gitops.Annotations = map[string]string{"argocd.argoproj.io/tracking-id": "shop:apps/Deployment:shop/managed"}
	capped := deploy("capped", container("main", corev1.ResourceList{"cpu": q("500m")}, corev1.ResourceList{"cpu": q("1")}))
	own := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{
		"selector": map[string]any{"matchLabels": map[string]any{"app": "web"}},
		"template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"name": "main", "image": "web",
			"resources": map[string]any{"requests": map[string]any{"cpu": "2", "memory": "2Gi"}}}}}}}}}
	own.SetGroupVersionKind(rolloutGVK)
	own.SetNamespace("shop")
	own.SetName("web")
	ref := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{"workloadRef": map[string]any{"kind": "Deployment", "name": "x"}}}}
	ref.SetGroupVersionKind(rolloutGVK)
	ref.SetNamespace("shop")
	ref.SetName("ref")
	c := client_(t, gitops, capped, own, ref)

	if _, err := Apply(ctx, c, "shop", rightsizing.WorkloadAdvice{Kind: "Deployment", Name: "managed", Containers: []rightsizing.ContainerAdvice{reduce("250m", "")}}, "ops", now); !errors.Is(err, ErrManagedByGitOps) || !strings.Contains(err.Error(), "Argo CD") {
		t.Errorf("gitops: %v", err)
	}
	keep := rightsizing.ContainerAdvice{Name: "main", CPU: rightsizing.ResourceAdvice{Action: rightsizing.ActionKeep}, Memory: rightsizing.ResourceAdvice{Action: rightsizing.ActionUnknown}}
	if _, err := Apply(ctx, c, "shop", rightsizing.WorkloadAdvice{Kind: "Deployment", Name: "capped", Containers: []rightsizing.ContainerAdvice{keep}}, "ops", now); !errors.Is(err, ErrNothingToApply) {
		t.Errorf("nothing to apply: %v", err)
	}
	up := rightsizing.ContainerAdvice{Name: "main", CPU: rightsizing.ResourceAdvice{Action: rightsizing.ActionIncrease, Recommended: "2"}}
	if rec, err := Apply(ctx, c, "shop", rightsizing.WorkloadAdvice{Kind: "Deployment", Name: "capped", Containers: []rightsizing.ContainerAdvice{up}}, "ops", now); err != nil || rec.Containers[0].Optimized.CPURequest != "1" {
		t.Errorf("an increase above the limit: %+v, %v", rec, err)
	}
	if rec, err := Apply(ctx, c, "shop", rightsizing.WorkloadAdvice{Kind: "Rollout", Name: "web", Containers: []rightsizing.ContainerAdvice{reduce("300m", "256Mi")}}, "ops", now); err != nil || rec.Containers[0].Optimized.MemoryRequest != "256Mi" {
		t.Errorf("a Rollout: %+v, %v", rec, err)
	}
	if _, err := Apply(ctx, c, "shop", rightsizing.WorkloadAdvice{Kind: "Rollout", Name: "ref", Containers: []rightsizing.ContainerAdvice{reduce("300m", "")}}, "ops", now); !errors.Is(err, ErrUnsupported) || !strings.Contains(err.Error(), "workloadRef") {
		t.Errorf("a workloadRef Rollout: %v", err)
	}
}

func TestRevertALegacyRecord(t *testing.T) {
	ctx := context.Background()
	api := deploy("api", container("main", corev1.ResourceList{"cpu": q("100m")}, nil))
	opt := &finopsv1.NamespaceOptimization{Name: "shop", Namespace: "costdeck", Spec: finopsv1.NamespaceOptimizationSpec{TargetNamespace: "shop"},
		Status: finopsv1.NamespaceOptimizationStatus{Active: true, Workloads: []finopsv1.WorkloadOptimization{{Name: "api", Kind: "Deployment",
			Original: finopsv1.ResourceValues{CPURequest: "1", MemoryRequest: "1Gi", MemoryLimit: "2Gi"}}}}}
	c := client_(t, api, opt)
	if err := RevertAll(ctx, c, "shop", "reverted by ops"); err != nil {
		t.Fatal(err)
	}
	var got appsv1.Deployment
	_ = c.Get(ctx, client.ObjectKeyFromObject(api), &got)
	if r := got.Spec.Template.Spec.Containers[0].Resources; r.Requests.Cpu().String() != "1" || r.Limits.Memory().String() != "2Gi" {
		t.Errorf("legacy record not restored: %+v", r)
	}
}

type sent struct{ messages []string }

func (s *sent) Send(_ context.Context, _ finopsv1.NotificationEvent, md string) ([]string, error) {
	s.messages = append(s.messages, md)
	return nil, nil
}

func TestGuard(t *testing.T) {
	ctx := context.Background()
	at := metav1.NewTime(now.Add(-10 * time.Minute))
	pod := func(name string, created time.Time, restarts int32) *corev1.Pod {
		return &corev1.Pod{Name: name, Namespace: "shop", Labels: map[string]string{"app": "api"}, CreationTimestamp: metav1.NewTime(created),
			Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{Name: "main", RestartCount: restarts}}}}
	}
	record := func(name string, appliedAt metav1.Time) finopsv1.WorkloadOptimization {
		return finopsv1.WorkloadOptimization{Name: name, Kind: "Deployment", State: StateApplied, AppliedAt: &appliedAt,
			Containers: []finopsv1.ContainerOptimization{{Name: "main", Original: finopsv1.ResourceValues{CPURequest: "2"}, Optimized: finopsv1.ResourceValues{CPURequest: "100m"}}}}
	}
	opt := &finopsv1.NamespaceOptimization{Name: "shop", Namespace: "costdeck", Spec: finopsv1.NamespaceOptimizationSpec{TargetNamespace: "shop"},
		Status: finopsv1.NamespaceOptimizationStatus{Active: true, Workloads: []finopsv1.WorkloadOptimization{record("api", at), record("quiet", metav1.NewTime(now.Add(-2*time.Hour)))}}}
	c := client_(t, opt,
		deploy("api", container("main", corev1.ResourceList{"cpu": q("100m")}, nil)),
		deploy("quiet", container("main", corev1.ResourceList{"cpu": q("100m")}, nil)),
		pod("api-old", now.Add(-time.Hour), 7),     // from before the change: ignored
		pod("api-new", now.Add(-5*time.Minute), 1)) // started by it, and restarted
	s := &sent{}
	if err := (&Guard{Client: c, Notify: s, Now: func() time.Time { return now }}).Check(ctx); err != nil {
		t.Fatal(err)
	}
	_ = c.Get(ctx, client.ObjectKeyFromObject(opt), opt)
	states := map[string]finopsv1.WorkloadOptimization{}
	for _, w := range opt.Status.Workloads {
		states[w.Name] = w
	}
	if w := states["api"]; w.State != StateReverted || !strings.Contains(w.Reason, "pod api-new restarted 1 time(s)") {
		t.Errorf("api = %+v", w)
	}
	if states["quiet"].State != StateKept {
		t.Errorf("quiet = %+v", states["quiet"])
	}
	var api appsv1.Deployment
	_ = c.Get(ctx, client.ObjectKey{Namespace: "shop", Name: "api"}, &api)
	if api.Spec.Template.Spec.Containers[0].Resources.Requests.Cpu().String() != "2" {
		t.Error("the old request is not back")
	}
	if len(s.messages) != 1 || !strings.Contains(s.messages[0], "Put back the requests of Deployment `shop/api`") {
		t.Errorf("messages = %v", s.messages)
	}
}
