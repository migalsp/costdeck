package scaling

import (
	"context"
	"slices"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
)

var (
	cnpgGVK    = schema.GroupVersionKind{Group: "postgresql.cnpg.io", Version: "v1", Kind: "Cluster"}
	zalandoGVK = schema.GroupVersionKind{Group: "acid.zalan.do", Version: "v1", Kind: "postgresql"}
	bridgeGVK  = schema.GroupVersionKind{Group: "kafka.strimzi.io", Version: "v1beta2", Kind: "KafkaBridge"}
	redisGVK   = schema.GroupVersionKind{Group: "redis.example.com", Version: "v1", Kind: "Redis"}
)

func service(gvk schema.GroupVersionKind, name string, body map[string]any) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: body}
	u.SetGroupVersionKind(gvk)
	u.SetName(name)
	u.SetNamespace("test-ns")
	return u
}

// hibernateEngine knows the given kinds, as if their operators were installed.
func hibernateEngine(t *testing.T, kinds []schema.GroupVersionKind, funcs *interceptor.Funcs, objs ...client.Object) *Engine {
	t.Helper()
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	mapper := meta.NewDefaultRESTMapper(nil)
	for _, gvk := range append([]schema.GroupVersionKind{appsv1.SchemeGroupVersion.WithKind("Deployment"), appsv1.SchemeGroupVersion.WithKind("StatefulSet"),
		corev1.SchemeGroupVersion.WithKind("Pod")}, kinds...) {
		mapper.Add(gvk, meta.RESTScopeNamespace)
	}
	b := fake.NewClientBuilder().WithScheme(scheme).WithRESTMapper(mapper).WithObjects(objs...)
	if funcs != nil {
		b = b.WithInterceptorFuncs(*funcs)
	}
	return &Engine{Client: b.Build()}
}

func reread(t *testing.T, e *Engine, u *unstructured.Unstructured) *unstructured.Unstructured {
	t.Helper()
	out := &unstructured.Unstructured{}
	out.SetGroupVersionKind(u.GroupVersionKind())
	if err := e.Client.Get(context.Background(), client.ObjectKeyFromObject(u), out); err != nil {
		t.Fatal(err)
	}
	return out
}

// setStatus replaces a resource's status, as its operator would.
func setStatus(t *testing.T, e *Engine, u *unstructured.Unstructured, status map[string]any) {
	t.Helper()
	cur := reread(t, e, u)
	cur.Object["status"] = status
	if err := e.Client.Update(context.Background(), cur); err != nil {
		t.Fatal(err)
	}
}

func hibernate(t *testing.T, e *Engine, active bool, rules ...finopsv1.HibernateRule) bool {
	t.Helper()
	ready, err := e.HibernateServices(context.Background(), "test-ns", active, rules)
	if err != nil {
		t.Fatal(err)
	}
	return ready
}

func conditions(kv ...string) []any {
	var out []any
	for i := 0; i < len(kv); i += 2 {
		out = append(out, map[string]any{"type": kv[i], "status": kv[i+1]})
	}
	return out
}

func TestValidateHibernate(t *testing.T) {
	ok := []finopsv1.HibernateRule{
		{Preset: "cnpg"}, {Preset: "zalando-postgres", Names: []string{"pg-*"}}, {Preset: "strimzi"},
		{APIVersion: "redis.example.com/v1", Kind: "Redis", ReplicasField: "spec.size", ReadyField: "status.readyNodes"},
	}
	if err := ValidateHibernate(ok); err != nil {
		t.Errorf("valid rules refused: %v", err)
	}
	bad := map[string]finopsv1.HibernateRule{
		"unknown preset":            {Preset: "mysql"},
		"a preset with fields":      {Preset: "cnpg", ReplicasField: "spec.instances"},
		"no kind":                   {APIVersion: "redis.example.com/v1", ReplicasField: "spec.size"},
		"a core kind":               {APIVersion: "v1", Kind: "Pod", ReplicasField: "spec.replicas"},
		"not a group version":       {APIVersion: "a/b/c", Kind: "X", ReplicasField: "spec.replicas"},
		"a field in metadata":       {APIVersion: "x.io/v1", Kind: "X", ReplicasField: "metadata.generation"},
		"a field with no dot":       {APIVersion: "x.io/v1", Kind: "X", ReplicasField: "replicas"},
		"an empty path segment":     {APIVersion: "x.io/v1", Kind: "X", ReplicasField: "spec..replicas"},
		"a bad ready field":         {APIVersion: "x.io/v1", Kind: "X", ReplicasField: "spec.replicas", ReadyField: "ready"},
		"a broken glob":             {Preset: "cnpg", Names: []string{"pg-["}},
		"an empty name":             {Preset: "cnpg", Names: []string{""}},
		"neither preset nor fields": {},
	}
	for name, r := range bad {
		if err := ValidateHibernate([]finopsv1.HibernateRule{{Preset: "cnpg"}, r}); err == nil || !strings.HasPrefix(err.Error(), "hibernate rule 2:") {
			t.Errorf("%s: got %v, want an error naming rule 2", name, err)
		}
	}
}

