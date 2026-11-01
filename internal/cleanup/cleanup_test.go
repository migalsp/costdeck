package cleanup

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr/funcr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
)

var now = time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)

type sent struct{ messages []string }

func (s *sent) Send(_ context.Context, _ finopsv1.NotificationEvent, md string) ([]string, error) {
	s.messages = append(s.messages, md)
	return []string{"Slack"}, nil
}

func namespace(name string, age time.Duration, labels, annotations map[string]string) *corev1.Namespace {
	return &corev1.Namespace{Name: name, UID: types.UID("uid-" + name), Labels: labels, Annotations: annotations,
		CreationTimestamp: metav1.NewTime(now.Add(-age))}
}

func preview(extra ...map[string]string) map[string]string {
	out := map[string]string{"env": "preview"}
	for _, e := range extra {
		maps.Copy(out, e)
	}
	return out
}

const day = 24 * time.Hour

func warnedFor(expiry time.Time) map[string]string {
	return map[string]string{WarnedAnnotation: expiry.UTC().Format(time.RFC3339)}
}

var warnedOld = warnedFor(now.Add(-day))

func setup(t *testing.T, ttl finopsv1.NamespaceTTLConfig, objs ...client.Object) client.Client {
	t.Helper()
	t.Setenv("POD_NAMESPACE", "costdeck")
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = finopsv1.AddToScheme(scheme)
	cfg := &finopsv1.CostDeckConfig{Name: "default", Namespace: "costdeck", Spec: finopsv1.CostDeckConfigSpec{NamespaceTTL: ttl}}
	group := &finopsv1.ScalingGroup{Name: "shop", Namespace: "costdeck", Spec: finopsv1.ScalingGroupSpec{Namespaces: []string{"pr-named"}}}
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
		namespace("pr-old", 8*day, preview(), warnedOld),
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
		objs = append(objs, namespace(fmt.Sprintf("pr-%02d", i), 8*day, preview(), warnedOld))
	}
	c := setup(t, ttl(false), objs...)
	left := func(at time.Time) int {
		t.Helper()
		if err := (&Reaper{Client: c, Now: func() time.Time { return at }}).Run(context.Background()); err != nil {
			t.Fatal(err)
		}
		var list corev1.NamespaceList
		_ = c.List(context.Background(), &list)
		return len(list.Items)
	}
	if n := left(now); n != 5 {
		t.Errorf("%d left after the first run, want 5: at most 20 deletions an hour", n)
	}
	if n := left(now.Add(30 * time.Second)); n != 5 {
		t.Errorf("%d left 30 s later, want still 5: the limit is per hour, not per run", n)
	}
	if n := left(now.Add(time.Hour)); n != 0 {
		t.Errorf("%d left an hour later, want the rest deleted", n)
	}
}

func TestReaperStillWarnsOnceTheHourlyDeletionsAreUsedUp(t *testing.T) {
	ctx := context.Background()
	objs := make([]client.Object, 0, 23)
	for i := range 22 {
		objs = append(objs, namespace(fmt.Sprintf("pr-%02d", i), 8*day, preview(), warnedOld))
	}
	objs = append(objs, namespace("pr-soon", 7*day-12*time.Hour, preview(), nil))
	c := setup(t, ttl(false), objs...)
	s := &sent{}
	if err := (&Reaper{Client: c, Notify: s, Now: func() time.Time { return now }}).Run(ctx); err != nil {
		t.Fatal(err)
	}
	var soon corev1.Namespace
	if err := c.Get(ctx, client.ObjectKey{Name: "pr-soon"}, &soon); err != nil {
		t.Fatal(err)
	}
	if soon.Annotations[WarnedAnnotation] == "" {
		t.Error("a namespace that expires in 12 hours was not warned because the hour's 20 deletions were used up")
	}
	var list corev1.NamespaceList
	_ = c.List(ctx, &list)
	if len(list.Items) != 3 {
		t.Errorf("%d namespaces left, want the 2 expired ones over the limit and pr-soon", len(list.Items))
	}
}

