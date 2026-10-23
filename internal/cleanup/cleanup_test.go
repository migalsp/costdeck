package cleanup

import (
	"context"
	"fmt"
	"maps"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
)

var now = time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)

type sent struct{ messages []string }

func (s *sent) Send(_ context.Context, _ finopsv1.NotificationEvent, md string) ([]string, error) {
	s.messages = append(s.messages, md)
	return []string{"Slack"}, nil
}

func namespace(name string, age time.Duration, labels, annotations map[string]string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID("uid-" + name), Labels: labels, Annotations: annotations,
		CreationTimestamp: metav1.NewTime(now.Add(-age))}}
}

func preview(extra ...map[string]string) map[string]string {
	out := map[string]string{"env": "preview"}
	for _, e := range extra {
		maps.Copy(out, e)
	}
	return out
}

const day = 24 * time.Hour

func setup(t *testing.T, ttl finopsv1.NamespaceTTLConfig, objs ...client.Object) client.Client {
	t.Helper()
	t.Setenv("POD_NAMESPACE", "costdeck")
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = finopsv1.AddToScheme(scheme)
	cfg := &finopsv1.CostDeckConfig{ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: "costdeck"}, Spec: finopsv1.CostDeckConfigSpec{NamespaceTTL: ttl}}
	group := &finopsv1.ScalingGroup{ObjectMeta: metav1.ObjectMeta{Name: "shop", Namespace: "costdeck"}, Spec: finopsv1.ScalingGroupSpec{Namespaces: []string{"pr-named"}}}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(append(objs, cfg, group)...).Build()
}

// ttl is the setting, switched on, deleting or in a dry run.
func ttl(dryRun bool) finopsv1.NamespaceTTLConfig {
	return finopsv1.NamespaceTTLConfig{Enabled: true, DryRun: dryRun, Selector: "env=preview", TTL: &metav1.Duration{Duration: 7 * day}}
}

func TestParseTTLAndSelector(t *testing.T) {
	for in, want := range map[string]time.Duration{"3d": 3 * day, "36h": 36 * time.Hour, "90m": 90 * time.Minute} {
		if d, off, err := ParseTTL(in); err != nil || off || d != want {
			t.Errorf("%s = %v, %v, %v", in, d, off, err)
		}
	}
	if _, off, err := ParseTTL("off"); !off || err != nil {
		t.Error("off is not off")
	}
	for _, bad := range []string{"soon", "0d", "-1h", "3w"} {
		if _, _, err := ParseTTL(bad); err == nil {
			t.Errorf("%s was accepted", bad)
		}
	}
	if _, err := Selector(" "); err == nil {
		t.Error("an empty selector was accepted")
	}
	if _, err := Selector("env in (preview"); err == nil {
		t.Error("a broken selector was accepted")
	}
}

func TestPlanProtectsWhatMustStay(t *testing.T) {
	ctx := context.Background()
	c := setup(t, ttl(false),
		namespace("pr-old", 8*day, preview(), nil),
		namespace("pr-label", 2*day, preview(map[string]string{TTLLabel: "3d"}), nil),
		namespace("pr-kept", 30*day, preview(map[string]string{TTLLabel: "off"}), nil),
		namespace("pr-bad", 30*day, preview(map[string]string{TTLLabel: "soon"}), nil),
		namespace("pr-extended", 30*day, preview(), map[string]string{ExpiresAnnotation: "2026-10-25T00:00:00Z"}),
		namespace("pr-named", 30*day, preview(), nil),
		namespace("kube-preview", 30*day, preview(), nil),
		namespace("costdeck", 30*day, preview(), nil),
		namespace("unrelated", 30*day, nil, nil),
	)
	plan, err := Plan(ctx, c, ttl(false))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Entry{}
	for _, e := range plan {
		got[e.Namespace] = e
	}
	if _, ok := got["unrelated"]; ok || len(plan) != 8 {
		t.Fatalf("plan = %+v", plan)
	}
	if e := got["pr-old"]; e.Protected != "" || !e.ExpiresAt.Equal(now.Add(-day)) || e.Source != SourceTTL {
		t.Errorf("pr-old = %+v", e)
	}
	if e := got["pr-label"]; e.Source != SourceLabel || !e.ExpiresAt.Equal(now.Add(day)) {
		t.Errorf("pr-label = %+v", e)
	}
	if e := got["pr-extended"]; e.Source != SourceExtended || e.ExpiresAt.Format(time.RFC3339) != "2026-10-25T00:00:00Z" {
		t.Errorf("pr-extended = %+v", e)
	}
	for ns, why := range map[string]string{"pr-kept": "=off keeps it", "pr-bad": "not a duration", "pr-named": "lists it by name", "kube-preview": "system", "costdeck": "system"} {
		if !strings.Contains(got[ns].Protected, why) {
			t.Errorf("%s protected = %q, want %q", ns, got[ns].Protected, why)
		}
	}
	if plan[0].Namespace != "pr-old" || plan[len(plan)-1].Protected == "" {
		t.Errorf("not soonest first with the protected ones last: %+v", plan)
	}
}

