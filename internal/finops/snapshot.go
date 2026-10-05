// Package finops turns what the cluster runs into the numbers a FinOps practice works
// with: what the nodes cost, how much of that the namespaces request and actually use,
// what the schedules keep down, and how all of it changes from day to day.
package finops

import (
	"context"
	"sort"
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/controller-runtime/pkg/client"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/config"
	"github.com/migalsp/costdeck-operator/internal/pricing"
)

// Snapshot is the cluster's cost picture at one moment.
type Snapshot struct {
	At         time.Time
	Rates      pricing.Rates
	Cloud      string
	Cluster    Cluster
	Namespaces []Namespace
	Schedules  []Schedule
	Infra      Infra
	// InfraError says why volumes and load balancers could not be read, if they could not.
	InfraError string
}

// Options says where Build reads from.
type Options struct {
	// Namespace is the operator namespace with the CostDeck resources.
	Namespace string
	Now       time.Time
	// Live reads objects that should not be cached cluster-wide (EndpointSlices); nil
	// means the main reader.
	Live client.Reader
}

// Cluster sums the nodes and everything the namespaces request and use. Costs are per hour.
type Cluster struct {
	Nodes        int     `json:"nodes"`
	SpotNodes    int     `json:"spotNodes"`
	CPUCores     float64 `json:"cpuCores"`
	MemoryGiB    float64 `json:"memoryGiB"`
	CPURequested float64 `json:"cpuRequested"`
	MemRequested float64 `json:"memoryRequestedGiB"`
	CPUUsed      float64 `json:"cpuUsed"`
	MemUsed      float64 `json:"memoryUsedGiB"`
	// ProvisionedHourly is what the nodes cost: the bill.
	ProvisionedHourly float64 `json:"provisionedHourly"`
	// RequestedHourly is the part of it reserved by pod requests.
	RequestedHourly float64 `json:"requestedHourly"`
	// UsedHourly is what the pods actually use, at the same rates.
	UsedHourly float64 `json:"usedHourly"`
	// StorageHourly and NetworkHourly are persistent volumes and load balancers, billed
	// apart from the nodes.
	StorageHourly float64 `json:"storageHourly"`
	NetworkHourly float64 `json:"networkHourly"`
}

// BillHourly is everything the cluster costs per hour: nodes, volumes and load balancers.
func (c Cluster) BillHourly() float64 { return c.ProvisionedHourly + c.StorageHourly + c.NetworkHourly }

// Namespace is one namespace's requests, usage and their cost per hour.
type Namespace struct {
	Name         string
	Labels       map[string]string
	Pods         int
	CPURequested float64
	CPUUsed      float64
	CPULimit     float64
	MemRequested float64 // GiB
	MemUsed      float64 // GiB
	MemLimit     float64 // GiB
	Insights     []string
	// Collected is false until the namespace has its first usage sample.
	Collected       bool
	RequestedHourly float64
	UsedHourly      float64
	// IdleHourly is requested but unused capacity, per resource, never negative.
	IdleHourly float64
	// StorageHourly and NetworkHourly are the namespace's volumes and load balancers.
	StorageHourly float64
	NetworkHourly float64
}

// TotalHourly is the namespace's compute, storage and load balancers per hour.
func (n Namespace) TotalHourly() float64 {
	return n.RequestedHourly + n.StorageHourly + n.NetworkHourly
}

// Schedule is a ScalingGroup or ScalingConfig and what it keeps down right now.
type Schedule struct {
	Kind         string // "group" or "config"
	Name         string
	Namespaces   []string
	DesiredState string
	Mode         string
	Phase        string
	SavedHourly  float64
}

// Key names a schedule uniquely across kinds.
func (s Schedule) Key() string { return s.Kind + "/" + s.Name }

const (
	gib          = 1 << 30
	modeAlwaysOn = "AlwaysOn"
)