func TestExclusionsWith(t *testing.T) {
	own := []string{"keep-me"}
	got := ExclusionsWith(own, []finopsv1.HibernateRule{{Preset: "cnpg"}, {Preset: "strimzi"}, {Preset: "cnpg"},
		{APIVersion: "redis.example.com/v1", Kind: "Redis", ReplicasField: "spec.size"}})
	want := []string{"keep-me", "owner:Cluster", "owner:KafkaConnect", "owner:KafkaMirrorMaker2", "owner:KafkaBridge", "owner:Redis"}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if len(own) != 1 {
		t.Errorf("the schedule's own exclusions were changed: %v", own)
	}
	if got := ExclusionsWith(nil, []finopsv1.HibernateRule{{Preset: "zalando-postgres"}}); !slices.Equal(got, []string{"owner:postgresql"}) {
		t.Errorf("zalando: %v", got)
	}
}

func TestOwnerExclusionLeavesOperatorWorkloadsAlone(t *testing.T) {
	controller := func(kind string, isController bool) []metav1.OwnerReference {
		return []metav1.OwnerReference{{APIVersion: "x/v1", Kind: kind, Name: "db", UID: "1", Controller: new(isController)}}
	}
	spilo := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "test-ns", OwnerReferences: controller("postgresql", true)}}
	adopted := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "helper", Namespace: "test-ns", OwnerReferences: controller("postgresql", false)}}
	app := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "test-ns"}}
	e := hibernateEngine(t, nil, nil, spilo, adopted, app)
	objs, err := e.listScalableResources(context.Background(), "test-ns", ExclusionsWith(nil, []finopsv1.HibernateRule{{Preset: "zalando-postgres"}}))
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(objs))
	for _, o := range objs {
		names = append(names, o.GetName())
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"app", "helper"}) {
		t.Errorf("scalable = %v, want the operator's StatefulSet left out (and only it: helper's owner is not its controller)", names)
	}
	if !excludedObject(spilo, []string{" owner:Postgresql "}) {
		t.Error("owner: entries should match the kind case-insensitively")
	}
}

func TestHibernateCNPG(t *testing.T) {
	healthy := map[string]any{"instances": int64(3), "readyInstances": int64(3), "conditions": conditions("Ready", "True")}
	db := service(cnpgGVK, "db", map[string]any{"spec": map[string]any{"instances": int64(3)}, "status": healthy})
	theirs := service(cnpgGVK, "theirs", map[string]any{"spec": map[string]any{"instances": int64(1)}})
	theirs.SetAnnotations(map[string]string{cnpgHibernation: "on"})
	e := hibernateEngine(t, []schema.GroupVersionKind{cnpgGVK}, nil, db, theirs)
	rule := finopsv1.HibernateRule{Preset: "cnpg"}

	if hibernate(t, e, false, rule) {
		t.Error("stopped before the operator reported the cluster hibernated")
	}
	got := reread(t, e, db).GetAnnotations()
	if got[cnpgHibernation] != "on" || got[HibernatedAnnotation] != hibernatedStopped {
		t.Fatalf("annotations after stopping: %v", got)
	}
	if reread(t, e, theirs).GetAnnotations()[HibernatedAnnotation] != "" {
		t.Error("a cluster someone else hibernated was claimed")
	}
	// A hibernated cluster can still say Ready: only the hibernation condition counts.
	setStatus(t, e, db, map[string]any{"instances": int64(3), "readyInstances": int64(0), "conditions": conditions("Ready", "True", cnpgHibernation, "True")})
	if !hibernate(t, e, false, rule) {
		t.Error("not stopped once the hibernation condition is True")
	}

	if hibernate(t, e, true, rule) {
		t.Error("started while the cluster is still hibernated")
	}
	got = reread(t, e, db).GetAnnotations()
	if _, on := got[cnpgHibernation]; on || got[HibernatedAnnotation] != hibernatedStarting {
		t.Fatalf("annotations after starting: %v", got)
	}
	setStatus(t, e, db, map[string]any{"instances": int64(3), "readyInstances": int64(2), "conditions": conditions("Ready", "True")})
	if hibernate(t, e, true, rule) {
		t.Error("started with two of three instances ready")
	}
	setStatus(t, e, db, healthy)
	if !hibernate(t, e, true, rule) {
		t.Error("not started with every instance ready")
	}
	if got := reread(t, e, db).GetAnnotations(); got[HibernatedAnnotation] != "" {
		t.Errorf("CostDeck's mark stayed after the start: %v", got)
	}
	if reread(t, e, theirs).GetAnnotations()[cnpgHibernation] != "on" {
		t.Error("a cluster someone else hibernated was woken")
	}
}

