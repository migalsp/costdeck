// Package pricing turns CPU and memory into money. Rates come, in order of preference,
// from custom rates in the CostDeckConfig, from the AWS Price List API for the instance
// types the cluster actually runs, or from list-price heuristics per cloud.
package pricing

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/config"
)

// HoursPerMonth is the average number of hours in a month.
const HoursPerMonth = 730

// Rates converts resources into money.
type Rates struct {
	CPUCoreHour  float64 `json:"cpuCoreHour"`
	MemoryGBHour float64 `json:"memoryGiBHour"`
	Currency     string  `json:"currency"`
	// Basis says where the rates come from, so an estimate is never mistaken for a quote.
	Basis string `json:"basis"`
	// Factor is the billing reconciliation applied to list prices (billed ÷ list); 0 or 1
	// when none is. List prices are the rates divided by it.
	Factor float64 `json:"factor,omitempty"`
}

// ListFactor is what to divide by to get back to list prices.
func (r Rates) ListFactor() float64 {
	if r.Factor <= 0 {
		return 1
	}
	return r.Factor
}

// billingMaxAge bounds how long a reconciliation factor is trusted.
const billingMaxAge = 8 * 24 * time.Hour

// reconcile scales list-price rates by the billing factor when reconciliation is on and
// its result is recent. Custom rates are never scaled: they are what you pay already.
func reconcile(rates Rates, cfg *finopsv1.CostDeckConfig) Rates {
	st := cfg.Status.Billing
	if !cfg.Spec.Billing.Enabled || st == nil || st.Factor == "" || time.Since(st.LastReconciled.Time) > billingMaxAge {
		return rates
	}
	f, err := strconv.ParseFloat(st.Factor, 64)
	if err != nil || f <= 0 {
		return rates
	}
	rates.CPUCoreHour *= f
	rates.MemoryGBHour *= f
	rates.Factor = f
	if st.Currency != "" {
		rates.Currency = st.Currency
	}
	rates.Basis += fmt.Sprintf(" — reconciled with %s: billed %.0f%% of list price (%s to %s)", st.Source, f*100, st.From, st.To)
	return rates
}

// Hourly is the cost of the given CPU and memory for one hour.
func (r Rates) Hourly(cpu, mem resource.Quantity) float64 {
	return cpu.AsApproximateFloat64()*r.CPUCoreHour + mem.AsApproximateFloat64()/(1<<30)*r.MemoryGBHour
}

// Monthly is the cost of the given CPU and memory for an average month.
func (r Rates) Monthly(cpu, mem resource.Quantity) float64 { return r.Hourly(cpu, mem) * HoursPerMonth }

// heuristics are list prices per core-hour and GiB-hour of general-purpose on-demand
// Linux instances. They apply when nothing better is available, and their ratio splits a
// node's real price into CPU and memory. AWS (m6i and r6i in us-east-1) and Azure (Dsv5
// and Esv5 in East US) both work out at $0.033 a core-hour and $0.00375 a GiB-hour.
// Google Cloud and clusters without a cloud use the reference rates of OpenCost and
// Kubecost, so their figures compare with those tools.
var heuristics = map[string][2]float64{
	"aws":   {0.033, 0.00375},
	"azure": {0.033, 0.00375},
	"gcp":   {0.031611, 0.004237},
	"local": {0.031611, 0.004237},
}

// NodePricer returns the on-demand hourly price of an instance type in a region.
type NodePricer interface {
	HourlyPrice(ctx context.Context, region, instanceType string) (float64, error)
}

// Resolver resolves the rates for the cluster.
type Resolver struct {
	Client client.Reader
	// AWS builds an AWS Price List client from the live CostDeckConfig; nil disables
	// AWS pricing.
	AWS func(ctx context.Context, cfg *finopsv1.CostDeckConfig) (NodePricer, error)
	// Azure returns the Azure Retail Prices client; nil disables Azure pricing.
	Azure func(ctx context.Context, cfg *finopsv1.CostDeckConfig) (NodePricer, error)
	// TTL bounds how long cloud-derived rates are reused (prices change rarely).
	TTL time.Duration

	mu       sync.Mutex
	cached   *Rates
	cachedAt time.Time
	cacheKey string
}