// Build takes a snapshot from the objects the operator already watches: nodes, pods,
// namespaces, and the NamespaceFinOps, ScalingGroup and ScalingConfig resources in the
// operator namespace. It reads usage from the NamespaceFinOps samples instead of querying
// metrics again.
func Build(ctx context.Context, c client.Reader, rates pricing.Rates, opts Options) (Snapshot, error) {
	operatorNamespace, now := opts.Namespace, opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	snap := Snapshot{At: now, Rates: rates}

	var nodes corev1.NodeList
	if err := c.List(ctx, &nodes); err != nil {
		return snap, err
	}
	var cpuCap, memCap resource.Quantity
	for _, n := range nodes.Items {
		cpuCap.Add(*n.Status.Capacity.Cpu())
		memCap.Add(*n.Status.Capacity.Memory())
		if pricing.IsSpot(n) {
			snap.Cluster.SpotNodes++
		}
	}
	snap.Cluster.Nodes = len(nodes.Items)
	snap.Cluster.CPUCores = cpuCap.AsApproximateFloat64()
	snap.Cluster.MemoryGiB = memCap.AsApproximateFloat64() / gib
	snap.Cluster.ProvisionedHourly = rates.Hourly(cpuCap, memCap)
	snap.Cloud = pricing.DetectCloud(nodes.Items)

	var tracked finopsv1.NamespaceFinOpsList
	if err := c.List(ctx, &tracked, client.InNamespace(operatorNamespace)); err != nil {
		return snap, err
	}
	var namespaces corev1.NamespaceList
	if err := c.List(ctx, &namespaces); err != nil {
		return snap, err
	}
	labels := map[string]map[string]string{}
	for _, ns := range namespaces.Items {
		labels[ns.Name] = ns.Labels
	}
	var pods corev1.PodList
	if err := c.List(ctx, &pods); err != nil {
		return snap, err
	}
	running := map[string]int{}
	for _, p := range pods.Items {
		if p.Status.Phase == corev1.PodRunning {
			running[p.Namespace]++
		}
	}

	seen := map[string]bool{}
	for _, t := range tracked.Items {
		name := t.Spec.TargetNamespace
		if seen[name] {
			continue // a stray duplicate; the first one wins
		}
		if _, exists := labels[name]; !exists {
			continue // the namespace is gone; discovery removes its record shortly
		}
		seen[name] = true
		ns := Namespace{Name: name, Labels: labels[name], Pods: running[name], Insights: t.Status.Insights}
		if h := t.Status.History; len(h) > 0 {
			last := h[len(h)-1]
			ns.Collected = true
			ns.CPURequested, ns.CPUUsed, ns.CPULimit = cores(last.CPU.Requests), cores(last.CPU.Usage), cores(last.CPU.Limits)
			ns.MemRequested, ns.MemUsed, ns.MemLimit = gibs(last.Memory.Requests), gibs(last.Memory.Usage), gibs(last.Memory.Limits)
		}
		ns.RequestedHourly = ns.CPURequested*rates.CPUCoreHour + ns.MemRequested*rates.MemoryGBHour
		ns.UsedHourly = ns.CPUUsed*rates.CPUCoreHour + ns.MemUsed*rates.MemoryGBHour
		ns.IdleHourly = max(0, ns.CPURequested-ns.CPUUsed)*rates.CPUCoreHour + max(0, ns.MemRequested-ns.MemUsed)*rates.MemoryGBHour

		snap.Cluster.CPURequested += ns.CPURequested
		snap.Cluster.MemRequested += ns.MemRequested
		snap.Cluster.CPUUsed += ns.CPUUsed
		snap.Cluster.MemUsed += ns.MemUsed
		snap.Cluster.RequestedHourly += ns.RequestedHourly
		snap.Cluster.UsedHourly += ns.UsedHourly
		snap.Namespaces = append(snap.Namespaces, ns)
	}
	sort.Slice(snap.Namespaces, func(i, j int) bool { return snap.Namespaces[i].Name < snap.Namespaces[j].Name })

	var groups finopsv1.ScalingGroupList
	if err := c.List(ctx, &groups, client.InNamespace(operatorNamespace)); err != nil {
		return snap, err
	}
	for _, g := range groups.Items {
		snap.Schedules = append(snap.Schedules, Schedule{
			Kind: "group", Name: g.Name, Namespaces: g.Spec.Namespaces,
			DesiredState: g.Status.DesiredState, Mode: g.Status.Mode, Phase: g.Status.Phase,
			SavedHourly: parseMoney(g.Status.EstimatedHourlySavings),
		})
	}
	var configs finopsv1.ScalingConfigList
	if err := c.List(ctx, &configs, client.InNamespace(operatorNamespace)); err != nil {
		return snap, err
	}
	for _, cfg := range configs.Items {
		snap.Schedules = append(snap.Schedules, Schedule{
			Kind: "config", Name: cfg.Name, Namespaces: []string{cfg.Spec.TargetNamespace},
			DesiredState: cfg.Status.DesiredState, Mode: cfg.Status.Mode, Phase: cfg.Status.Phase,
			SavedHourly: parseMoney(cfg.Status.EstimatedHourlySavings),
		})
	}
	snap.addInfra(ctx, c, opts.Live, pods.Items)
	return snap, nil
}

