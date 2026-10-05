package finops

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/migalsp/costdeck-operator/internal/pricing"
	"github.com/migalsp/costdeck-operator/internal/telemetry"
)

// The ledger keeps a daily cost history in a ConfigMap of the operator namespace. Kubernetes
// keeps no history of its own and VictoriaMetrics is optional, so without it there would be
// no trend, no month to date and no week-over-week comparison.
const (
	LedgerConfigMap = "costdeck-cost-history"
	ledgerKey       = "history.json"
	// RetentionDays bounds the history; older days are dropped.
	RetentionDays = 90
	// maxLedgerBytes keeps the ConfigMap well below the 1 MiB object limit by dropping the
	// oldest days first.
	maxLedgerBytes = 800 << 10
	// maxGap is the longest pause between two samples that is still counted. Anything
	// longer (the operator was down) is a gap in the history rather than a guess.
	maxGap = 15 * time.Minute
	// DefaultInterval is how often the collector samples.
	DefaultInterval = 5 * time.Minute
)

// Ledger is the daily cost history. Every amount is money accumulated over the hours the
// day was observed, in Currency.
type Ledger struct {
	Version  int       `json:"v"`
	Currency string    `json:"currency"`
	Since    time.Time `json:"since"`
	Last     time.Time `json:"last"`
	Days     []Day     `json:"days"`
}

// Day is one UTC day of the history.
type Day struct {
	Date string `json:"date"` // YYYY-MM-DD, UTC
	// Hours is how much of the day was observed; a day the operator was partly down has
	// fewer than 24.
	Hours       float64 `json:"hours"`
	Provisioned float64 `json:"provisioned"`
	Requested   float64 `json:"requested"`
	Used        float64 `json:"used"`
	Saved       float64 `json:"saved"`
	// List is the node cost at list price, before billing reconciliation; reconciliation
	// compares the bill with it.
	List float64 `json:"list,omitempty"`
	// Storage and Network are persistent volumes and load balancers, billed apart from
	// the nodes.
	Storage float64 `json:"storage,omitempty"`
	Network float64 `json:"network,omitempty"`
	// Namespaces holds each namespace's requested and used cost.
	Namespaces map[string]NamespaceDay `json:"ns,omitempty"`
	// Schedules holds what each schedule saved, by "group/name" or "config/name".
	Schedules map[string]float64 `json:"schedules,omitempty"`
}

// NamespaceDay is one namespace's cost on one day: compute requested and used, volumes
// and load balancers.
type NamespaceDay struct {
	Cost    float64 `json:"c"`
	Used    float64 `json:"u"`
	Storage float64 `json:"s,omitempty"`
	Network float64 `json:"n,omitempty"`
}

// Total is compute, storage and load balancers.
func (n NamespaceDay) Total() float64 { return n.Cost + n.Storage + n.Network }

// Bill is what the day cost: nodes, volumes and load balancers.
func (d Day) Bill() float64 { return d.Provisioned + d.Storage + d.Network }

// DateOf is the ledger day a moment belongs to.
func DateOf(t time.Time) string { return t.UTC().Format(time.DateOnly) }

// Add books the time since the previous sample at the snapshot's rates. The first sample,
// and the first after a gap longer than maxGap, only starts the clock.
func (l *Ledger) Add(s Snapshot) {
	if l.Currency != "" && s.Rates.Currency != "" && l.Currency != s.Rates.Currency {
		// Amounts in two currencies cannot be added up; start over.
		*l = Ledger{}
	}
	if l.Version == 0 {
		l.Version = 1
	}
	if s.Rates.Currency != "" {
		l.Currency = s.Rates.Currency
	}
	if l.Since.IsZero() {
		l.Since = s.At
	}
	elapsed := s.At.Sub(l.Last)
	fresh := l.Last.IsZero() || elapsed <= 0 || elapsed > maxGap
	l.Last = s.At
	if fresh {
		return
	}
	hours := elapsed.Hours()
	day := l.day(DateOf(s.At))
	day.Hours += hours
	day.Provisioned += s.Cluster.ProvisionedHourly * hours
	day.List += s.Cluster.ProvisionedHourly / s.Rates.ListFactor() * hours
	day.Requested += s.Cluster.RequestedHourly * hours
	day.Used += s.Cluster.UsedHourly * hours
	day.Storage += s.Cluster.StorageHourly * hours
	day.Network += s.Cluster.NetworkHourly * hours
	for _, ns := range s.Namespaces {
		if ns.RequestedHourly == 0 && ns.UsedHourly == 0 && ns.StorageHourly == 0 && ns.NetworkHourly == 0 {
			continue
		}
		if day.Namespaces == nil {
			day.Namespaces = map[string]NamespaceDay{}
		}
		d := day.Namespaces[ns.Name]
		d.Cost += ns.RequestedHourly * hours
		d.Used += ns.UsedHourly * hours
		d.Storage += ns.StorageHourly * hours
		d.Network += ns.NetworkHourly * hours
		day.Namespaces[ns.Name] = d
	}
	for _, sc := range s.Schedules {
		if sc.SavedHourly == 0 {
			continue
		}
		if day.Schedules == nil {
			day.Schedules = map[string]float64{}
		}
		day.Schedules[sc.Key()] += sc.SavedHourly * hours
		day.Saved += sc.SavedHourly * hours
	}
	l.trim(s.At)
}

// day returns the day with the given date, appending it when it is new.
func (l *Ledger) day(date string) *Day {
	if n := len(l.Days); n > 0 && l.Days[n-1].Date == date {
		return &l.Days[n-1]
	}
	l.Days = append(l.Days, Day{Date: date})
	return &l.Days[len(l.Days)-1]
}

