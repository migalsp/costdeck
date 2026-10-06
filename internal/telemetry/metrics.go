// Package telemetry exports CostDeck's state as Prometheus metrics on the manager's
// metrics endpoint, so schedules, overrides and savings can be graphed and alerted on.
package telemetry

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	// DesiredUp is 1 while a group or config should be up.
	DesiredUp = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "costdeck_scaling_desired_up",
		Help: "1 while the ScalingGroup or ScalingConfig should be scaled up, 0 while it should be down.",
	}, []string{"kind", "name"})

	// Ready is 1 when every target reached the desired state.
	Ready = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "costdeck_scaling_ready",
		Help: "1 when every target of the ScalingGroup or ScalingConfig reached the desired state.",
	}, []string{"kind", "name"})

	// OverrideActive is 1 while a manual override ignores the schedule.
	OverrideActive = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "costdeck_scaling_override_active",
		Help: "1 while a manual override (spec.active) makes the ScalingGroup or ScalingConfig ignore its schedule.",
	}, []string{"kind", "name"})

	// HourlySavings estimates what the workloads kept down would cost per hour.
	HourlySavings = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "costdeck_estimated_hourly_savings",
		Help: "Estimated hourly cost of the workloads CostDeck currently keeps scaled down (requests x original replicas x rates).",
	}, []string{"kind", "name", "currency"})

	// NamespaceMonthlyCost estimates the monthly cost of a namespace's requests.
	NamespaceMonthlyCost = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "costdeck_namespace_estimated_monthly_cost",
		Help: "Estimated monthly cost of the CPU and memory requests of running pods in a namespace.",
	}, []string{"namespace", "currency"})

	// NamespaceCPUUsage is the observed CPU usage of a namespace in cores.
	NamespaceCPUUsage = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "costdeck_namespace_cpu_usage_cores",
		Help: "Observed CPU usage of all containers in a namespace.",
	}, []string{"namespace"})

	// NamespaceMemoryUsage is the observed memory usage of a namespace in bytes.
	NamespaceMemoryUsage = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "costdeck_namespace_memory_usage_bytes",
		Help: "Observed working-set memory of all containers in a namespace.",
	}, []string{"namespace"})

	// ClusterMonthlyCost is the cluster's cost at the current run rate, split into what the
	// nodes cost, what pods request and what they use.
	ClusterMonthlyCost = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "costdeck_cluster_estimated_monthly_cost",
		Help: "Estimated monthly cost of the cluster at the current run rate: part=nodes, storage or network (together the bill), requested or used.",
	}, []string{"part", "currency"})

	// BudgetSpent and BudgetForecast are spending and forecast as a share of a budget.
	BudgetSpent = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "costdeck_budget_spent_ratio",
		Help: "Spending this month as a share of the budget's monthly limit (1 = the whole budget).",
	}, []string{"budget"})
	BudgetForecast = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "costdeck_budget_forecast_ratio",
		Help: "Forecast spending for this month as a share of the budget's monthly limit.",
	}, []string{"budget"})
)

func init() {
	metrics.Registry.MustRegister(DesiredUp, Ready, OverrideActive, HourlySavings,
		NamespaceMonthlyCost, NamespaceCPUUsage, NamespaceMemoryUsage, ClusterMonthlyCost, BudgetSpent, BudgetForecast)
}

// RecordCluster publishes the cluster's monthly run rate by part.
func RecordCluster(parts map[string]float64, currency string) {
	ClusterMonthlyCost.Reset()
	for part, v := range parts {
		ClusterMonthlyCost.WithLabelValues(part, currency).Set(v)
	}
}

func bool01(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// RecordScaling publishes the state of a group or config.
func RecordScaling(kind, name string, desiredUp, ready, override bool, hourlySavings float64, currency string) {
	DesiredUp.WithLabelValues(kind, name).Set(bool01(desiredUp))
	Ready.WithLabelValues(kind, name).Set(bool01(ready))
	OverrideActive.WithLabelValues(kind, name).Set(bool01(override))
	HourlySavings.DeletePartialMatch(prometheus.Labels{"kind": kind, "name": name})
	if currency != "" {
		HourlySavings.WithLabelValues(kind, name, currency).Set(hourlySavings)
	}
}

// ForgetScaling removes the series of a deleted group or config.
func ForgetScaling(kind, name string) {
	labels := prometheus.Labels{"kind": kind, "name": name}
	DesiredUp.DeletePartialMatch(labels)
	Ready.DeletePartialMatch(labels)
	OverrideActive.DeletePartialMatch(labels)
	HourlySavings.DeletePartialMatch(labels)
}

// RecordNamespace publishes the usage and estimated cost of a namespace.
func RecordNamespace(namespace string, cpuCores, memBytes, monthlyCost float64, currency string) {
	NamespaceCPUUsage.WithLabelValues(namespace).Set(cpuCores)
	NamespaceMemoryUsage.WithLabelValues(namespace).Set(memBytes)
	NamespaceMonthlyCost.DeletePartialMatch(prometheus.Labels{"namespace": namespace})
	NamespaceMonthlyCost.WithLabelValues(namespace, currency).Set(monthlyCost)
}

// ForgetNamespace removes the series of a namespace that is no longer tracked.
func ForgetNamespace(namespace string) {
	labels := prometheus.Labels{"namespace": namespace}
	NamespaceCPUUsage.DeletePartialMatch(labels)
	NamespaceMemoryUsage.DeletePartialMatch(labels)
	NamespaceMonthlyCost.DeletePartialMatch(labels)
}

// RecordBudget publishes one budget's spending and forecast ratios.
func RecordBudget(name string, spent, forecast float64) {
	BudgetSpent.WithLabelValues(name).Set(spent)
	BudgetForecast.WithLabelValues(name).Set(forecast)
}

var budgetsSeen = map[string]bool{}

// KeepBudgets drops the series of budgets that no longer exist.
func KeepBudgets(names []string) {
	keep := map[string]bool{}
	for _, n := range names {
		keep[n] = true
	}
	for n := range budgetsSeen {
		if !keep[n] {
			BudgetSpent.DeleteLabelValues(n)
			BudgetForecast.DeleteLabelValues(n)
		}
	}
	budgetsSeen = keep
}
