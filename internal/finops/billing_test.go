package finops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/pricing"
	"github.com/migalsp/costdeck-operator/internal/scaling"
)

const goodFactor = "0.7000"

// billingNow is 08:00 UTC today. The price resolver trusts a reconciliation for
// billingMaxAge by the real clock, so a fixed date would make these tests expire.
func billingNow() time.Time {
	return time.Now().UTC().Truncate(24 * time.Hour).Add(8 * time.Hour)
}

func billingClient(t *testing.T, days int) client.Client {
	t.Helper()
	now := billingNow()
	l := &Ledger{Currency: "USD"}
	for d := days; d >= 1; d-- {
		l.Days = append(l.Days, Day{Date: DateOf(now.AddDate(0, 0, -d)), Hours: 24, Provisioned: 100, List: 100})
	}
	raw, _ := json.Marshal(l)
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = finopsv1.AddToScheme(scheme)
	cfg := &finopsv1.CostDeckConfig{
		Name: "default", Namespace: "costdeck",
		Spec: finopsv1.CostDeckConfigSpec{Billing: finopsv1.BillingConfig{Enabled: true, AWS: &finopsv1.AWSBilling{TagValue: "prod"}}},
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&finopsv1.CostDeckConfig{}).WithObjects(cfg,
		&corev1.ConfigMap{Name: LedgerConfigMap, Namespace: "costdeck", Data: map[string]string{ledgerKey: string(raw)}}).Build()
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
	now := billingNow
	c := billingClient(t, 14)

	b := &BillingReconciler{Client: c, Namespace: "costdeck", Read: fixedBill(70, nil), Now: now}
	st, err := b.Reconcile(ctx)
	// The compared week ends the day before yesterday, whose bill is complete.
	from, to := DateOf(now().AddDate(0, 0, -8)), DateOf(now().AddDate(0, 0, -2))
	if err != nil || st.Factor != goodFactor || st.From != from || st.To != to || st.Error != "" {
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
		Now: billingNow}
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

func TestBillingReconcilerStartWaitsBeforeTheFirstRead(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "costdeck")
	var reads atomic.Int64
	read := func(ctx context.Context, cfg *finopsv1.CostDeckConfig, from, to time.Time) (scaling.DailyCost, string, string, error) {
		reads.Add(1)
		return fixedBill(70, nil)(ctx, cfg, from, to)
	}
	b := &BillingReconciler{Client: billingClient(t, 14), Namespace: "costdeck", Read: read, Now: billingNow}
	if !b.NeedLeaderElection() {
		t.Fatal("billing reconciliation must run on the leader only")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- b.Start(ctx) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Start returned %v when its context ended, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Start did not return after its context ended")
	}
	if n := reads.Load(); n != 0 {
		t.Errorf("the bill was read %d time(s) before the first minute was up", n)
	}
}

func TestBillingReconcileWhenOff(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "costdeck")
	ctx := context.Background()
	c := testClient(&finopsv1.CostDeckConfig{Name: "default", Namespace: "costdeck"})
	read := func(context.Context, *finopsv1.CostDeckConfig, time.Time, time.Time) (scaling.DailyCost, string, string, error) {
		t.Error("the bill was read while reconciliation is off")
		return nil, "", "", nil
	}
	st, err := (&BillingReconciler{Client: c, Namespace: "costdeck", Read: read, Now: billingNow}).Reconcile(ctx)
	if st != nil || err != nil {
		t.Fatalf("Reconcile = %+v, %v; want nothing while off", st, err)
	}
	var cfg finopsv1.CostDeckConfig
	_ = c.Get(ctx, client.ObjectKey{Namespace: "costdeck", Name: "default"}, &cfg)
	if cfg.Status.Billing != nil {
		t.Errorf("status written while off: %+v", cfg.Status.Billing)
	}
	if _, err := (&BillingReconciler{Client: clientFailingGets(), Namespace: "costdeck", Read: read}).Reconcile(ctx); !errors.Is(err, errAPIDown) {
		t.Errorf("Reconcile = %v, want the error reading the settings", err)
	}
}

func TestBillingEmptyBillIsNotApplied(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "costdeck")
	b := &BillingReconciler{Client: billingClient(t, 14), Namespace: "costdeck", Read: fixedBill(0, nil), Now: billingNow}
	st, err := b.Reconcile(context.Background())
	if err != nil || st.Factor != "" || !strings.Contains(st.Error, "the bill is empty for these days") || st.Billed != "0.00" || st.List != "700.00" {
		t.Errorf("an empty bill: %+v %v", st, err)
	}
}

func TestBillingReadsTheBillFromTheConfiguredCloud(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "costdeck")
	for _, tc := range []struct {
		name      string
		billing   finopsv1.BillingConfig
		providers finopsv1.ProvidersConfig
		source    string
		err       string
	}{
		{"no cloud named", finopsv1.BillingConfig{Enabled: true}, finopsv1.ProvidersConfig{},
			"", "billing reconciliation is on but names no cloud: set aws, azure or gcp"},
		{"AWS credentials missing", finopsv1.BillingConfig{Enabled: true, AWS: &finopsv1.AWSBilling{TagValue: "prod"}},
			finopsv1.ProvidersConfig{AWS: &finopsv1.AWSProviderConfig{SecretRef: "aws-billing"}}, scaling.BillingAWS, "read secret costdeck/aws-billing"},
		{"Azure provider not set up", finopsv1.BillingConfig{Enabled: true, Azure: &finopsv1.AzureBilling{ResourceGroup: "MC_rg"}},
			finopsv1.ProvidersConfig{}, scaling.BillingAzure, "the Azure provider is not configured"},
		{"Google Cloud provider not set up", finopsv1.BillingConfig{Enabled: true, GCP: &finopsv1.GCPBilling{Table: "billing-project.export.gcp_billing", ClusterName: "prod"}},
			finopsv1.ProvidersConfig{}, scaling.BillingGCP, "the Google Cloud provider is not configured"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			cfg := &finopsv1.CostDeckConfig{Name: "default", Namespace: "costdeck",
				Spec:   finopsv1.CostDeckConfigSpec{Billing: tc.billing, Providers: tc.providers},
				Status: finopsv1.CostDeckConfigStatus{Billing: &finopsv1.BillingStatus{Factor: goodFactor}}}
			scheme := runtime.NewScheme()
			_ = clientgoscheme.AddToScheme(scheme)
			_ = finopsv1.AddToScheme(scheme)
			c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&finopsv1.CostDeckConfig{}).WithObjects(cfg).Build()

			st, err := (&BillingReconciler{Client: c, Namespace: "costdeck", Now: billingNow}).Reconcile(ctx)
			if err != nil || st.Source != tc.source || !strings.Contains(st.Error, tc.err) || st.Factor != goodFactor {
				t.Fatalf("Reconcile = %+v, %v; want source %q, error %q and the last factor kept", st, err, tc.source, tc.err)
			}
			var stored finopsv1.CostDeckConfig
			_ = c.Get(ctx, client.ObjectKey{Namespace: "costdeck", Name: "default"}, &stored)
			if stored.Status.Billing == nil || stored.Status.Billing.Error != st.Error || stored.Status.Billing.Source != tc.source {
				t.Errorf("stored status = %+v, want the error recorded", stored.Status.Billing)
			}
		})
	}
}
