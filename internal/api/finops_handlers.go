package api

import (
	"context"
	"net/http"
	"sort"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/migalsp/costdeck-operator/internal/config"
	"github.com/migalsp/costdeck-operator/internal/finops"
	"github.com/migalsp/costdeck-operator/internal/metrics"
	"github.com/migalsp/costdeck-operator/internal/pricing"
)

// warmup computes right-sizing advice in the background, so the overview never waits for
// one query per namespace. One run at a time; a run skips namespaces already cached.
type warmup struct {
	mu      sync.Mutex
	running bool
	// failed remembers namespaces with no usable data, so they are not reported as pending
	// forever; they are retried after recommendationTTL.
	failed map[string]time.Time
}

func (w *warmup) failedRecently(ns string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	at, ok := w.failed[ns]
	return ok && time.Since(at) < recommendationTTL
}

func (w *warmup) markFailed(ns string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.failed == nil {
		w.failed = map[string]time.Time{}
	}
	w.failed[ns] = time.Now()
}

// warmupConcurrency bounds the metrics queries a warm-up runs at once.
const warmupConcurrency = 4

// handleFinOpsOverview serves the cost overview: run rate, month to date, history,
// savings, every namespace and the ranked opportunities.
func (s *Server) handleFinOpsOverview(w http.ResponseWriter, r *http.Request) {
	overview, err := s.finopsOverview(r.Context())
	if err != nil {
		writeK8sError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, overview)
}

// FinOpsOverview is finopsOverview for other components, such as the digest scheduler.
func (s *Server) FinOpsOverview(ctx context.Context) (finops.Overview, error) {
	return s.finopsOverview(ctx)
}

// finopsOverview builds the overview from a fresh snapshot, the cost history and the
// right-sizing advice cached so far; missing advice is calculated in the background.
func (s *Server) finopsOverview(ctx context.Context) (finops.Overview, error) {
	ns := config.OperatorNamespace()
	snap, err := finops.Build(ctx, s.Client, s.costRates(ctx), finops.Options{Namespace: ns, Now: time.Now(), Live: s.APIReader})
	if err != nil {
		return finops.Overview{}, err
	}
	ledger, err := s.costLedger(ctx)
	if err != nil {
		logf.FromContext(ctx).Error(err, "Could not read the cost history")
		ledger = &finops.Ledger{}
	}

	advice := map[string]float64{}
	var missing []string
	for _, n := range snap.Namespaces {
		if n.CPURequested == 0 && n.MemRequested == 0 {
			continue // nothing requested, nothing to shrink
		}
		if report, ok := s.recommendationCache.get(n.Name); ok {
			advice[n.Name] = report.MonthlySavings
		} else if !s.warm.failedRecently(n.Name) {
			missing = append(missing, n.Name)
		}
	}
	s.warmRecommendations(missing)
	return finops.NewOverview(snap, ledger, advice, len(missing)), nil
}

// warmRecommendations fills the advice cache for the given namespaces in the background.
func (s *Server) warmRecommendations(namespaces []string) {
	if len(namespaces) == 0 {
		return
	}
	s.warm.mu.Lock()
	if s.warm.running {
		s.warm.mu.Unlock()
		return
	}
	s.warm.running = true
	s.warm.mu.Unlock()

	go func() {
		defer func() {
			s.warm.mu.Lock()
			s.warm.running = false
			s.warm.mu.Unlock()
		}()
		ctx := s.backgroundContext()
		sem := make(chan struct{}, warmupConcurrency)
		var wg sync.WaitGroup
		for _, ns := range namespaces {
			wg.Add(1)
			sem <- struct{}{}
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				if _, err := s.recommendations(ctx, ns); err != nil {
					// No usage data yet; not pending until the next attempt.
					s.warm.markFailed(ns)
				}
			}()
		}
		wg.Wait()
	}()
}

