package finops

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/config"
	"github.com/migalsp/costdeck-operator/internal/pricing"
)

// Reports are kept in one ConfigMap of the operator namespace: an index plus one Markdown
// key per report. The newest maxReports are kept, and fewer when they would not fit.
const (
	ReportsConfigMap = "costdeck-reports"
	indexKey         = "index.json"
	maxReports       = 24
	maxReportsBytes  = 800 << 10

	annotationDigestSent     = "costdeck.io/digest-last-sent"
	annotationDigestSchedule = "costdeck.io/digest-schedule"
)

// Report kinds.
const (
	ReportAI     = "ai"
	ReportDigest = "digest"
)

// ReportEntry describes one stored report.
type ReportEntry struct {
	ID          string    `json:"id"`
	Kind        string    `json:"kind"`
	Title       string    `json:"title"`
	Audience    string    `json:"audience,omitempty"`
	Period      string    `json:"period,omitempty"`
	GeneratedAt time.Time `json:"generatedAt"`
	By          string    `json:"by,omitempty"`
	// Delivered names where the report was sent, e.g. "webex".
	Delivered string `json:"delivered,omitempty"`
}

// ErrReportNotFound is returned for an unknown report ID.
var ErrReportNotFound = errors.New("report not found")

func newReportID(kind string, at time.Time) string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%s-%s-%s", at.UTC().Format("20060102-150405"), kind, hex.EncodeToString(b))
}

func readIndex(cm *corev1.ConfigMap) []ReportEntry {
	var idx []ReportEntry
	if raw := cm.Data[indexKey]; raw != "" {
		_ = json.Unmarshal([]byte(raw), &idx)
	}
	return idx
}

// ListReports returns the stored reports, newest first.
func ListReports(ctx context.Context, c client.Reader, namespace string) ([]ReportEntry, error) {
	var cm corev1.ConfigMap
	if err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: ReportsConfigMap}, &cm); err != nil {
		if apierrors.IsNotFound(err) {
			return []ReportEntry{}, nil
		}
		return nil, err
	}
	idx := readIndex(&cm)
	if idx == nil {
		idx = []ReportEntry{}
	}
	return idx, nil
}

// GetReport returns one report and its Markdown.
func GetReport(ctx context.Context, c client.Reader, namespace, id string) (ReportEntry, string, error) {
	var cm corev1.ConfigMap
	if err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: ReportsConfigMap}, &cm); err != nil {
		if apierrors.IsNotFound(err) {
			return ReportEntry{}, "", ErrReportNotFound
		}
		return ReportEntry{}, "", err
	}
	for _, e := range readIndex(&cm) {
		if e.ID == id {
			return e, cm.Data[id+".md"], nil
		}
	}
	return ReportEntry{}, "", ErrReportNotFound
}

// updateReports applies a change to the reports ConfigMap, creating it on first use and
// retrying on conflicts: the API and the leader's scheduler both write it.
func updateReports(ctx context.Context, c client.Client, namespace string, change func(cm *corev1.ConfigMap)) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var cm corev1.ConfigMap
		err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: ReportsConfigMap}, &cm)
		create := apierrors.IsNotFound(err)
		if err != nil && !create {
			return err
		}
		if create {
			cm = corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
				Name: ReportsConfigMap, Namespace: namespace,
				Labels: map[string]string{"app.kubernetes.io/managed-by": "costdeck"},
			}}
		}
		if cm.Data == nil {
			cm.Data = map[string]string{}
		}
		if cm.Annotations == nil {
			cm.Annotations = map[string]string{}
		}
		change(&cm)
		if create {
			return c.Create(ctx, &cm)
		}
		return c.Update(ctx, &cm)
	})
}

// SaveReport stores a report and returns its entry, dropping the oldest reports beyond
// the limits.
func SaveReport(ctx context.Context, c client.Client, namespace string, e ReportEntry, markdown string) (ReportEntry, error) {
	if e.GeneratedAt.IsZero() {
		e.GeneratedAt = time.Now()
	}
	if e.ID == "" {
		e.ID = newReportID(e.Kind, e.GeneratedAt)
	}
	err := updateReports(ctx, c, namespace, func(cm *corev1.ConfigMap) {
		idx := append([]ReportEntry{e}, readIndex(cm)...)
		sort.SliceStable(idx, func(i, j int) bool { return idx[i].GeneratedAt.After(idx[j].GeneratedAt) })
		cm.Data[e.ID+".md"] = markdown
		size := func() int {
			n := 0
			for _, v := range cm.Data {
				n += len(v)
			}
			return n
		}
		for len(idx) > 1 && (len(idx) > maxReports || size() > maxReportsBytes) {
			last := idx[len(idx)-1]
			delete(cm.Data, last.ID+".md")
			idx = idx[:len(idx)-1]
		}
		raw, _ := json.Marshal(idx)
		cm.Data[indexKey] = string(raw)
	})
	return e, err
}

// DeleteReport removes one report.
func DeleteReport(ctx context.Context, c client.Client, namespace, id string) error {
	found := false
	err := updateReports(ctx, c, namespace, func(cm *corev1.ConfigMap) {
		idx := readIndex(cm)
		out := idx[:0]
		for _, e := range idx {
			if e.ID == id {
				found = true
				continue
			}
			out = append(out, e)
		}
		delete(cm.Data, id+".md")
		raw, _ := json.Marshal(out)
		cm.Data[indexKey] = string(raw)
	})
	if err == nil && !found {
		return ErrReportNotFound
	}
	return err
}