// trim drops days outside the retention window.
func (l *Ledger) trim(now time.Time) {
	oldest := DateOf(now.AddDate(0, 0, -RetentionDays+1))
	i := 0
	for i < len(l.Days) && l.Days[i].Date < oldest {
		i++
	}
	l.Days = l.Days[i:]
}

// Encode serialises the ledger with amounts rounded to a hundredth of a cent, dropping the
// oldest days until it fits in a ConfigMap.
func (l *Ledger) Encode() ([]byte, error) {
	for {
		out := *l
		out.Days = make([]Day, len(l.Days))
		for i, d := range l.Days {
			out.Days[i] = d.rounded()
		}
		data, err := json.Marshal(out)
		if err != nil || len(data) <= maxLedgerBytes || len(l.Days) <= 1 {
			return data, err
		}
		l.Days = l.Days[1:]
	}
}

func (d Day) rounded() Day {
	d.Hours = round(d.Hours, 3)
	d.Provisioned, d.Requested, d.Used, d.Saved = round(d.Provisioned, 4), round(d.Requested, 4), round(d.Used, 4), round(d.Saved, 4)
	d.Storage, d.Network, d.List = round(d.Storage, 4), round(d.Network, 4), round(d.List, 4)
	if len(d.Namespaces) > 0 {
		ns := make(map[string]NamespaceDay, len(d.Namespaces))
		for k, v := range d.Namespaces {
			if v.Total() >= 0.00005 || v.Used >= 0.00005 {
				ns[k] = NamespaceDay{Cost: round(v.Cost, 4), Used: round(v.Used, 4), Storage: round(v.Storage, 4), Network: round(v.Network, 4)}
			}
		}
		d.Namespaces = ns
	}
	if len(d.Schedules) > 0 {
		sc := make(map[string]float64, len(d.Schedules))
		for k, v := range d.Schedules {
			sc[k] = round(v, 4)
		}
		d.Schedules = sc
	}
	return d
}

func round(v float64, digits int) float64 {
	p := math.Pow10(digits)
	return math.Round(v*p) / p
}

// LoadLedger reads the ledger. A missing ConfigMap is an empty ledger.
func LoadLedger(ctx context.Context, c client.Reader, namespace string) (*Ledger, error) {
	var cm corev1.ConfigMap
	if err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: LedgerConfigMap}, &cm); err != nil {
		if apierrors.IsNotFound(err) {
			return &Ledger{}, nil
		}
		return nil, err
	}
	var l Ledger
	if raw := cm.Data[ledgerKey]; raw != "" {
		if err := json.Unmarshal([]byte(raw), &l); err != nil {
			return nil, fmt.Errorf("could not read the cost history: %w", err)
		}
	}
	return &l, nil
}

// SaveLedger writes the ledger, creating the ConfigMap on first use.
func SaveLedger(ctx context.Context, c client.Client, namespace string, l *Ledger) error {
	data, err := l.Encode()
	if err != nil {
		return err
	}
	var cm corev1.ConfigMap
	err = c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: LedgerConfigMap}, &cm)
	if apierrors.IsNotFound(err) {
		cm = corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name: LedgerConfigMap, Namespace: namespace,
				Labels: map[string]string{"app.kubernetes.io/managed-by": "costdeck"},
			},
			Data: map[string]string{ledgerKey: string(data)},
		}
		return c.Create(ctx, &cm)
	}
	if err != nil {
		return err
	}
	if cm.Data == nil {
		cm.Data = map[string]string{}
	}
	cm.Data[ledgerKey] = string(data)
	return c.Update(ctx, &cm)
}

// Collector samples the cluster's cost on an interval and books it into the ledger. It
// runs on the leader only, so one writer owns the ConfigMap.
type Collector struct {
	Client    client.Client
	Pricing   *pricing.Resolver
	Namespace string
	Interval  time.Duration
	// Live reads EndpointSlices without a cache; nil means Client.
	Live client.Reader
	// Now is replaceable in tests.
	Now func() time.Time
}

// NeedLeaderElection implements manager.LeaderElectionRunnable.
func (c *Collector) NeedLeaderElection() bool { return true }

// Start implements manager.Runnable.
func (c *Collector) Start(ctx context.Context) error {
	log := logf.FromContext(ctx).WithName("cost-history")
	interval := c.Interval
	if interval <= 0 {
		interval = DefaultInterval
	}
	ledger, err := LoadLedger(ctx, c.Client, c.Namespace)
	if err != nil {
		log.Error(err, "Could not read the cost history, starting a new one")
		ledger = &Ledger{}
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := c.sample(ctx, ledger); err != nil {
			log.Error(err, "Could not record cost history")
			// Reload so the next sample starts from what is stored, not from a write that failed.
			if reloaded, lerr := LoadLedger(ctx, c.Client, c.Namespace); lerr == nil {
				ledger = reloaded
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (c *Collector) sample(ctx context.Context, ledger *Ledger) error {
	now := time.Now()
	if c.Now != nil {
		now = c.Now()
	}
	snap, err := Build(ctx, c.Client, c.Pricing.Rates(ctx), Options{Namespace: c.Namespace, Now: now, Live: c.Live})
	if err != nil {
		return err
	}
	ledger.Add(snap)
	m := float64(pricing.HoursPerMonth)
	telemetry.RecordCluster(map[string]float64{
		"nodes": snap.Cluster.ProvisionedHourly * m, "requested": snap.Cluster.RequestedHourly * m, "used": snap.Cluster.UsedHourly * m,
		"storage": snap.Cluster.StorageHourly * m, "network": snap.Cluster.NetworkHourly * m,
	}, snap.Rates.Currency)
	return SaveLedger(ctx, c.Client, c.Namespace, ledger)
}