// ledgerTTL is how long the dashboard reuses the cost history. The leader books it every
// five minutes and it can approach a megabyte, so reading it on every poll was the largest
// request the dashboard made.
const ledgerTTL = time.Minute

// costLedger returns the cost history, read from its ConfigMap at most every ledgerTTL.
// Callers only read it.
func (s *Server) costLedger(ctx context.Context) (*finops.Ledger, error) {
	s.ledgerMu.Lock()
	defer s.ledgerMu.Unlock()
	if s.ledger != nil && time.Since(s.ledgerAt) < ledgerTTL {
		return s.ledger, nil
	}
	l, err := finops.LoadLedger(ctx, s.Client, config.OperatorNamespace())
	if err != nil {
		return nil, err
	}
	s.ledger, s.ledgerAt = l, time.Now()
	return l, nil
}

// handleFinOpsNodes serves the node view: cost, bin-packing and findings per node.
func (s *Server) handleFinOpsNodes(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var nodes corev1.NodeList
	if err := s.Client.List(ctx, &nodes); err != nil {
		writeK8sError(w, err)
		return
	}
	var pods corev1.PodList
	if err := s.Client.List(ctx, &pods); err != nil {
		writeK8sError(w, err)
		return
	}
	rates := s.costRates(ctx)
	rep := finops.BuildNodes(nodes.Items, pods.Items, s.getNodeMetricsMap(ctx), rates, time.Now())
	cloud := pricing.DetectCloud(nodes.Items)
	live := func(region, instanceType string) (float64, bool) {
		return s.pricingResolver().InstancePrice(ctx, cloud, region, instanceType)
	}
	finops.RecommendNodes(&rep, cloud, live, s.nonProductionCompute(ctx, pods.Items, rates))
	writeJSON(w, http.StatusOK, map[string]any{"k8sVersion": s.getK8sVersion(), "cloud": cloud, "report": rep})
}

// NodePod is one pod on a node with what it requests and uses.
type NodePod struct {
	Namespace     string  `json:"namespace"`
	Name          string  `json:"name"`
	Phase         string  `json:"phase"`
	DaemonSet     bool    `json:"daemonSet"`
	CPURequest    float64 `json:"cpuRequest"`
	CPUUsed       float64 `json:"cpuUsed"`
	MemoryRequest float64 `json:"memoryRequestGiB"`
	MemoryUsed    float64 `json:"memoryUsedGiB"`
	MonthlyCost   float64 `json:"monthlyCost"`
}

// handleFinOpsNodePods lists the pods running on one node.
func (s *Server) handleFinOpsNodePods(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	name := r.PathValue("name")
	var pods corev1.PodList
	if err := s.Client.List(ctx, &pods); err != nil {
		writeK8sError(w, err)
		return
	}
	// Usage comes from the cluster-wide reading the controllers already share; a missing
	// reading only leaves the usage columns empty.
	usage, err := s.metricsProvider().ClusterPodUsage(ctx)
	if err != nil {
		usage = map[string]metrics.Usage{}
	}
	rates := s.costRates(ctx)
	out := []NodePod{}
	for i := range pods.Items {
		p := &pods.Items[i]
		if p.Spec.NodeName != name || p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
			continue
		}
		cpu, mem := finops.PodRequests(p)
		np := NodePod{
			Namespace: p.Namespace, Name: p.Name, Phase: string(p.Status.Phase),
			CPURequest: cpu.AsApproximateFloat64(), MemoryRequest: mem.AsApproximateFloat64() / (1 << 30),
			MonthlyCost: rates.Monthly(cpu, mem),
		}
		for _, o := range p.OwnerReferences {
			np.DaemonSet = np.DaemonSet || o.Kind == "DaemonSet"
		}
		if u, ok := usage[p.Namespace+"/"+p.Name]; ok {
			np.CPUUsed = u.CPU.AsApproximateFloat64()
			np.MemoryUsed = u.Memory.AsApproximateFloat64() / (1 << 30)
		}
		out = append(out, np)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].MonthlyCost > out[j].MonthlyCost })
	writeJSON(w, http.StatusOK, out)
}

