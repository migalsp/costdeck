// Package cleanup deletes preview namespaces once they outlive their time to live
// (CostDeckConfig spec.namespaceTTL). It warns the notification channels first, deletes
// at most a few namespaces a run, and keeps a record of what it deleted.
package cleanup

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/config"
	"github.com/migalsp/costdeck-operator/internal/notify"
)

const (
	// TTLLabel on a namespace overrides the time to live ("3d", "36h") or keeps the
	// namespace ("off"). CI that creates preview namespaces can set it.
	TTLLabel = "costdeck.io/ttl"
	// ExpiresAnnotation is when an extended namespace is deleted (RFC 3339).
	ExpiresAnnotation = "costdeck.io/expires-at"
	// WarnedAnnotation records the expiry a warning was sent for, so an extension warns again.
	WarnedAnnotation = "costdeck.io/ttl-warned"

	logConfigMap      = "costdeck-namespace-ttl"
	logKey            = "deleted.json"
	logSize           = 100
	maxDeletions      = 20
	defaultWarnBefore = 24 * time.Hour
	interval          = 30 * time.Second
)

// Sources of a namespace's expiry.
const (
	SourceTTL      = "ttl"
	SourceLabel    = "label"
	SourceExtended = "extended"
)

// Entry is a namespace the setting selects.
type Entry struct {
	Namespace string    `json:"namespace"`
	CreatedAt time.Time `json:"createdAt"`
	ExpiresAt time.Time `json:"expiresAt"`
	Source    string    `json:"source"`
	Warned    bool      `json:"warned"`
	// Protected says why the namespace is never deleted; empty when it will be.
	Protected string `json:"protected,omitempty"`
	uid       types.UID
}

// Deletion is a namespace the setting deleted.
type Deletion struct {
	Namespace string    `json:"namespace"`
	CreatedAt time.Time `json:"createdAt"`
	ExpiresAt time.Time `json:"expiresAt"`
	DeletedAt time.Time `json:"deletedAt"`
}

// ParseTTL reads "off", a number of days ("3d") or a Go duration ("36h", "90m").
func ParseTTL(s string) (d time.Duration, off bool, err error) {
	s = strings.TrimSpace(s)
	switch {
	case s == "off":
		return 0, true, nil
	case strings.HasSuffix(s, "d"):
		days, err := strconv.Atoi(strings.TrimSuffix(s, "d"))
		if err != nil || days <= 0 {
			return 0, false, fmt.Errorf("%q is not a number of days", s)
		}
		return time.Duration(days) * 24 * time.Hour, false, nil
	}
	d, err = time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, false, fmt.Errorf("%q is not a duration such as 3d or 36h", s)
	}
	return d, false, nil
}

// Selector parses the setting's selector, refusing an empty one.
func Selector(s string) (labels.Selector, error) {
	sel, err := labels.Parse(strings.TrimSpace(s))
	if err != nil {
		return nil, err
	}
	if sel.Empty() {
		return nil, fmt.Errorf("the selector is empty and would match every namespace")
	}
	return sel, nil
}

// ScheduledByName lists the namespaces a schedule names: a ScalingGroup's spec.namespaces
// and a ScalingConfig's target. They are environments someone manages, never deleted.
func ScheduledByName(ctx context.Context, c client.Reader) (map[string]bool, error) {
	out := map[string]bool{}
	var groups finopsv1.ScalingGroupList
	if err := c.List(ctx, &groups, client.InNamespace(config.OperatorNamespace())); err != nil {
		return nil, err
	}
	for _, g := range groups.Items {
		for _, ns := range g.Spec.Namespaces {
			out[ns] = true
		}
	}
	var configs finopsv1.ScalingConfigList
	if err := c.List(ctx, &configs, client.InNamespace(config.OperatorNamespace())); err != nil {
		return nil, err
	}
	for _, sc := range configs.Items {
		out[sc.Spec.TargetNamespace] = true
	}
	return out, nil
}