func TestReaperWarnsBeforeDeletingANamespaceThatExpiredUnwarned(t *testing.T) {
	ctx := context.Background()
	c := setup(t, ttl(false), namespace("pr-old", 8*day, preview(), nil))
	s := &sent{}
	r := &Reaper{Client: c, Notify: s, Now: func() time.Time { return now }}
	if err := r.Run(ctx); err != nil {
		t.Fatal(err)
	}
	var ns corev1.Namespace
	if err := c.Get(ctx, client.ObjectKey{Name: "pr-old"}, &ns); err != nil {
		t.Fatalf("a namespace nobody was warned about was deleted at once: %v", err)
	}
	postponed := now.Add(day).Format(time.RFC3339)
	if ns.Annotations[ExpiresAnnotation] != postponed || ns.Annotations[WarnedAnnotation] != postponed {
		t.Errorf("annotations = %v, want the deletion moved to %s and the warning recorded for it", ns.Annotations, postponed)
	}
	if len(s.messages) != 1 || !strings.Contains(s.messages[0], "`pr-old` will be deleted at Fri 23 Oct 12:00 UTC") || !strings.Contains(s.messages[0], "before Cost Deck could warn") {
		t.Errorf("messages = %v", s.messages)
	}
	plan, err := Plan(ctx, c, ttl(false))
	if err != nil || len(plan) != 1 || !plan[0].ExpiresAt.Equal(now.Add(day)) || !plan[0].Warned {
		t.Errorf("plan = %+v, %v; the namespace page must show the new time", plan, err)
	}

	if err := (&Reaper{Client: c, Notify: s, Now: func() time.Time { return now.Add(day - time.Minute) }}).Run(ctx); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, client.ObjectKey{Name: "pr-old"}, &corev1.Namespace{}); err != nil {
		t.Fatalf("deleted before the warning lead was over: %v", err)
	}
	if err := (&Reaper{Client: c, Notify: s, Now: func() time.Time { return now.Add(day) }}).Run(ctx); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, client.ObjectKey{Name: "pr-old"}, &corev1.Namespace{}); !apierrors.IsNotFound(err) {
		t.Errorf("still there once the lead was over: %v", err)
	}
	if len(s.messages) != 2 || !strings.Contains(s.messages[1], "Deleted namespace `pr-old`") {
		t.Errorf("messages = %v", s.messages)
	}
}

func TestReaperKeepsANamespaceWhoseLateWarningFailed(t *testing.T) {
	c := setup(t, ttl(false), namespace("pr-old", 8*day, preview(), nil))
	err := (&Reaper{Client: c, Notify: &failingSender{}, Now: func() time.Time { return now }}).Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "warn about pr-old: slack is down") {
		t.Errorf("Run = %v", err)
	}
	var ns corev1.Namespace
	if err := c.Get(context.Background(), client.ObjectKey{Name: "pr-old"}, &ns); err != nil {
		t.Fatalf("a namespace whose warning failed was deleted: %v", err)
	}
	if ns.Annotations[WarnedAnnotation] != "" || ns.Annotations[ExpiresAnnotation] != "" {
		t.Errorf("annotations = %v; a warning that was not sent must not be recorded", ns.Annotations)
	}
}

type failingSender struct{ attempts int }

func (f *failingSender) Send(context.Context, finopsv1.NotificationEvent, string) ([]string, error) {
	f.attempts++
	return nil, errors.New("slack is down")
}