func TestHibernateZalandoRestoresTheCount(t *testing.T) {
	pg := service(zalandoGVK, "pg", map[string]any{"spec": map[string]any{"numberOfInstances": int64(2)}})
	sts := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: "pg", Namespace: "test-ns"}, Status: appsv1.StatefulSetStatus{Replicas: 2, ReadyReplicas: 2}}
	e := hibernateEngine(t, []schema.GroupVersionKind{zalandoGVK}, nil, pg, sts)
	rule := finopsv1.HibernateRule{Preset: "zalando-postgres"}
	instances := func() int64 { n, _ := intField(reread(t, e, pg), "spec.numberOfInstances"); return n }
	setSts := func(replicas, ready int32) {
		t.Helper()
		cur := &appsv1.StatefulSet{}
		if err := e.Client.Get(context.Background(), client.ObjectKeyFromObject(sts), cur); err != nil {
			t.Fatal(err)
		}
		cur.Status.Replicas, cur.Status.ReadyReplicas = replicas, ready
		if err := e.Client.Status().Update(context.Background(), cur); err != nil {
			t.Fatal(err)
		}
	}

	if hibernate(t, e, false, rule) {
		t.Error("stopped while the StatefulSet still runs pods")
	}
	if instances() != 0 || reread(t, e, pg).GetAnnotations()[OriginalReplicasAnnotation] != "2" {
		t.Fatalf("after stopping: numberOfInstances %d, annotations %v", instances(), reread(t, e, pg).GetAnnotations())
	}
	setSts(0, 0)
	if !hibernate(t, e, false, rule) {
		t.Error("not stopped once the StatefulSet has no pods")
	}

	if hibernate(t, e, true, rule) || instances() != 2 {
		t.Fatalf("starting: numberOfInstances %d, want 2 and not ready yet", instances())
	}
	// A second pass while it starts must not lose the count (the record is gone by then).
	if hibernate(t, e, true, rule) || instances() != 2 {
		t.Fatalf("a second pass while starting set numberOfInstances to %d", instances())
	}
	setSts(2, 2)
	if !hibernate(t, e, true, rule) {
		t.Error("not started with both pods ready")
	}
}