// Plan lists the namespaces the setting selects with when each one is deleted, soonest
// first and the protected ones last. It reads the setting whether or not it is enabled,
// for the dashboard.
func Plan(ctx context.Context, c client.Reader, t finopsv1.NamespaceTTLConfig) ([]Entry, error) {
	sel, err := Selector(t.Selector)
	if err != nil {
		return nil, err
	}
	named, err := ScheduledByName(ctx, c)
	if err != nil {
		return nil, err
	}
	var list corev1.NamespaceList
	if err := c.List(ctx, &list, client.MatchingLabelsSelector{Selector: sel}); err != nil {
		return nil, err
	}
	var ttl time.Duration
	if t.TTL != nil {
		ttl = t.TTL.Duration
	}
	out := make([]Entry, 0, len(list.Items))
	for _, ns := range list.Items {
		if ns.DeletionTimestamp != nil {
			continue
		}
		e := Entry{Namespace: ns.Name, CreatedAt: ns.CreationTimestamp.Time, Source: SourceTTL, uid: ns.UID, Protected: protected(ns, named)}
		life := ttl
		if v, ok := ns.Labels[TTLLabel]; ok && e.Protected == "" {
			if d, _, err := ParseTTL(v); err == nil {
				life, e.Source = d, SourceLabel
			} else {
				e.Protected = "its " + TTLLabel + " label is not a duration"
			}
		}
		e.ExpiresAt = e.CreatedAt.Add(life)
		if at, err := time.Parse(time.RFC3339, ns.Annotations[ExpiresAnnotation]); err == nil {
			e.ExpiresAt, e.Source = at, SourceExtended
		}
		if life <= 0 && e.Source != SourceExtended && e.Protected == "" {
			e.Protected = "no time to live is set"
		}
		e.Warned = ns.Annotations[WarnedAnnotation] == e.ExpiresAt.UTC().Format(time.RFC3339)
		out = append(out, e)
	}
	// What will be deleted comes first, soonest first; what never is comes last.
	sort.Slice(out, func(i, j int) bool {
		if (out[i].Protected == "") != (out[j].Protected == "") {
			return out[i].Protected == ""
		}
		return out[i].ExpiresAt.Before(out[j].ExpiresAt)
	})
	return out, nil
}

// protected says why a namespace is never deleted.
func protected(ns corev1.Namespace, named map[string]bool) string {
	switch {
	case strings.HasPrefix(ns.Name, "kube-") || ns.Name == "default" || ns.Name == config.OperatorNamespace():
		return "a system namespace"
	case ns.Labels[TTLLabel] == "off":
		return TTLLabel + "=off keeps it"
	case named[ns.Name]:
		return "a schedule lists it by name"
	}
	return ""
}

// Sender delivers alerts; notify.Dispatcher does.
type Sender interface {
	Send(ctx context.Context, event finopsv1.NotificationEvent, markdown string) ([]string, error)
}

// Reaper warns about and deletes expired namespaces; the leader runs it.
type Reaper struct {
	Client client.Client
	Notify Sender
	Now    func() time.Time
}

// NeedLeaderElection makes only the leader delete namespaces.
func (r *Reaper) NeedLeaderElection() bool { return true }

