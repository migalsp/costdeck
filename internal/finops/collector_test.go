package finops

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

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
