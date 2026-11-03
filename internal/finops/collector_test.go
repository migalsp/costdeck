package finops

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-logr/logr/funcr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/pricing"
)

// A day written to the history between two samples survives the next one.
func TestCollectorKeepsChangesMadeBetweenSamples(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = finopsv1.AddToScheme(scheme)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	col := &Collector{Client: c, Pricing: &pricing.Resolver{Client: c}, Namespace: "costdeck", Now: func() time.Time { return now }}
	if err := col.Sample(ctx); err != nil {
		t.Fatal(err)
	}

	l, err := LoadLedger(ctx, c, "costdeck")
	if err != nil {
		t.Fatal(err)
	}
	l.Days = append([]Day{{Date: "2026-10-01", Hours: 24, Provisioned: 42}}, l.Days...)
	if err := SaveLedger(ctx, c, "costdeck", l); err != nil {
		t.Fatal(err)
	}

	now = now.Add(5 * time.Minute)
	if err := col.Sample(ctx); err != nil {
		t.Fatal(err)
	}
	var cm corev1.ConfigMap
	_ = c.Get(ctx, client.ObjectKey{Namespace: "costdeck", Name: LedgerConfigMap}, &cm)
	var saved Ledger
	_ = json.Unmarshal([]byte(cm.Data[ledgerKey]), &saved)
	if len(saved.Days) == 0 || saved.Days[0].Date != "2026-10-01" || saved.Days[0].Provisioned != 42 {
		t.Errorf("the day written in between is gone: %+v", saved.Days)
	}
	if !saved.Last.Equal(now) {
		t.Errorf("last sample = %v, want %v", saved.Last, now)
	}
}

type capturedLog struct {
	mu    sync.Mutex
	lines []string
}

func (l *capturedLog) into(ctx context.Context) context.Context {
	sink := funcr.New(func(prefix, args string) {
		l.mu.Lock()
		defer l.mu.Unlock()
		l.lines = append(l.lines, prefix+" "+args)
	}, funcr.Options{})
	return logf.IntoContext(ctx, sink)
}

func (l *capturedLog) count(substring string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, line := range l.lines {
		if strings.Contains(line, substring) {
			n++
		}
	}
	return n
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting until %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

type runningLoop struct {
	cancel context.CancelFunc
	done   chan error
}

func startLoop(ctx context.Context, start func(context.Context) error) *runningLoop {
	ctx, cancel := context.WithCancel(ctx)
	loop := &runningLoop{cancel: cancel, done: make(chan error, 1)}
	go func() { loop.done <- start(ctx) }()
	return loop
}

func (l *runningLoop) stop(t *testing.T) error {
	t.Helper()
	l.cancel()
	select {
	case err := <-l.done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("Start did not return after its context was cancelled")
		return nil
	}
}

func TestCollectorStartSamplesOnEveryTickUntilCancelled(t *testing.T) {
	ctx := context.Background()
	c := testClient()
	first := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	var samples atomic.Int64
	col := &Collector{Client: c, Pricing: &pricing.Resolver{Client: c}, Namespace: "costdeck", Interval: time.Millisecond,
		Now: func() time.Time { return first.Add(time.Duration(samples.Add(1)-1) * time.Minute) }}
	if !col.NeedLeaderElection() {
		t.Fatal("the collector must run on the leader only, so one writer owns the ledger")
	}

	loop := startLoop(ctx, col.Start)
	waitUntil(t, "two minutes are booked by three samples", func() bool {
		l, err := LoadLedger(ctx, c, "costdeck")
		return err == nil && len(l.Days) == 1 && l.Days[0].Hours >= 2.0/60-0.001
	})
	if err := loop.stop(t); err != nil {
		t.Fatalf("Start returned %v after cancellation, want nil", err)
	}
	stopped := samples.Load()
	l, _ := LoadLedger(ctx, c, "costdeck")
	if want := first.Add(time.Duration(stopped-1) * time.Minute); !l.Last.Equal(want) {
		t.Errorf("last sample = %v, want the %dth sample at %v", l.Last, stopped, want)
	}
}

func TestCollectorStartWithoutAnIntervalSamplesOnceAndWaitsTheDefault(t *testing.T) {
	ctx := context.Background()
	c := testClient()
	at := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	var samples atomic.Int64
	col := &Collector{Client: c, Pricing: &pricing.Resolver{Client: c}, Namespace: "costdeck",
		Now: func() time.Time { samples.Add(1); return at }}

	loop := startLoop(ctx, col.Start)
	waitUntil(t, "the first sample is stored", func() bool {
		var cm corev1.ConfigMap
		return c.Get(ctx, client.ObjectKey{Namespace: "costdeck", Name: LedgerConfigMap}, &cm) == nil
	})
	if err := loop.stop(t); err != nil {
		t.Fatalf("Start returned %v, want nil", err)
	}
	if n := samples.Load(); n != 1 {
		t.Errorf("sampled %d times; without an interval the next sample waits %s", n, DefaultInterval)
	}
}

func TestCollectorStartKeepsSamplingAfterAFailure(t *testing.T) {
	const corrupt = "{not json"
	c := testClient(&corev1.ConfigMap{Name: LedgerConfigMap, Namespace: "costdeck", Data: map[string]string{ledgerKey: corrupt}})
	logs := &capturedLog{}
	ctx := logs.into(context.Background())
	col := &Collector{Client: c, Pricing: &pricing.Resolver{Client: c}, Namespace: "costdeck", Interval: time.Millisecond}

	loop := startLoop(ctx, col.Start)
	waitUntil(t, "two failed samples are logged", func() bool { return logs.count("Could not record cost history") >= 2 })
	if err := loop.stop(t); err != nil {
		t.Fatalf("Start returned %v, want nil", err)
	}
	var cm corev1.ConfigMap
	_ = c.Get(context.Background(), client.ObjectKey{Namespace: "costdeck", Name: LedgerConfigMap}, &cm)
	if cm.Data[ledgerKey] != corrupt {
		t.Errorf("a history that cannot be read was overwritten with %q", cm.Data[ledgerKey])
	}
}