// Start runs the reaper every 30 seconds.
func (r *Reaper) Start(ctx context.Context) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := r.Run(ctx); err != nil {
			logf.FromContext(ctx).WithName("namespace-ttl").Error(err, "Could not clean up preview namespaces")
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// Run sends due warnings and deletes expired namespaces, at most maxDeletions at a time
// so that a selector that matches too much cannot empty the cluster in one go.
func (r *Reaper) Run(ctx context.Context) error {
	cfg, err := config.Get(ctx, r.Client)
	if err != nil {
		return err
	}
	t := cfg.Spec.NamespaceTTL
	if !t.Enabled || t.DryRun {
		return nil
	}
	plan, err := Plan(ctx, r.Client, t)
	if err != nil {
		return err
	}
	now := time.Now()
	if r.Now != nil {
		now = r.Now()
	}
	warnBefore := defaultWarnBefore
	if t.WarnBefore != nil {
		warnBefore = t.WarnBefore.Duration
	}
	log := logf.FromContext(ctx).WithName("namespace-ttl")
	deleted := 0
	for _, e := range plan {
		switch {
		case e.Protected != "":
			continue
		case !now.Before(e.ExpiresAt):
			if deleted >= maxDeletions {
				log.Info("Left expired namespaces for the next run", "limit", maxDeletions)
				return nil
			}
			if err := r.delete(ctx, e, now); err != nil {
				return err
			}
			deleted++
			log.Info("Deleted an expired preview namespace", "namespace", e.Namespace, "expiresAt", e.ExpiresAt)
		case warnBefore > 0 && !e.Warned && !now.Before(e.ExpiresAt.Add(-warnBefore)):
			if err := r.warn(ctx, e); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *Reaper) warn(ctx context.Context, e Entry) error {
	msg := fmt.Sprintf("⏳ Namespace `%s` will be deleted at %s: preview cleanup deletes it %s. To keep it longer, extend it in Cost Deck (Settings → Preview cleanup) or label it %s=off.",
		e.Namespace, e.ExpiresAt.UTC().Format("Mon 2 Jan 15:04 MST"), describe(e), TTLLabel)
	if r.Notify != nil {
		if _, err := r.Notify.Send(ctx, notify.EventAlerts, msg); err != nil {
			return fmt.Errorf("warn about %s: %w", e.Namespace, err)
		}
	}
	patch := fmt.Sprintf(`{"metadata":{"annotations":{%q:%q}}}`, WarnedAnnotation, e.ExpiresAt.UTC().Format(time.RFC3339))
	ns := &corev1.Namespace{Name: e.Namespace}
	return r.Client.Patch(ctx, ns, client.RawPatch(types.MergePatchType, []byte(patch)))
}

func (r *Reaper) delete(ctx context.Context, e Entry, now time.Time) error {
	ns := &corev1.Namespace{Name: e.Namespace}
	// The UID precondition keeps a namespace recreated under the same name safe.
	err := r.Client.Delete(ctx, ns, client.PropagationPolicy(metav1.DeletePropagationBackground), client.Preconditions{UID: &e.uid})
	if apierrors.IsNotFound(err) || apierrors.IsConflict(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("delete namespace %s: %w", e.Namespace, err)
	}
	if err := record(ctx, r.Client, Deletion{Namespace: e.Namespace, CreatedAt: e.CreatedAt, ExpiresAt: e.ExpiresAt, DeletedAt: now}); err != nil {
		logf.FromContext(ctx).WithName("namespace-ttl").Error(err, "Could not record a deleted namespace", "namespace", e.Namespace)
	}
	if r.Notify != nil {
		msg := fmt.Sprintf("🗑️ Deleted namespace `%s`, created %s: preview cleanup deletes it %s.", e.Namespace, e.CreatedAt.UTC().Format("Mon 2 Jan 15:04 MST"), describe(e))
		if _, err := r.Notify.Send(ctx, notify.EventAlerts, msg); err != nil {
			logf.FromContext(ctx).WithName("namespace-ttl").Error(err, "Could not announce a deleted namespace", "namespace", e.Namespace)
		}
	}
	return nil
}

// describe says why a namespace expires when it does.
func describe(e Entry) string {
	switch e.Source {
	case SourceExtended:
		return "at the time it was extended to"
	case SourceLabel:
		return "after the time its " + TTLLabel + " label gives"
	}
	return fmt.Sprintf("%s after it was created", humanDuration(e.ExpiresAt.Sub(e.CreatedAt)))
}

func humanDuration(d time.Duration) string {
	if d >= 24*time.Hour && d%(24*time.Hour) == 0 {
		return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
	}
	return d.String()
}

// Deleted returns the namespaces the setting deleted, newest first.
func Deleted(ctx context.Context, c client.Reader) ([]Deletion, error) {
	var cm corev1.ConfigMap
	err := c.Get(ctx, client.ObjectKey{Namespace: config.OperatorNamespace(), Name: logConfigMap}, &cm)
	if apierrors.IsNotFound(err) {
		return []Deletion{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []Deletion{}
	if raw := cm.Data[logKey]; raw != "" {
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// record adds a deletion to the record, keeping the latest logSize.
func record(ctx context.Context, c client.Client, d Deletion) error {
	list, err := Deleted(ctx, c)
	if err != nil {
		return err
	}
	list = append([]Deletion{d}, list...)
	if len(list) > logSize {
		list = list[:logSize]
	}
	raw, err := json.Marshal(list)
	if err != nil {
		return err
	}
	cm := &corev1.ConfigMap{Namespace: config.OperatorNamespace(), Name: logConfigMap,
		Labels: map[string]string{"app.kubernetes.io/managed-by": "costdeck"}}
	err = c.Get(ctx, client.ObjectKeyFromObject(cm), cm)
	if apierrors.IsNotFound(err) {
		cm.Data = map[string]string{logKey: string(raw)}
		return c.Create(ctx, cm)
	}
	if err != nil {
		return err
	}
	if cm.Data == nil {
		cm.Data = map[string]string{}
	}
	cm.Data[logKey] = string(raw)
	return c.Update(ctx, cm)
}

// Extend moves a namespace's deletion to by after its current expiry, or after now when
// that has passed, and clears the warning so the new time is announced again.
func Extend(ctx context.Context, c client.Client, t finopsv1.NamespaceTTLConfig, namespace string, by time.Duration, now time.Time) (Entry, error) {
	plan, err := Plan(ctx, c, t)
	if err != nil {
		return Entry{}, err
	}
	for _, e := range plan {
		if e.Namespace != namespace {
			continue
		}
		if e.Protected != "" {
			return e, fmt.Errorf("%s is never deleted: %s", namespace, e.Protected)
		}
		from := e.ExpiresAt
		if from.Before(now) {
			from = now
		}
		e.ExpiresAt, e.Source, e.Warned = from.Add(by).Truncate(time.Second), SourceExtended, false
		patch := fmt.Sprintf(`{"metadata":{"annotations":{%q:%q,%q:null}}}`, ExpiresAnnotation, e.ExpiresAt.UTC().Format(time.RFC3339), WarnedAnnotation)
		ns := &corev1.Namespace{Name: namespace}
		return e, c.Patch(ctx, ns, client.RawPatch(types.MergePatchType, []byte(patch)))
	}
	return Entry{}, apierrors.NewNotFound(corev1.Resource("namespaces"), namespace)
}