// addInfra prices volumes and load balancers and charges them to their namespaces. It
// never fails the snapshot: without the permissions or the objects, compute alone remains.
func (s *Snapshot) addInfra(ctx context.Context, c, live client.Reader, pods []corev1.Pod) {
	if live == nil {
		live = c
	}
	custom := pricing.InfraRates{Currency: s.Rates.Currency}
	if cfg, err := config.Get(ctx, c); err == nil {
		custom.StorageGiBMonth, custom.LoadBalancerMonth = cfg.Spec.Pricing.StorageGiBMonth, cfg.Spec.Pricing.LoadBalancerMonth
		if cfg.Spec.Pricing.Currency != "" {
			custom.Currency = cfg.Spec.Pricing.Currency
		}
	}
	down := map[string]bool{}
	for _, n := range s.Namespaces {
		if sc, ok := s.ScheduleFor(n.Name); ok && sc.DesiredState == "Down" {
			down[n.Name] = true
		}
	}
	inf, err := buildInfra(ctx, c, live, s.Cloud, custom, pods, down)
	if err != nil {
		s.InfraError = err.Error()
		return
	}
	s.Infra = inf
	storage, network := map[string]float64{}, map[string]float64{}
	for _, v := range inf.Volumes {
		storage[v.Namespace] += v.MonthlyCost / pricing.HoursPerMonth
	}
	for _, lb := range inf.LoadBalancers {
		network[lb.Namespace] += lb.MonthlyCost / pricing.HoursPerMonth
	}
	s.Cluster.StorageHourly = inf.StorageMonthly / pricing.HoursPerMonth
	s.Cluster.NetworkHourly = inf.NetworkMonthly / pricing.HoursPerMonth
	for i := range s.Namespaces {
		s.Namespaces[i].StorageHourly = storage[s.Namespaces[i].Name]
		s.Namespaces[i].NetworkHourly = network[s.Namespaces[i].Name]
	}
}

// SavedHourly is what all schedules keep down right now.
func (s Snapshot) SavedHourly() float64 {
	var total float64
	for _, sc := range s.Schedules {
		total += sc.SavedHourly
	}
	return total
}

// ScheduleFor returns the schedule that governs a namespace: a group wins over a config,
// as in the controllers.
func (s Snapshot) ScheduleFor(namespace string) (Schedule, bool) {
	var cfgSched *Schedule
	for i, sc := range s.Schedules {
		for _, ns := range sc.Namespaces {
			if ns != namespace {
				continue
			}
			if sc.Kind == "group" {
				return sc, true
			}
			cfgSched = &s.Schedules[i]
		}
	}
	// A config that only carries workload rules has no hours and is kept up (AlwaysOn);
	// it does not schedule the namespace.
	if cfgSched != nil && cfgSched.Mode != "" && cfgSched.Mode != modeAlwaysOn {
		return *cfgSched, true
	}
	return Schedule{}, false
}

func cores(v string) float64 { return quantity(v) }
func gibs(v string) float64  { return quantity(v) / gib }

func quantity(v string) float64 {
	if v == "" {
		return 0
	}
	q, err := resource.ParseQuantity(v)
	if err != nil {
		return 0
	}
	return q.AsApproximateFloat64()
}

func parseMoney(v string) float64 {
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f < 0 {
		return 0
	}
	return f
}