func TestHibernateStrimziUsesTheServedVersionAndPods(t *testing.T) {
	bridge := service(bridgeGVK, "bridge", map[string]any{"spec": map[string]any{"replicas": int64(2)}, "status": map[string]any{"conditions": conditions("Ready", "True")}})
	pod := func(name string, ready bool) *corev1.Pod {
		status := corev1.ConditionFalse
		if ready {
			status = corev1.ConditionTrue
		}
		return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "test-ns", Labels: map[string]string{"strimzi.io/cluster": "bridge", "strimzi.io/kind": "KafkaBridge"}},
			Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: status}}}}
	}
	// Only v1beta2 is served, as before Strimzi 0.49.
	e := hibernateEngine(t, []schema.GroupVersionKind{bridgeGVK}, nil, bridge, pod("bridge-a", true))
	rule := finopsv1.HibernateRule{Preset: "strimzi"}
	replicas := func() int64 { n, _ := intField(reread(t, e, bridge), "spec.replicas"); return n }

	if hibernate(t, e, false, rule) || replicas() != 0 {
		t.Fatalf("stopping: replicas %d, want 0 and not stopped while a pod runs", replicas())
	}
	if err := e.Client.DeleteAllOf(context.Background(), &corev1.Pod{}, client.InNamespace("test-ns")); err != nil {
		t.Fatal(err)
	}
	if !hibernate(t, e, false, rule) {
		t.Error("not stopped once its pods are gone")
	}
	// Strimzi reports a bridge with no replicas Ready: that alone must not count as started.
	if hibernate(t, e, true, rule) || replicas() != 2 {
		t.Fatalf("starting: replicas %d, want 2 and not ready without pods", replicas())
	}
	for _, p := range []*corev1.Pod{pod("bridge-a", true), pod("bridge-b", false)} {
		if err := e.Client.Create(context.Background(), p); err != nil {
			t.Fatal(err)
		}
	}
	if hibernate(t, e, true, rule) {
		t.Error("started with one of two pods ready")
	}
	ready := pod("bridge-b", true)
	ready.ResourceVersion = ""
	if err := e.Client.Delete(context.Background(), pod("bridge-b", false)); err != nil {
		t.Fatal(err)
	}
	if err := e.Client.Create(context.Background(), ready); err != nil {
		t.Fatal(err)
	}
	if !hibernate(t, e, true, rule) {
		t.Error("not started with both pods ready")
	}
}

func TestHibernateGenericRuleAndNames(t *testing.T) {
	cache := service(redisGVK, "cache-a", map[string]any{"spec": map[string]any{"size": int64(3)}, "status": map[string]any{"readyNodes": int64(3)}})
	other := service(redisGVK, "sessions", map[string]any{"spec": map[string]any{"size": int64(1)}})
	e := hibernateEngine(t, []schema.GroupVersionKind{redisGVK}, nil, cache, other)
	rule := finopsv1.HibernateRule{APIVersion: "redis.example.com/v1", Kind: "Redis", ReplicasField: "spec.size", ReadyField: "status.readyNodes", Names: []string{"cache-*"}}
	size := func(u *unstructured.Unstructured) int64 { n, _ := intField(reread(t, e, u), "spec.size"); return n }

	if hibernate(t, e, false, rule) || size(cache) != 0 || size(other) != 1 {
		t.Fatalf("stopping: cache-a %d, sessions %d", size(cache), size(other))
	}
	setStatus(t, e, cache, map[string]any{"readyNodes": int64(0)})
	if !hibernate(t, e, false, rule) {
		t.Error("not stopped with no ready nodes")
	}
	if hibernate(t, e, true, rule) || size(cache) != 3 {
		t.Fatalf("starting: size %d", size(cache))
	}
	setStatus(t, e, cache, map[string]any{"readyNodes": int64(3)})
	if !hibernate(t, e, true, rule) {
		t.Error("not started with every node ready")
	}

	// Without readyField a resource counts as there once patched.
	noReady := rule
	noReady.ReadyField = ""
	if !hibernate(t, e, false, noReady) || size(cache) != 0 {
		t.Errorf("without readyField: stopped=%d", size(cache))
	}
}

func TestHibernateSkipsMissingOperatorsButNotMissingPermissions(t *testing.T) {
	e := hibernateEngine(t, nil, nil)
	if !hibernate(t, e, false, finopsv1.HibernateRule{Preset: "cnpg"}, finopsv1.HibernateRule{Preset: "strimzi"}) {
		t.Error("kinds that are not installed should not hold a namespace back")
	}
	forbidden := &interceptor.Funcs{List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
		if _, ok := list.(*unstructured.UnstructuredList); ok {
			return apierrors.NewForbidden(schema.GroupResource{Group: "postgresql.cnpg.io", Resource: "clusters"}, "", nil)
		}
		return c.List(ctx, list, opts...)
	}}
	e = hibernateEngine(t, []schema.GroupVersionKind{cnpgGVK}, forbidden)
	if _, err := e.HibernateServices(context.Background(), "test-ns", false, []finopsv1.HibernateRule{{Preset: "cnpg"}}); !apierrors.IsForbidden(err) {
		t.Errorf("a missing permission must surface, got %v", err)
	}
}