func TestReaperStartRunsRightAway(t *testing.T) {
	c := setup(t, ttl(false), namespace("pr-old", 8*day, preview(), warnedOld), namespace("pr-later", 2*day, preview(), nil))
	r := &Reaper{Client: c, Now: func() time.Time { return now }}
	if !r.NeedLeaderElection() {
		t.Fatal("the reaper must run on the leader only, so a namespace is deleted once")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Start(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	for c.Get(context.Background(), client.ObjectKey{Name: "pr-old"}, &corev1.Namespace{}) == nil {
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("the first run did not delete the expired namespace")
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
	if err := c.Get(context.Background(), client.ObjectKey{Name: "pr-later"}, &corev1.Namespace{}); err != nil {
		t.Errorf("a namespace that has not expired was deleted: %v", err)
	}
}

func TestPlanSkipsNamespacesBeingDeletedAndProtectsConfigTargets(t *testing.T) {
	going := namespace("pr-going", 8*day, preview(), nil)
	going.Finalizers = []string{"kubernetes"}
	going.DeletionTimestamp = &metav1.Time{Time: now}
	target := &finopsv1.ScalingConfig{Name: "pr-config", Namespace: "costdeck", Spec: finopsv1.ScalingConfigSpec{TargetNamespace: "pr-config"}}
	noTTL := finopsv1.NamespaceTTLConfig{Enabled: true, Selector: "env=preview"}
	c := setup(t, noTTL, going, target,
		namespace("pr-config", 8*day, preview(), nil),
		namespace("pr-plain", 8*day, preview(), nil),
		namespace("pr-label", 8*day, preview(map[string]string{TTLLabel: "3d"}), nil),
		namespace("pr-extended", 8*day, preview(), map[string]string{ExpiresAnnotation: "2026-10-25T00:00:00Z"}))

	plan, err := Plan(context.Background(), c, noTTL)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Entry{}
	for _, e := range plan {
		got[e.Namespace] = e
	}
	if _, listed := got["pr-going"]; listed || len(plan) != 4 {
		t.Fatalf("plan = %+v; a namespace already being deleted must not be listed", plan)
	}
	for ns, want := range map[string]string{"pr-config": "a schedule lists it by name", "pr-plain": "no time to live is set", "pr-label": "", "pr-extended": ""} {
		if got[ns].Protected != want {
			t.Errorf("%s protected = %q, want %q", ns, got[ns].Protected, want)
		}
	}
	if _, err := Plan(context.Background(), c, finopsv1.NamespaceTTLConfig{Selector: ""}); err == nil {
		t.Error("an empty selector was planned")
	}
}

func TestReaperDeletesOnlyTheNamespaceItPlanned(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name       string
		deleteErr  error
		wantErr    string
		wantRecord bool
	}{
		{"deleted", nil, "", true},
		{"recreated under the same name in between", apierrors.NewConflict(corev1.Resource("namespaces"), "pr-old", errors.New("UID mismatch")), "", false},
		{"already gone", apierrors.NewNotFound(corev1.Resource("namespaces"), "pr-old"), "", false},
		{"refused", apierrors.NewForbidden(corev1.Resource("namespaces"), "pr-old", errors.New("no")), "delete namespace pr-old:", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("POD_NAMESPACE", "costdeck")
			var opts client.DeleteOptions
			scheme := runtime.NewScheme()
			_ = clientgoscheme.AddToScheme(scheme)
			_ = finopsv1.AddToScheme(scheme)
			cfg := &finopsv1.CostDeckConfig{Name: "default", Namespace: "costdeck", Spec: finopsv1.CostDeckConfigSpec{NamespaceTTL: ttl(false)}}
			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cfg, namespace("pr-old", 8*day, preview(), warnedOld)).
				WithInterceptorFuncs(interceptor.Funcs{Delete: func(ctx context.Context, w client.WithWatch, obj client.Object, o ...client.DeleteOption) error {
					opts.ApplyOptions(o)
					if tc.deleteErr != nil {
						return tc.deleteErr
					}
					return w.Delete(ctx, obj, o...)
				}}).Build()
			s := &sent{}
			err := (&Reaper{Client: c, Notify: s, Now: func() time.Time { return now }}).Run(ctx)
			if (tc.wantErr == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("Run = %v, want error %q", err, tc.wantErr)
			}
			if opts.Preconditions == nil || opts.Preconditions.UID == nil || *opts.Preconditions.UID != "uid-pr-old" {
				t.Errorf("delete preconditions = %+v, want the UID the plan saw", opts.Preconditions)
			}
			if opts.PropagationPolicy == nil || *opts.PropagationPolicy != metav1.DeletePropagationBackground {
				t.Errorf("propagation = %v, want background", opts.PropagationPolicy)
			}
			deleted, _ := Deleted(ctx, c)
			if (len(deleted) == 1) != tc.wantRecord || (len(s.messages) == 1) != tc.wantRecord {
				t.Errorf("record %+v, messages %v; want a record and an announcement only for a real deletion", deleted, s.messages)
			}
		})
	}
}

func TestReaperWarningLeadAndFailures(t *testing.T) {
	ctx := context.Background()
	with := func(warnBefore time.Duration) finopsv1.NamespaceTTLConfig {
		setting := ttl(false)
		setting.WarnBefore = &metav1.Duration{Duration: warnBefore}
		return setting
	}
	objs := func() []client.Object {
		return []client.Object{
			namespace("pr-three-days", 4*day+time.Hour, preview(), nil),
			namespace("pr-five-days", 2*day, preview(), nil),
		}
	}
	warned := func(c client.Client, name string) bool {
		var ns corev1.Namespace
		_ = c.Get(ctx, client.ObjectKey{Name: name}, &ns)
		return ns.Annotations[WarnedAnnotation] != ""
	}

	c := setup(t, with(72*time.Hour), objs()...)
	s := &sent{}
	if err := (&Reaper{Client: c, Notify: s, Now: func() time.Time { return now }}).Run(ctx); err != nil {
		t.Fatal(err)
	}
	if !warned(c, "pr-three-days") || warned(c, "pr-five-days") || len(s.messages) != 1 || !strings.Contains(s.messages[0], "`pr-three-days` will be deleted") {
		t.Errorf("a 72h lead: messages %v", s.messages)
	}

	off := setup(t, with(0), objs()...)
	s = &sent{}
	if err := (&Reaper{Client: off, Notify: s, Now: func() time.Time { return now.Add(2 * day) }}).Run(ctx); err != nil || len(s.messages) != 0 || warned(off, "pr-three-days") {
		t.Errorf("a zero lead warned: %v %v", err, s.messages)
	}

	down := setup(t, with(72*time.Hour), objs()...)
	failing := &failingSender{}
	err := (&Reaper{Client: down, Notify: failing, Now: func() time.Time { return now }}).Run(ctx)
	if err == nil || !strings.Contains(err.Error(), "warn about pr-three-days: slack is down") || warned(down, "pr-three-days") {
		t.Errorf("Run = %v; a warning that was not sent must not be marked as sent", err)
	}
}