// Rates returns the current rates. It never fails: when a better source is unavailable it
// falls back to the next one and says so in Basis.
func (r *Resolver) Rates(ctx context.Context) Rates {
	cfg, err := config.Get(ctx, r.Client)
	if err != nil {
		cfg = config.Empty()
	}
	var nodes corev1.NodeList
	_ = r.Client.List(ctx, &nodes)
	cloud := DetectCloud(nodes.Items)

	if custom, ok := customRates(cfg.Spec.Pricing); ok {
		return custom
	}
	fallback := heuristicRates(cloud, cfg.Spec.Pricing.Currency)

	if build := r.pricerFor(cloud); cfg.Spec.Features.CloudPricingAPI && build != nil && len(nodes.Items) > 0 {
		key := nodeFingerprint(nodes.Items)
		r.mu.Lock()
		if r.cached != nil && r.cacheKey == key && time.Since(r.cachedAt) < r.ttl() {
			rates := *r.cached
			r.mu.Unlock()
			return reconcile(rates, cfg)
		}
		r.mu.Unlock()

		rates, err := nodeRates(ctx, cloud, build, cfg, nodes.Items)
		if err != nil {
			logf.FromContext(ctx).Info("Could not price nodes with the cloud price list, using heuristic rates", "cloud", cloud, "error", err.Error())
			fallback.Basis += " — " + cloudNames[cloud] + " pricing unavailable: " + err.Error()
			return reconcile(fallback, cfg)
		}
		r.mu.Lock()
		r.cached, r.cachedAt, r.cacheKey = &rates, time.Now(), key
		r.mu.Unlock()
		return reconcile(rates, cfg)
	}
	return reconcile(fallback, cfg)
}

// pricerFor returns the price list builder for a cloud, or nil when there is none.
func (r *Resolver) pricerFor(cloud string) func(context.Context, *finopsv1.CostDeckConfig) (NodePricer, error) {
	switch cloud {
	case "aws":
		return r.AWS
	case "azure":
		return r.Azure
	}
	return nil
}

// InstancePrice is the live list price of an instance type, when cloud list prices are on
// and the cloud has a price list CostDeck can read.
func (r *Resolver) InstancePrice(ctx context.Context, cloud, region, instanceType string) (float64, bool) {
	cfg, err := config.Get(ctx, r.Client)
	if err != nil || !cfg.Spec.Features.CloudPricingAPI || region == "" {
		return 0, false
	}
	build := r.pricerFor(cloud)
	if build == nil {
		return 0, false
	}
	pricer, err := build(ctx, cfg)
	if err != nil {
		return 0, false
	}
	price, err := pricer.HourlyPrice(ctx, region, instanceType)
	return price, err == nil && price > 0
}

var cloudNames = map[string]string{"aws": "AWS", "azure": "Azure", "gcp": "Google Cloud"}

// priceLabels describe each cloud's list price in the rates' basis.
var priceLabels = map[string]string{"aws": "AWS on-demand list prices", "azure": "Azure pay-as-you-go list prices"}

func (r *Resolver) ttl() time.Duration {
	if r.TTL > 0 {
		return r.TTL
	}
	return 12 * time.Hour
}

func customRates(p finopsv1.PricingConfig) (Rates, bool) {
	cpu, errCPU := strconv.ParseFloat(p.CPUCoreHour, 64)
	mem, errMem := strconv.ParseFloat(p.MemoryGBHour, 64)
	if errCPU != nil || errMem != nil || cpu < 0 || mem < 0 || (cpu == 0 && mem == 0) {
		return Rates{}, false
	}
	return Rates{CPUCoreHour: cpu, MemoryGBHour: mem, Currency: currency(p.Currency), Basis: "Custom rates from CostDeckConfig"}, true
}

func heuristicRates(cloud, cur string) Rates {
	h, ok := heuristics[cloud]
	if !ok {
		h = heuristics["local"]
	}
	basis := "Heuristic list-price estimate"
	if cloud != "local" {
		basis += " (" + cloud + ")"
	}
	return Rates{CPUCoreHour: h[0], MemoryGBHour: h[1], Currency: currency(cur), Basis: basis}
}

