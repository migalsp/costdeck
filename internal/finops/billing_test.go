package finops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/pricing"
	"github.com/migalsp/costdeck-operator/internal/scaling"
)

const goodFactor = "0.7000"

func billingClient(t *testing.T, days int) client.Client {
	t.Helper()
	now := time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)
	l := &Ledger{Currency: "USD"}
	for d := days; d >= 1; d-- {
		l.Days = append(l.Days, Day{Date: DateOf(now.AddDate(0, 0, -d)), Hours: 24, Provisioned: 100, List: 100})
	}
	raw, _ := json.Marshal(l)
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = finopsv1.AddToScheme(scheme)
	cfg := &finopsv1.CostDeckConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: "costdeck"},
		Spec:       finopsv1.CostDeckConfigSpec{Billing: finopsv1.BillingConfig{Enabled: true, AWS: &finopsv1.AWSBilling{TagValue: "prod"}}},
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&finopsv1.CostDeckConfig{}).WithObjects(cfg,
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: LedgerConfigMap, Namespace: "costdeck"}, Data: map[string]string{ledgerKey: string(raw)}}).Build()
}

func fixedBill(per float64, err error) BillReader {
	return func(_ context.Context, _ *finopsv1.CostDeckConfig, from, to time.Time) (scaling.DailyCost, string, string, error) {
		if err != nil {
			return nil, "", scaling.BillingAWS, err
		}
		out := scaling.DailyCost{}
		for d := from; !d.After(to); d = d.AddDate(0, 0, 1) {
			out[DateOf(d)] = per
		}
		return out, "USD", scaling.BillingAWS, nil
	}
}

func TestBillingReconcile(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "costdeck")
	ctx := context.Background()
	now := func() time.Time { return time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC) }
	c := billingClient(t, 14)

	b := &BillingReconciler{Client: c, Namespace: "costdeck", Read: fixedBill(70, nil), Now: now}
	st, err := b.Reconcile(ctx)
	if err != nil || st.Factor != goodFactor || st.From != "2026-10-04" || st.To != "2026-10-10" || st.Error != "" {
		t.Fatalf("reconcile: %+v %v", st, err)
	}
	var cfg finopsv1.CostDeckConfig
	_ = c.Get(ctx, client.ObjectKey{Namespace: "costdeck", Name: "default"}, &cfg)
	if cfg.Status.Billing == nil || cfg.Status.Billing.Factor != goodFactor || cfg.Status.Billing.Billed != "490.00" {
		t.Fatalf("status not stored: %+v", cfg.Status.Billing)
	}

	// The resolver scales list-price rates by the factor.
	rates := (&pricing.Resolver{Client: c}).Rates(ctx)
	if rates.Factor != 0.7 || !strings.Contains(rates.Basis, "reconciled with AWS Cost Explorer") {
		t.Errorf("rates not reconciled: %+v", rates)
	}

	// A failed read keeps the last good factor.
	b.Read = fixedBill(0, errors.New("access denied"))
	st, _ = b.Reconcile(ctx)
	if st.Factor != goodFactor || !strings.Contains(st.Error, "access denied") {
		t.Errorf("a failure must keep the factor: %+v", st)
	}

	// An implausible bill is not applied.
	b.Read = fixedBill(900, nil)
	st, _ = b.Reconcile(ctx)
	if st.Factor != goodFactor || !strings.Contains(st.Error, "implausible") {
		t.Errorf("an implausible factor must not be applied: %+v", st)
	}
}

func TestBillingNeedsHistory(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "costdeck")
	c := billingClient(t, 3) // only two days fall inside the compared week
	b := &BillingReconciler{Client: c, Namespace: "costdeck", Read: fixedBill(70, nil),
		Now: func() time.Time { return time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC) }}
	st, err := b.Reconcile(context.Background())
	if err != nil || st.Factor != "" || !strings.Contains(st.Error, fmt.Sprintf("needs %d", minBillingDays)) {
		t.Errorf("too little history: %+v %v", st, err)
	}
}

func TestLedgerKeepsListCost(t *testing.T) {
	var l Ledger
	at := time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)
	s := Snapshot{At: at, Rates: pricing.Rates{Currency: "USD", Factor: 0.5}, Cluster: Cluster{ProvisionedHourly: 6}}
	l.Add(s)
	s.At = at.Add(10 * time.Minute)
	l.Add(s)
	if d := l.Days[0]; !near(d.Provisioned, 1) || !near(d.List, 2) {
		t.Errorf("billed %v, list %v", d.Provisioned, d.List)
	}
}
