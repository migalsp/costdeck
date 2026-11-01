package optimize

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

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

const quarterCore = "250m"

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
	advice := rightsizing.WorkloadAdvice{Kind: "Deployment", Name: "api", Containers: []rightsizing.ContainerAdvice{reduce(quarterCore, "512Mi"),
		{Name: "sidecar", CPU: rightsizing.ResourceAdvice{Action: rightsizing.ActionKeep}}}}

	rec, err := Apply(ctx, c, "shop", advice, "ops", now)
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.Containers) != 1 || rec.Containers[0].Original.CPURequest != "2" || rec.Containers[0].Optimized.CPURequest != quarterCore || rec.State != StateApplied || rec.AppliedBy != "ops" {
		t.Fatalf("record = %+v", rec)
	}
	var got appsv1.Deployment
	_ = c.Get(ctx, client.ObjectKeyFromObject(api), &got)
	main := got.Spec.Template.Spec.Containers[0].Resources
	if main.Requests.Cpu().String() != quarterCore || main.Requests.Memory().String() != "512Mi" || main.Limits.Memory().String() != "4Gi" {
		t.Errorf("main = %+v", main)
	}
	if got.Spec.Template.Spec.Containers[1].Resources.Requests.Cpu().String() != "100m" {
		t.Error("the sidecar was changed")
	}
	var opt finopsv1.NamespaceOptimization
	if err := c.Get(ctx, client.ObjectKey{Namespace: "costdeck", Name: "shop"}, &opt); err != nil || !opt.Status.Active || len(opt.Status.Workloads) != 1 {
		t.Fatalf("record in the cluster = %+v, %v", opt.Status, err)
	}

	back, err := Revert(ctx, c, "shop", "Deployment", "api", "reverted by ops")
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

	if _, err := Apply(ctx, c, "shop", rightsizing.WorkloadAdvice{Kind: "Deployment", Name: "managed", Containers: []rightsizing.ContainerAdvice{reduce(quarterCore, "")}}, "ops", now); !errors.Is(err, ErrManagedByGitOps) || !strings.Contains(err.Error(), "Argo CD") {
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

func guardedRecord(name string, appliedAt time.Time) finopsv1.WorkloadOptimization {
	at := metav1.NewTime(appliedAt)
	return finopsv1.WorkloadOptimization{Name: name, Kind: "Deployment", State: StateApplied, AppliedAt: &at,
		Containers: []finopsv1.ContainerOptimization{{Name: "main", Original: finopsv1.ResourceValues{CPURequest: "2"}, Optimized: finopsv1.ResourceValues{CPURequest: "100m"}}}}
}

func guardedNamespace(records ...finopsv1.WorkloadOptimization) *finopsv1.NamespaceOptimization {
	return &finopsv1.NamespaceOptimization{Name: "shop", Namespace: "costdeck", Spec: finopsv1.NamespaceOptimizationSpec{TargetNamespace: "shop"},
		Status: finopsv1.NamespaceOptimizationStatus{Active: true, Workloads: records}}
}

func podWith(name, app string, created time.Time, status corev1.ContainerStatus) *corev1.Pod {
	status.Name = "main"
	return &corev1.Pod{Name: name, Namespace: "shop", Labels: map[string]string{"app": app}, CreationTimestamp: metav1.NewTime(created),
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{status}}}
}

func workloadStates(t *testing.T, c client.Client) map[string]finopsv1.WorkloadOptimization {
	t.Helper()
	var opt finopsv1.NamespaceOptimization
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "costdeck", Name: "shop"}, &opt); err != nil {
		t.Fatal(err)
	}
	out := map[string]finopsv1.WorkloadOptimization{}
	for _, w := range opt.Status.Workloads {
		out[w.Name] = w
	}
	return out
}