func TestReaperLogsAFailedAnnouncementAndKeepsTheRecord(t *testing.T) {
	c := setup(t, ttl(false), namespace("pr-old", 8*day, preview(), warnedOld))
	var logged []string
	ctx := logf.IntoContext(context.Background(), funcr.New(func(prefix, args string) { logged = append(logged, prefix+" "+args) }, funcr.Options{}))
	if err := (&Reaper{Client: c, Notify: &failingSender{}, Now: func() time.Time { return now }}).Run(ctx); err != nil {
		t.Fatalf("Run = %v; a failed announcement must not fail the run", err)
	}
	if deleted, err := Deleted(ctx, c); err != nil || len(deleted) != 1 {
		t.Errorf("record = %+v, %v", deleted, err)
	}
	if !strings.Contains(strings.Join(logged, "\n"), "Could not announce a deleted namespace") {
		t.Errorf("log = %q", logged)
	}
}

func TestDescribe(t *testing.T) {
	created := now.Add(-time.Hour)
	for _, tc := range []struct {
		entry Entry
		want  string
	}{
		{Entry{Source: SourceExtended}, "at the time it was extended to"},
		{Entry{Source: SourceLabel}, "after the time its costdeck.io/ttl label gives"},
		{Entry{Source: SourceTTL, CreatedAt: created, ExpiresAt: created.Add(3 * day)}, "3d after it was created"},
		{Entry{Source: SourceTTL, CreatedAt: created, ExpiresAt: created.Add(36 * time.Hour)}, "36h0m0s after it was created"},
	} {
		if got := describe(tc.entry); got != tc.want {
			t.Errorf("describe(%+v) = %q, want %q", tc.entry, got, tc.want)
		}
	}
}

func TestDeletedRecord(t *testing.T) {
	ctx := context.Background()
	corrupt := &corev1.ConfigMap{Name: logConfigMap, Namespace: "costdeck", Data: map[string]string{logKey: "[{"}}
	if _, err := Deleted(ctx, setup(t, ttl(false), corrupt)); err == nil {
		t.Error("a record that does not parse was read as empty")
	}

	c := setup(t, ttl(false), &corev1.ConfigMap{Name: logConfigMap, Namespace: "costdeck"})
	for i := range logSize + 3 {
		if err := record(ctx, c, Deletion{Namespace: fmt.Sprintf("pr-%03d", i), DeletedAt: now.Add(time.Duration(i) * time.Minute)}); err != nil {
			t.Fatal(err)
		}
	}
	list, err := Deleted(ctx, c)
	if err != nil || len(list) != logSize || list[0].Namespace != fmt.Sprintf("pr-%03d", logSize+2) || list[logSize-1].Namespace != "pr-003" {
		t.Errorf("record has %d entries from %q to %q, %v; want the newest %d, newest first", len(list), list[0].Namespace, list[len(list)-1].Namespace, err, logSize)
	}
}

func TestExtendFromNowOnceExpired(t *testing.T) {
	ctx := context.Background()
	c := setup(t, ttl(false), namespace("pr-old", 8*day, preview(), nil))
	e, err := Extend(ctx, c, ttl(false), "pr-old", day, now)
	if err != nil || !e.ExpiresAt.Equal(now.Add(day)) || e.Source != SourceExtended {
		t.Errorf("extend an expired namespace = %+v, %v; want a day from now, not from its past expiry", e, err)
	}
	if _, err := Extend(ctx, c, ttl(false), "pr-missing", day, now); !apierrors.IsNotFound(err) {
		t.Errorf("extend a namespace the setting does not select = %v, want not found", err)
	}
}