// handleFinOpsInfra serves volumes and load balancers with their cost and state.
func (s *Server) handleFinOpsInfra(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	snap, err := finops.Build(ctx, s.Client, s.costRates(ctx), finops.Options{Namespace: config.OperatorNamespace(), Now: time.Now(), Live: s.APIReader})
	if err != nil {
		writeK8sError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"currency": snap.Rates.Currency, "cloud": snap.Cloud, "infra": snap.Infra, "error": snap.InfraError})
}

// handleBudgets serves every budget against the month so far, the anomaly settings and the
// recent alerts.
func (s *Server) handleBudgets(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ns := config.OperatorNamespace()
	cfg, err := config.Get(ctx, s.Client)
	if err != nil {
		writeK8sError(w, err)
		return
	}
	rates := s.costRates(ctx)
	snap, err := finops.Build(ctx, s.Client, rates, finops.Options{Namespace: ns, Now: time.Now(), Live: s.APIReader})
	if err != nil {
		writeK8sError(w, err)
		return
	}
	ledger, err := s.costLedger(ctx)
	if err != nil {
		writeK8sError(w, err)
		return
	}
	events, err := finops.AlertEvents(ctx, s.Client, ns)
	if err != nil {
		writeK8sError(w, err)
		return
	}
	teams := map[string]bool{}
	names := []string{}
	for _, n := range snap.Namespaces {
		names = append(names, n.Name)
		if t := finops.Team(n.Labels); t != "" {
			teams[t] = true
		}
	}
	teamList := make([]string, 0, len(teams))
	for t := range teams {
		teamList = append(teamList, t)
	}
	sort.Strings(teamList)
	wx := cfg.Spec.Integrations.Messenger
	writeJSON(w, http.StatusOK, map[string]any{
		"currency":   rates.Currency,
		"budgets":    finops.EvaluateBudgets(cfg.Spec.Budgets, snap, ledger),
		"anomalies":  cfg.Spec.Alerts.Anomalies,
		"events":     events,
		"webexReady": wx != nil && wx.Webex != nil && wx.Webex.Enabled && wx.Webex.RoomID != "",
		"scopes":     map[string]any{"namespaces": names, "teams": teamList},
	})
}

// nonProductionCompute is the monthly cost of what non-production namespaces request,
// DaemonSets aside: the part of the cluster that could move to spot capacity.
func (s *Server) nonProductionCompute(ctx context.Context, pods []corev1.Pod, rates pricing.Rates) float64 {
	var namespaces corev1.NamespaceList
	if err := s.Client.List(ctx, &namespaces); err != nil {
		return 0
	}
	nonProd := map[string]bool{}
	for _, ns := range namespaces.Items {
		nonProd[ns.Name] = finops.Environment(ns.Name, ns.Labels) == finops.EnvNonProduction
	}
	var total float64
	for i := range pods {
		p := &pods[i]
		if !nonProd[p.Namespace] || p.Spec.NodeName == "" || p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
			continue
		}
		daemon := false
		for _, o := range p.OwnerReferences {
			daemon = daemon || o.Kind == "DaemonSet"
		}
		if !daemon {
			cpu, mem := finops.PodRequests(p)
			total += rates.Monthly(cpu, mem)
		}
	}
	return total
}

// handleBillingReconcile reads the cloud bill now instead of waiting for the next run.
func (s *Server) handleBillingReconcile(w http.ResponseWriter, r *http.Request) {
	st, err := (&finops.BillingReconciler{Client: s.Client, Namespace: config.OperatorNamespace()}).Reconcile(r.Context())
	if err != nil {
		writeK8sError(w, err)
		return
	}
	if st == nil {
		writeError(w, http.StatusBadRequest, "billing reconciliation is off; enable it under Settings → Cloud bill")
		return
	}
	writeJSON(w, http.StatusOK, st)
}