func TestGuardStartChecksRightAway(t *testing.T) {
	oom := corev1.ContainerStatus{LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled"}}}
	c := client_(t, guardedNamespace(guardedRecord("api", now.Add(-10*time.Minute))),
		deploy("api", container("main", corev1.ResourceList{"cpu": q("100m")}, nil)),
		podWith("api-7d9f8-x2k4p", "api", now.Add(-5*time.Minute), oom))
	g := &Guard{Client: c, Now: func() time.Time { return now }}
	if !g.NeedLeaderElection() {
		t.Fatal("the guard must run on the leader only, so a change is reverted once")
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- g.Start(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	for workloadStates(t, c)["api"].State != StateReverted {
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("the first check did not revert the OOM-killed workload")
		}
		time.Sleep(2 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Start returned %v after cancellation, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Start did not return after its context was cancelled")
	}
	var api appsv1.Deployment
	_ = c.Get(context.Background(), client.ObjectKey{Namespace: "shop", Name: "api"}, &api)
	if got := api.Spec.Template.Spec.Containers[0].Resources.Requests.Cpu().String(); got != "2" {
		t.Errorf("cpu request = %s, want the original 2 back", got)
	}
}

func TestGuardDecidesFromThePodsTheChangeStarted(t *testing.T) {
	applied := now.Add(-10 * time.Minute)
	after, before := now.Add(-5*time.Minute), now.Add(-time.Hour)
	oom := corev1.ContainerStatus{LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled"}}}
	crashLoop := corev1.ContainerStatus{State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}}
	restarted := corev1.ContainerStatus{RestartCount: 3}
	for _, tc := range []struct {
		name         string
		guardMinutes int32
		pod          *corev1.Pod
		noWorkload   bool
		wantState    string
		wantReason   string
		openSelector bool
	}{
		{"an OOM kill in a new pod reverts", 0, podWith("api-7d9f8-x2k4p", "api", after, oom), false, StateReverted, "container main of pod api-7d9f8-x2k4p was OOMKilled", false},
		{"a crash loop in a new pod reverts", 0, podWith("api-7d9f8-x2k4p", "api", after, crashLoop), false, StateReverted, "container main of pod api-7d9f8-x2k4p is crash-looping", false},
		{"restarts of a pod from before the change are ignored", 5, podWith("api-5c6b7-q8w9e", "api", before, restarted), false, StateKept, "", false},
		{"another workload's pods are ignored", 5, podWith("web-7d9f8-x2k4p", "web", after, oom), false, StateKept, "", false},
		{"a quiet change stays watched for the default period", 0, podWith("api-7d9f8-x2k4p", "api", after, corev1.ContainerStatus{}), false, StateApplied, "", false},
		{"a quiet change is kept after spec.rightsizing.guardMinutes", 5, podWith("api-7d9f8-x2k4p", "api", after, corev1.ContainerStatus{}), false, StateKept, "", false},
		{"a deleted workload is not reverted", 0, podWith("api-7d9f8-x2k4p", "api", after, oom), true, StateApplied, "", false},
		{"an empty selector matches no pods rather than all", 0, podWith("web-7d9f8-x2k4p", "web", after, oom), false, StateApplied, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &finopsv1.CostDeckConfig{Name: "default", Namespace: "costdeck", Spec: finopsv1.CostDeckConfigSpec{Rightsizing: finopsv1.RightsizingConfig{GuardMinutes: tc.guardMinutes}}}
			objs := []client.Object{cfg, tc.pod, guardedNamespace(guardedRecord("api", applied))}
			if !tc.noWorkload {
				api := deploy("api", container("main", corev1.ResourceList{"cpu": q("100m")}, nil))
				if tc.openSelector {
					api.Spec.Selector = &metav1.LabelSelector{}
				}
				objs = append(objs, api)
			}
			c := client_(t, objs...)
			if err := (&Guard{Client: c, Now: func() time.Time { return now }}).Check(context.Background()); err != nil {
				t.Fatal(err)
			}
			w := workloadStates(t, c)["api"]
			if w.State != tc.wantState || !strings.Contains(w.Reason, tc.wantReason) {
				t.Errorf("api = state %q, reason %q; want %q, %q", w.State, w.Reason, tc.wantState, tc.wantReason)
			}
		})
	}
}

func TestGuardLeavesSettledRecordsAlone(t *testing.T) {
	oom := corev1.ContainerStatus{LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled"}}}
	kept := guardedRecord("api", now.Add(-10*time.Minute))
	kept.State = StateKept
	legacy := guardedRecord("web", now)
	legacy.AppliedAt = nil
	c := client_(t, guardedNamespace(kept, legacy),
		deploy("api", container("main", corev1.ResourceList{"cpu": q("100m")}, nil)),
		deploy("web", container("main", corev1.ResourceList{"cpu": q("100m")}, nil)),
		podWith("api-7d9f8-x2k4p", "api", now.Add(-5*time.Minute), oom),
		podWith("web-7d9f8-x2k4p", "web", now.Add(-5*time.Minute), oom))
	s := &sent{}
	if err := (&Guard{Client: c, Notify: s, Now: func() time.Time { return now }}).Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	states := workloadStates(t, c)
	if states["api"].State != StateKept || states["web"].State != StateApplied || len(s.messages) != 0 {
		t.Errorf("states = %+v, messages %v; a kept change and one without an apply time must not be reverted", states, s.messages)
	}
}

func TestRecordRetriesAChangeMadeMeanwhile(t *testing.T) {
	ctx := context.Background()
	t.Setenv("POD_NAMESPACE", "costdeck")
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = finopsv1.AddToScheme(scheme)
	conflicts := 0
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(guardedNamespace(guardedRecord("web", now))).
		WithStatusSubresource(&finopsv1.NamespaceOptimization{}).
		WithInterceptorFuncs(interceptor.Funcs{SubResourceUpdate: func(ctx context.Context, w client.Client, sub string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
			if conflicts == 0 {
				conflicts++
				return apierrors.NewConflict(schema.GroupResource{Group: "finops.costdeck.io", Resource: "namespaceoptimizations"}, obj.GetName(), errors.New("the object has been modified"))
			}
			return w.SubResource(sub).Update(ctx, obj, opts...)
		}}).Build()
	if err := Record(ctx, c, "shop", guardedRecord("api", now)); err != nil {
		t.Fatalf("Record = %v; a conflict must be read again and retried", err)
	}
	if states := workloadStates(t, c); len(states) != 2 {
		t.Errorf("records = %v, want web and api", states)
	}
}

func TestRecordRetriesWhenAnotherWriterCreatedTheRecordFirst(t *testing.T) {
	ctx := context.Background()
	t.Setenv("POD_NAMESPACE", "costdeck")
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = finopsv1.AddToScheme(scheme)
	raced := false
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&finopsv1.NamespaceOptimization{}).
		WithInterceptorFuncs(interceptor.Funcs{Create: func(ctx context.Context, w client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if _, ok := obj.(*finopsv1.NamespaceOptimization); ok && !raced {
				raced = true
				winner := guardedNamespace(guardedRecord("web", now))
				if err := w.Create(ctx, winner); err != nil {
					return err
				}
				if err := w.Status().Update(ctx, winner); err != nil {
					return err
				}
				return apierrors.NewAlreadyExists(schema.GroupResource{Group: "finops.costdeck.io", Resource: "namespaceoptimizations"}, obj.GetName())
			}
			return w.Create(ctx, obj, opts...)
		}}).Build()
	if err := Record(ctx, c, "shop", guardedRecord("api", now)); err != nil {
		t.Fatalf("Record = %v; losing the race to create the record must read it and retry", err)
	}
	if states := workloadStates(t, c); len(states) != 2 {
		t.Errorf("records = %v, want web and api", states)
	}
}

func TestRecordsKeepWorkloadsOfTheSameNameApart(t *testing.T) {
	ctx := context.Background()
	api := deploy("api", container("main", corev1.ResourceList{"cpu": q("2")}, nil))
	db := &appsv1.StatefulSet{Name: "api", Namespace: "shop", Spec: appsv1.StatefulSetSpec{
		Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "api-db"}},
		Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "api-db"}},
			Spec: corev1.PodSpec{Containers: []corev1.Container{container("main", corev1.ResourceList{"cpu": q("4")}, nil)}}},
	}}
	c := client_(t, api, db)
	if _, err := Apply(ctx, c, "shop", rightsizing.WorkloadAdvice{Kind: "Deployment", Name: "api", Containers: []rightsizing.ContainerAdvice{reduce(quarterCore, "")}}, "ops", now); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(ctx, c, "shop", rightsizing.WorkloadAdvice{Kind: "StatefulSet", Name: "api", Containers: []rightsizing.ContainerAdvice{reduce("1", "")}}, "ops", now); err != nil {
		t.Fatal(err)
	}
	var opt finopsv1.NamespaceOptimization
	if err := c.Get(ctx, client.ObjectKey{Namespace: "costdeck", Name: "shop"}, &opt); err != nil || len(opt.Status.Workloads) != 2 {
		t.Fatalf("records = %+v, %v; the StatefulSet's change replaced the Deployment's", opt.Status.Workloads, err)
	}
	if _, err := Revert(ctx, c, "shop", "", "api", "by hand"); !errors.Is(err, ErrAmbiguousWorkload) {
		t.Errorf("Revert without a kind = %v, want ErrAmbiguousWorkload", err)
	}
	if _, err := Revert(ctx, c, "shop", "StatefulSet", "api", "by hand"); err != nil {
		t.Fatal(err)
	}
	var gotDB appsv1.StatefulSet
	var gotAPI appsv1.Deployment
	_ = c.Get(ctx, client.ObjectKeyFromObject(db), &gotDB)
	_ = c.Get(ctx, client.ObjectKeyFromObject(api), &gotAPI)
	if gotDB.Spec.Template.Spec.Containers[0].Resources.Requests.Cpu().String() != "4" {
		t.Error("the StatefulSet's request is not back")
	}
	if gotAPI.Spec.Template.Spec.Containers[0].Resources.Requests.Cpu().String() != quarterCore {
		t.Error("reverting the StatefulSet put back the Deployment too")
	}
}