func TestReaperWarnsOnceDeletesAndRecords(t *testing.T) {
	ctx := context.Background()
	objs := []client.Object{
		namespace("pr-old", 8*day, preview(), nil),
		namespace("pr-soon", 6*day+time.Hour, preview(), nil), // 23h left, inside the 24h warning
		namespace("pr-later", 2*day, preview(), nil),
		namespace("pr-kept", 30*day, preview(map[string]string{TTLLabel: "off"}), nil),
	}
	exists := func(c client.Client, name string) bool {
		return c.Get(ctx, client.ObjectKey{Name: name}, &corev1.Namespace{}) == nil
	}

	dry := setup(t, ttl(true), objs...)
	s := &sent{}
	if err := (&Reaper{Client: dry, Notify: s, Now: func() time.Time { return now }}).Run(ctx); err != nil {
		t.Fatal(err)
	}
	if !exists(dry, "pr-old") || len(s.messages) != 0 {
		t.Fatalf("a dry run deleted or sent something: %v", s.messages)
	}

	c := setup(t, ttl(false), objs...)
	r := &Reaper{Client: c, Notify: s, Now: func() time.Time { return now }}
	if err := r.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if exists(c, "pr-old") || !exists(c, "pr-soon") || !exists(c, "pr-later") || !exists(c, "pr-kept") {
		t.Fatal("the wrong namespaces were deleted")
	}
	if len(s.messages) != 2 || !strings.Contains(s.messages[0], "Deleted namespace `pr-old`") || !strings.Contains(s.messages[1], "`pr-soon` will be deleted at Fri 23 Oct 11:00 UTC") {
		t.Fatalf("messages = %v", s.messages)
	}
	if !strings.Contains(s.messages[0], "7d after it was created") {
		t.Errorf("the deletion does not say why: %s", s.messages[0])
	}
	var soon corev1.Namespace
	_ = c.Get(ctx, client.ObjectKey{Name: "pr-soon"}, &soon)
	if soon.Annotations[WarnedAnnotation] == "" {
		t.Error("the warning is not recorded")
	}
	if err := r.Run(ctx); err != nil || len(s.messages) != 2 {
		t.Errorf("warned twice: %v %v", err, s.messages)
	}
	deleted, err := Deleted(ctx, c)
	if err != nil || len(deleted) != 1 || deleted[0].Namespace != "pr-old" || !deleted[0].DeletedAt.Equal(now) {
		t.Errorf("record = %+v, %v", deleted, err)
	}

	// Extending moves the deletion and warns again for the new time.
	e, err := Extend(ctx, c, ttl(false), "pr-soon", 3*day, now)
	if err != nil || e.Source != SourceExtended || !e.ExpiresAt.Equal(now.Add(23*time.Hour+3*day)) {
		t.Fatalf("extend = %+v, %v", e, err)
	}
	_ = c.Get(ctx, client.ObjectKey{Name: "pr-soon"}, &soon)
	if soon.Annotations[WarnedAnnotation] != "" || soon.Annotations[ExpiresAnnotation] == "" {
		t.Errorf("annotations after extending = %v", soon.Annotations)
	}
	if _, err := Extend(ctx, c, ttl(false), "pr-kept", day, now); err == nil {
		t.Error("a kept namespace was extended")
	}
}

func TestReaperDeletesAFewAtATime(t *testing.T) {
	objs := make([]client.Object, 0, 25)
	for i := range 25 {
		objs = append(objs, namespace(fmt.Sprintf("pr-%02d", i), 8*day, preview(), nil))
	}
	c := setup(t, ttl(false), objs...)
	if err := (&Reaper{Client: c, Now: func() time.Time { return now }}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	var left corev1.NamespaceList
	_ = c.List(context.Background(), &left)
	if len(left.Items) != 5 {
		t.Errorf("%d left, want 5: at most 20 deletions a run", len(left.Items))
	}
}