func currency(c string) string {
	if c == "" {
		return "USD"
	}
	return strings.ToUpper(c)
}

// DetectCloud names the cloud the nodes run on: aws, azure, gcp or local.
func DetectCloud(nodes []corev1.Node) string {
	for _, n := range nodes {
		id := strings.ToLower(n.Spec.ProviderID)
		switch {
		case strings.HasPrefix(id, "aws://"):
			return "aws"
		case strings.HasPrefix(id, "azure://"):
			return "azure"
		case strings.HasPrefix(id, "gce://"):
			return "gcp"
		}
	}
	return "local"
}

func nodeFingerprint(nodes []corev1.Node) string {
	counts := map[string]int{}
	for _, n := range nodes {
		counts[n.Labels[corev1.LabelTopologyRegion]+"/"+n.Labels[corev1.LabelInstanceTypeStable]]++
	}
	return fmt.Sprint(counts)
}

// nodeRates prices every node at its list price and splits the total into a per-core and
// a per-GiB rate. The split keeps the heuristic CPU:memory price ratio and scales it so
// that the rates reproduce the cluster's real hourly bill.
func nodeRates(ctx context.Context, cloud string, build func(context.Context, *finopsv1.CostDeckConfig) (NodePricer, error), cfg *finopsv1.CostDeckConfig, nodes []corev1.Node) (Rates, error) {
	pricer, err := build(ctx, cfg)
	if err != nil {
		return Rates{}, err
	}
	base := heuristics[cloud]
	prices := map[string]float64{}
	var billed, modelled float64
	spot := 0
	types := map[string]bool{}
	for _, n := range nodes {
		region := n.Labels[corev1.LabelTopologyRegion]
		instanceType := n.Labels[corev1.LabelInstanceTypeStable]
		if region == "" || instanceType == "" {
			continue
		}
		key := region + "/" + instanceType
		price, ok := prices[key]
		if !ok {
			if price, err = pricer.HourlyPrice(ctx, region, instanceType); err != nil {
				return Rates{}, fmt.Errorf("%s in %s: %w", instanceType, region, err)
			}
			prices[key] = price
		}
		if IsSpot(n) {
			spot++
		}
		types[instanceType] = true
		cpu := n.Status.Capacity.Cpu().AsApproximateFloat64()
		memGiB := n.Status.Capacity.Memory().AsApproximateFloat64() / (1 << 30)
		billed += price
		modelled += cpu*base[0] + memGiB*base[1]
	}
	if billed == 0 || modelled == 0 {
		return Rates{}, fmt.Errorf("no node carries instance-type and region labels")
	}
	scale := billed / modelled
	names := make([]string, 0, len(types))
	for t := range types {
		names = append(names, t)
	}
	basis := fmt.Sprintf("%s for %d node(s) (%s)", priceLabels[cloud], len(nodes), strings.Join(sortedFirst(names, 4), ", "))
	if spot > 0 {
		basis += fmt.Sprintf("; %d spot node(s) priced at the regular rate", spot)
	}
	return Rates{CPUCoreHour: base[0] * scale, MemoryGBHour: base[1] * scale, Currency: "USD", Basis: basis}, nil
}

// IsSpot reports whether a node runs on spot or low-priority capacity.
func IsSpot(n corev1.Node) bool {
	return strings.EqualFold(n.Labels["eks.amazonaws.com/capacityType"], "SPOT") ||
		strings.EqualFold(n.Labels["karpenter.sh/capacity-type"], "spot") ||
		strings.EqualFold(n.Labels["kubernetes.azure.com/scalesetpriority"], "spot")
}

// AzureRetail returns the shared Azure Retail Prices client. The API is public, so the
// configuration is not needed.
func AzureRetail() func(context.Context, *finopsv1.CostDeckConfig) (NodePricer, error) {
	pricer := NewAzurePricer()
	return func(context.Context, *finopsv1.CostDeckConfig) (NodePricer, error) { return pricer, nil }
}

func sortedFirst(items []string, n int) []string {
	sort.Strings(items)
	if len(items) > n {
		return append(items[:n:n], fmt.Sprintf("+%d more", len(items)-n))
	}
	return items
}