func TestScaleWithServicesOrder(t *testing.T) {
	ctx := context.Background()
	app := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "test-ns"}, Spec: appsv1.DeploymentSpec{Replicas: new(int32(2))},
		Status: appsv1.DeploymentStatus{Replicas: 2, ReadyReplicas: 2}}
	db := service(cnpgGVK, "db", map[string]any{"spec": map[string]any{"instances": int64(1)},
		"status": map[string]any{"readyInstances": int64(1), "conditions": conditions("Ready", "True")}})
	e := hibernateEngine(t, []schema.GroupVersionKind{cnpgGVK}, nil, app, db)
	rules := []finopsv1.HibernateRule{{Preset: "cnpg"}}
	scale := func(active bool) bool {
		t.Helper()
		_, ready, err := e.ScaleWithServices(ctx, "test-ns", active, nil, ExclusionsWith(nil, rules), rules, nil, false)
		if err != nil {
			t.Fatal(err)
		}
		return ready
	}
	deployment := func() *appsv1.Deployment {
		d := &appsv1.Deployment{}
		if err := e.Client.Get(ctx, client.ObjectKeyFromObject(app), d); err != nil {
			t.Fatal(err)
		}
		return d
	}
	setReady := func(n int32) {
		d := deployment()
		d.Status.Replicas, d.Status.ReadyReplicas = n, n
		if err := e.Client.Status().Update(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	hibernating := func() bool { return reread(t, e, db).GetAnnotations()[cnpgHibernation] == "on" }

	if scale(false) || *deployment().Spec.Replicas != 0 || hibernating() {
		t.Fatal("the database must wait until the workloads are down")
	}
	setReady(0)
	if scale(false) || !hibernating() {
		t.Fatal("the database is stopped once the workloads are down, and the namespace waits for it")
	}
	setStatus(t, e, db, map[string]any{"readyInstances": int64(0), "conditions": conditions(cnpgHibernation, "True")})
	if !scale(false) {
		t.Fatal("down once the database is hibernated")
	}

	if scale(true) || hibernating() || *deployment().Spec.Replicas != 0 {
		t.Fatal("the database starts first and the workloads wait for it")
	}
	setStatus(t, e, db, map[string]any{"readyInstances": int64(1), "conditions": conditions("Ready", "True")})
	if scale(true) || *deployment().Spec.Replicas != 2 {
		t.Fatalf("the workloads start once the database is ready: replicas %d", *deployment().Spec.Replicas)
	}
}

func TestReleaseAndListServices(t *testing.T) {
	ctx := context.Background()
	db := service(cnpgGVK, "db", map[string]any{"spec": map[string]any{"instances": int64(1)}})
	db.SetAnnotations(map[string]string{cnpgHibernation: "on", HibernatedAnnotation: hibernatedStopped})
	theirs := service(cnpgGVK, "theirs", map[string]any{"spec": map[string]any{"instances": int64(1)}})
	theirs.SetAnnotations(map[string]string{cnpgHibernation: "on"})
	running := service(cnpgGVK, "analytics", map[string]any{"spec": map[string]any{"instances": int64(1)}})
	e := hibernateEngine(t, []schema.GroupVersionKind{cnpgGVK}, nil, db, theirs, running)
	rules := []finopsv1.HibernateRule{{Preset: "cnpg"}, {Preset: "cnpg", Names: []string{"db"}}}

	got, err := e.Services(ctx, "test-ns", rules)
	if err != nil {
		t.Fatal(err)
	}
	want := []HibernatedService{{Kind: "Cluster", Name: "analytics", Running: true}, {Kind: "Cluster", Name: "db", StoppedByCostDeck: true}, {Kind: "Cluster", Name: "theirs"}}
	if !slices.Equal(got, want) {
		t.Errorf("services = %+v, want %+v (overlapping rules list a cluster once)", got, want)
	}
	if n, _ := e.ServicesToChange(ctx, "test-ns", false, rules); n != 1 {
		t.Errorf("to stop = %d, want 1 (analytics)", n)
	}
	if n, _ := e.ServicesToChange(ctx, "test-ns", true, rules); n != 1 {
		t.Errorf("to start = %d, want 1 (db; theirs is not CostDeck's)", n)
	}

	if err := e.ReleaseServices(ctx, "test-ns", rules); err != nil {
		t.Fatal(err)
	}
	if a := reread(t, e, db).GetAnnotations(); a[cnpgHibernation] != "" || a[HibernatedAnnotation] != "" {
		t.Errorf("a released cluster keeps %v", a)
	}
	if reread(t, e, theirs).GetAnnotations()[cnpgHibernation] != "on" {
		t.Error("release woke a cluster someone else hibernated")
	}
}
