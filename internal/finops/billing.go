package finops

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/config"
	"github.com/migalsp/costdeck-operator/internal/scaling"
)

// Billing reconciliation closes the gap between list prices and the bill. Every few hours
// it reads what the cloud charged for the cluster's nodes over a week, compares it with the
// ledger's list-price node cost for the same days, and stores the ratio in the
// CostDeckConfig status. The pricing resolver scales every compute rate by it, so Savings
// Plans, Reserved Instances, committed use discounts and spot prices reach every figure.

const (
	billingInterval = 6 * time.Hour
	// billingLagDays skips the most recent days, which clouds publish late.
	billingLagDays    = 2
	billingWindowDays = 7
	minBillingDays    = 3
	// A factor outside these bounds means the filter selects the wrong resources.
	minBillingFactor = 0.1
	maxBillingFactor = 3.0
	defaultAWSTagKey = "aws:eks:cluster-name"
)

// BillReader reads the bill per day for the configured cloud; it returns the amounts,
// their currency and the source's name.
type BillReader func(ctx context.Context, cfg *finopsv1.CostDeckConfig, from, to time.Time) (scaling.DailyCost, string, string, error)

// BillingReconciler runs reconciliation on the leader.
type BillingReconciler struct {
	Client    client.Client
	Namespace string
	// Read overrides how the bill is read; nil reads it from the configured cloud.
	Read BillReader
	Now  func() time.Time
}

// NeedLeaderElection implements manager.LeaderElectionRunnable.
func (b *BillingReconciler) NeedLeaderElection() bool { return true }

// Start implements manager.Runnable.
func (b *BillingReconciler) Start(ctx context.Context) error {
	log := logf.FromContext(ctx).WithName("billing")
	timer := time.NewTimer(time.Minute)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
		}
		if st, err := b.Reconcile(ctx); err != nil {
			log.Error(err, "Could not reconcile with the cloud bill")
		} else if st != nil && st.Error != "" {
			log.Info("Could not reconcile with the cloud bill", "reason", st.Error)
		}
		timer.Reset(billingInterval)
	}
}

// Reconcile reads the bill once and records the result. It returns nil when
// reconciliation is off.
func (b *BillingReconciler) Reconcile(ctx context.Context) (*finopsv1.BillingStatus, error) {
	now := time.Now()
	if b.Now != nil {
		now = b.Now()
	}
	cfg, err := config.Get(ctx, b.Client)
	if err != nil {
		return nil, err
	}
	if !cfg.Spec.Billing.Enabled {
		return nil, nil
	}
	today := time.Date(now.UTC().Year(), now.UTC().Month(), now.UTC().Day(), 0, 0, 0, 0, time.UTC)
	to := today.AddDate(0, 0, -billingLagDays)
	from := to.AddDate(0, 0, -(billingWindowDays - 1))

	st := finopsv1.BillingStatus{From: DateOf(from), To: DateOf(to), LastChecked: metav1.NewTime(now)}
	if prev := cfg.Status.Billing; prev != nil {
		// A failed attempt keeps the last good factor until it ages out.
		st.Factor, st.LastReconciled, st.Source = prev.Factor, prev.LastReconciled, prev.Source
	}
	read := b.Read
	if read == nil {
		read = b.readCloud
	}
	bill, currency, source, err := read(ctx, cfg, from, to)
	if source != "" {
		st.Source = source
	}
	if err != nil {
		st.Error = err.Error()
		return &st, b.store(ctx, &st)
	}
	ledger, err := LoadLedger(ctx, b.Client, b.Namespace)
	if err != nil {
		return nil, err
	}
	var billed, list float64
	days := 0
	for _, d := range ledger.Days {
		amount, ok := bill[d.Date]
		listCost := d.List
		if listCost == 0 {
			listCost = d.Provisioned
		}
		if !ok || d.Date < st.From || d.Date > st.To || d.Hours < completeDayHours || listCost <= 0 {
			continue
		}
		billed += amount
		list += listCost
		days++
	}
	st.Billed, st.List, st.Currency = fmt.Sprintf("%.2f", billed), fmt.Sprintf("%.2f", list), currency
	switch {
	case days < minBillingDays:
		st.Error = fmt.Sprintf("only %d day(s) of the bill overlap complete days of the cost history; reconciliation needs %d", days, minBillingDays)
	case billed <= 0:
		st.Error = "the bill is empty for these days: check that the filter selects the cluster's nodes"
	default:
		f := billed / list
		if f < minBillingFactor || f > maxBillingFactor {
			st.Error = fmt.Sprintf("the bill is %.0f%% of the list price, which is implausible: check that the filter selects exactly the cluster's nodes", f*100)
			break
		}
		st.Factor = strconv.FormatFloat(f, 'f', 4, 64)
		st.LastReconciled = metav1.NewTime(now)
		st.Error = ""
	}
	return &st, b.store(ctx, &st)
}

// store writes the result into the CostDeckConfig status.
func (b *BillingReconciler) store(ctx context.Context, st *finopsv1.BillingStatus) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var cfg finopsv1.CostDeckConfig
		if err := b.Client.Get(ctx, config.Key(), &cfg); err != nil {
			return err
		}
		patch := client.MergeFrom(cfg.DeepCopy())
		cfg.Status.Billing = st
		return b.Client.Status().Patch(ctx, &cfg, patch)
	})
}

// readCloud reads the bill from the cloud the billing settings name, with that provider's
// credentials.
func (b *BillingReconciler) readCloud(ctx context.Context, cfg *finopsv1.CostDeckConfig, from, to time.Time) (scaling.DailyCost, string, string, error) {
	bc := cfg.Spec.Billing
	switch {
	case bc.AWS != nil:
		p, err := scaling.BuildProvider(ctx, b.Client, cfg, scaling.ProviderAWS, nil)
		if err != nil {
			return nil, "", scaling.BillingAWS, err
		}
		aws, ok := p.(*scaling.AWSProvider)
		if !ok {
			return nil, "", scaling.BillingAWS, errors.New("the AWS provider is not available")
		}
		key := bc.AWS.TagKey
		if key == "" {
			key = defaultAWSTagKey
		}
		bill, cur, err := aws.DailyComputeCost(ctx, key, bc.AWS.TagValue, from, to)
		return bill, cur, scaling.BillingAWS, err
	case bc.Azure != nil:
		p, err := scaling.BuildProvider(ctx, b.Client, cfg, scaling.ProviderAzure, nil)
		if err != nil {
			return nil, "", scaling.BillingAzure, err
		}
		az, ok := p.(*scaling.AzureProvider)
		if !ok {
			return nil, "", scaling.BillingAzure, errors.New("the Azure provider is not available")
		}
		bill, cur, err := az.DailyComputeCost(ctx, bc.Azure.ResourceGroup, from, to)
		return bill, cur, scaling.BillingAzure, err
	case bc.GCP != nil:
		p, err := scaling.BuildProvider(ctx, b.Client, cfg, scaling.ProviderGCP, nil)
		if err != nil {
			return nil, "", scaling.BillingGCP, err
		}
		gcp, ok := p.(*scaling.GCPProvider)
		if !ok {
			return nil, "", scaling.BillingGCP, errors.New("the Google Cloud provider is not available")
		}
		bill, cur, err := gcp.DailyComputeCost(ctx, bc.GCP.Table, bc.GCP.ClusterName, from, to)
		return bill, cur, scaling.BillingGCP, err
	}
	return nil, "", "", errors.New("billing reconciliation is on but names no cloud: set aws, azure or gcp")
}