// DigestLastSent is when the scheduled digest was last sent, if ever.
func DigestLastSent(ctx context.Context, c client.Reader, namespace string) time.Time {
	var cm corev1.ConfigMap
	if err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: ReportsConfigMap}, &cm); err != nil {
		return time.Time{}
	}
	t, _ := time.Parse(time.RFC3339, cm.Annotations[annotationDigestSent])
	return t
}

// Poster delivers a digest, for example to the Webex space.
type Poster interface {
	Post(ctx context.Context, markdown string) error
}

// DigestScheduler sends the cost digest when the schedule in the CostDeckConfig says so.
// It runs on the leader only.
type DigestScheduler struct {
	Client    client.Client
	Pricing   *pricing.Resolver
	Poster    Poster
	Namespace string
	// Overview builds the overview with right-sizing advice; without it the digest is
	// built from a snapshot alone and lists no right-sizing opportunities.
	Overview func(ctx context.Context) (Overview, error)
	// Now is replaceable in tests.
	Now func() time.Time

	retryAt time.Time
}

// overviewWaits bounds how long a scheduled digest waits for right-sizing advice that is
// still being calculated.
const (
	overviewWaits    = 9
	overviewInterval = 5 * time.Second
)

// NeedLeaderElection implements manager.LeaderElectionRunnable.
func (d *DigestScheduler) NeedLeaderElection() bool { return true }

// Start implements manager.Runnable: it checks the schedule once a minute.
func (d *DigestScheduler) Start(ctx context.Context) error {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		if err := d.Tick(ctx); err != nil {
			logf.FromContext(ctx).WithName("digest").Error(err, "Could not send the cost digest")
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func scheduleFingerprint(s finopsv1.DigestSchedule) string {
	return strings.Join([]string{s.Frequency, s.Time, s.Timezone}, "|")
}

// Tick sends the digest if it is due. The first tick after the schedule is turned on or
// changed only records the moment, so a schedule never fires for a past occasion.
func (d *DigestScheduler) Tick(ctx context.Context) error {
	now := time.Now()
	if d.Now != nil {
		now = d.Now()
	}
	cfg, err := config.Get(ctx, d.Client)
	if err != nil {
		return err
	}
	sched := cfg.Spec.Reports.Digest
	if !sched.Enabled || now.Before(d.retryAt) {
		return nil
	}
	var cm corev1.ConfigMap
	err = d.Client.Get(ctx, client.ObjectKey{Namespace: d.Namespace, Name: ReportsConfigMap}, &cm)
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	last, _ := time.Parse(time.RFC3339, cm.Annotations[annotationDigestSent])
	if last.IsZero() || cm.Annotations[annotationDigestSchedule] != scheduleFingerprint(sched) {
		return d.mark(ctx, now, sched)
	}
	due, err := NextDigest(sched, last)
	if err != nil || now.Before(due) {
		return err
	}
	if _, err := d.Send(ctx, DigestPeriod(sched), "schedule"); err != nil {
		// Try again in an hour rather than every minute.
		d.retryAt = now.Add(time.Hour)
		return err
	}
	return d.mark(ctx, now, sched)
}

func (d *DigestScheduler) mark(ctx context.Context, at time.Time, sched finopsv1.DigestSchedule) error {
	return updateReports(ctx, d.Client, d.Namespace, func(cm *corev1.ConfigMap) {
		cm.Annotations[annotationDigestSent] = at.UTC().Format(time.RFC3339)
		cm.Annotations[annotationDigestSchedule] = scheduleFingerprint(sched)
	})
}

// Send builds the digest for a period, posts it and stores it in the report history.
func (d *DigestScheduler) Send(ctx context.Context, period, by string) (ReportEntry, error) {
	now := time.Now()
	if d.Now != nil {
		now = d.Now()
	}
	digest, err := d.digest(ctx, period, now)
	if err != nil {
		return ReportEntry{}, err
	}
	md := digest.Markdown()
	if err := d.Poster.Post(ctx, md); err != nil {
		return ReportEntry{}, err
	}
	return SaveReport(ctx, d.Client, d.Namespace, ReportEntry{
		Kind: ReportDigest, Title: digest.Title, Period: period, GeneratedAt: now, By: by, Delivered: DeliveredWebex,
	}, md)
}

func (d *DigestScheduler) digest(ctx context.Context, period string, now time.Time) (Digest, error) {
	if d.Overview == nil {
		return BuildDigest(ctx, d.Client, d.Pricing.Rates(ctx), d.Namespace, period, now)
	}
	o, err := d.Overview(ctx)
	for i := 0; err == nil && o.Savings.RightsizingPending > 0 && i < overviewWaits; i++ {
		select {
		case <-ctx.Done():
			return Digest{}, ctx.Err()
		case <-time.After(overviewInterval):
		}
		o, err = d.Overview(ctx)
	}
	if err != nil {
		return Digest{}, err
	}
	ledger, err := LoadLedger(ctx, d.Client, d.Namespace)
	if err != nil {
		return Digest{}, err
	}
	return NewDigest(o, ledger, period, now), nil
}

// BuildDigest takes a fresh snapshot and builds the digest from it and the ledger.
func BuildDigest(ctx context.Context, c client.Reader, rates pricing.Rates, namespace, period string, now time.Time) (Digest, error) {
	snap, err := Build(ctx, c, rates, Options{Namespace: namespace, Now: now})
	if err != nil {
		return Digest{}, err
	}
	ledger, err := LoadLedger(ctx, c, namespace)
	if err != nil {
		return Digest{}, err
	}
	return NewDigest(NewOverview(snap, ledger, nil, 0), ledger, period, now), nil
}