func TestApplyUndoesAChangeItCannotRecord(t *testing.T) {
	ctx := context.Background()
	t.Setenv("POD_NAMESPACE", "costdeck")
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = finopsv1.AddToScheme(scheme)
	api := deploy("api", container("main", corev1.ResourceList{"cpu": q("2")}, nil))
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(api).WithStatusSubresource(&finopsv1.NamespaceOptimization{}).
		WithInterceptorFuncs(interceptor.Funcs{SubResourceUpdate: func(context.Context, client.Client, string, client.Object, ...client.SubResourceUpdateOption) error {
			return apierrors.NewForbidden(schema.GroupResource{Group: "finops.costdeck.io", Resource: "namespaceoptimizations"}, "shop", errors.New("RBAC"))
		}}).Build()
	_, err := Apply(ctx, c, "shop", rightsizing.WorkloadAdvice{Kind: "Deployment", Name: "api", Containers: []rightsizing.ContainerAdvice{reduce(quarterCore, "")}}, "ops", now)
	if err == nil || !strings.Contains(err.Error(), "so it was undone") {
		t.Fatalf("Apply = %v", err)
	}
	var got appsv1.Deployment
	_ = c.Get(ctx, client.ObjectKeyFromObject(api), &got)
	if cpu := got.Spec.Template.Spec.Containers[0].Resources.Requests.Cpu().String(); cpu != "2" {
		t.Errorf("cpu request = %s; a change without a record cannot be reverted, so it must not stay", cpu)
	}
}

func TestGuardKeepsGoingAfterARestoreFails(t *testing.T) {
	ctx := context.Background()
	opt := guardedNamespace(guardedRecord("first", now.Add(-10*time.Minute)), guardedRecord("second", now.Add(-10*time.Minute)))
	t.Setenv("POD_NAMESPACE", "costdeck")
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = finopsv1.AddToScheme(scheme)
	crashing := corev1.ContainerStatus{RestartCount: 3}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&finopsv1.NamespaceOptimization{}).
		WithObjects(opt,
			deploy("first", container("main", corev1.ResourceList{"cpu": q("100m")}, nil)),
			deploy("second", container("main", corev1.ResourceList{"cpu": q("100m")}, nil)),
			podWith("first-1", "first", now.Add(-5*time.Minute), crashing),
			podWith("second-1", "second", now.Add(-5*time.Minute), crashing)).
		WithInterceptorFuncs(interceptor.Funcs{Patch: func(ctx context.Context, w client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
			if obj.GetName() == "first" {
				return apierrors.NewForbidden(schema.GroupResource{Group: "apps", Resource: "deployments"}, "first", errors.New("admission policy"))
			}
			return w.Patch(ctx, obj, patch, opts...)
		}}).Build()
	err := (&Guard{Client: c, Now: func() time.Time { return now }}).Check(ctx)
	if err == nil || !strings.Contains(err.Error(), "put back Deployment shop/first") {
		t.Errorf("Check = %v, want the failed restore reported", err)
	}
	states := workloadStates(t, c)
	if states["second"].State != StateReverted {
		t.Errorf("second = %+v; one failed restore stopped the guard for every other workload", states["second"])
	}
	if states["first"].State != StateApplied {
		t.Errorf("first = %+v; it stays applied, so the next check tries again", states["first"])
	}
}
