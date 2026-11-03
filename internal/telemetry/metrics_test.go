package telemetry

import (
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

func series(c prometheus.Collector, matching string) map[string]float64 {
	ch := make(chan prometheus.Metric)
	go func() {
		c.Collect(ch)
		close(ch)
	}()
	out := map[string]float64{}
	for m := range ch {
		var d dto.Metric
		_ = m.Write(&d)
		pairs := make([]string, 0, len(d.GetLabel()))
		for _, l := range d.GetLabel() {
			pairs = append(pairs, l.GetName()+"="+l.GetValue())
		}
		if key := strings.Join(pairs, ","); strings.Contains(key, matching) {
			out[key] = d.GetGauge().GetValue()
		}
	}
	return out
}

func expectSeries(t *testing.T, name string, c prometheus.Collector, matching string, want map[string]float64) {
	t.Helper()
	if got := series(c, matching); !maps.Equal(got, want) {
		t.Errorf("%s = %v, want %v", name, got, want)
	}
}

func TestSeriesAreRegisteredWithTheManagerOnce(t *testing.T) {
	collectors := map[string]prometheus.Collector{
		"costdeck_scaling_desired_up": DesiredUp, "costdeck_scaling_ready": Ready, "costdeck_scaling_override_active": OverrideActive,
		"costdeck_estimated_hourly_savings": HourlySavings, "costdeck_namespace_estimated_monthly_cost": NamespaceMonthlyCost,
		"costdeck_namespace_cpu_usage_cores": NamespaceCPUUsage, "costdeck_namespace_memory_usage_bytes": NamespaceMemoryUsage,
		"costdeck_cluster_estimated_monthly_cost": ClusterMonthlyCost, "costdeck_budget_spent_ratio": BudgetSpent,
		"costdeck_budget_forecast_ratio": BudgetForecast,
	}
	for _, name := range slices.Sorted(maps.Keys(collectors)) {
		c := collectors[name]
		var already prometheus.AlreadyRegisteredError
		if err := metrics.Registry.Register(c); !errors.As(err, &already) || already.ExistingCollector != c {
			t.Errorf("%s: registering again = %v, want it already registered with the manager's registry", name, err)
		}
		descs := make(chan *prometheus.Desc, 1)
		c.Describe(descs)
		if d := (<-descs).String(); !strings.Contains(d, `fqName: "`+name+`"`) {
			t.Errorf("collector registered as %s describes %s", name, d)
		}
	}
}

func TestRecordScaling(t *testing.T) {
	t.Cleanup(func() { ForgetScaling("group", "telemetry-dev"); ForgetScaling("group", "telemetry-prod") })
	const dev = "name=telemetry-dev"

	RecordScaling("group", "telemetry-dev", true, false, true, 1.5, "USD")
	RecordScaling("group", "telemetry-prod", false, true, false, 0, "USD")
	expectSeries(t, "desired up", DesiredUp, dev, map[string]float64{"kind=group," + dev: 1})
	expectSeries(t, "ready", Ready, dev, map[string]float64{"kind=group," + dev: 0})
	expectSeries(t, "override", OverrideActive, dev, map[string]float64{"kind=group," + dev: 1})
	expectSeries(t, "savings", HourlySavings, dev, map[string]float64{"currency=USD,kind=group," + dev: 1.5})

	RecordScaling("group", "telemetry-dev", false, true, false, 2, "EUR")
	expectSeries(t, "savings after a currency change", HourlySavings, dev, map[string]float64{"currency=EUR,kind=group," + dev: 2})
	expectSeries(t, "desired up after scaling down", DesiredUp, dev, map[string]float64{"kind=group," + dev: 0})

	RecordScaling("group", "telemetry-dev", false, true, false, 2, "")
	expectSeries(t, "savings without a currency", HourlySavings, dev, map[string]float64{})

	ForgetScaling("group", "telemetry-dev")
	for name, c := range map[string]prometheus.Collector{"desired up": DesiredUp, "ready": Ready, "override": OverrideActive, "savings": HourlySavings} {
		expectSeries(t, name+" after forgetting", c, dev, map[string]float64{})
	}
	expectSeries(t, "another group's series", Ready, "name=telemetry-prod", map[string]float64{"kind=group,name=telemetry-prod": 1})
}

func TestRecordNamespace(t *testing.T) {
	t.Cleanup(func() { ForgetNamespace("telemetry-shop"); ForgetNamespace("telemetry-dev") })
	const shop = "namespace=telemetry-shop"

	RecordNamespace("telemetry-shop", 1.25, 512<<20, 80, "USD")
	RecordNamespace("telemetry-dev", 0.5, 1<<30, 20, "USD")
	expectSeries(t, "cpu", NamespaceCPUUsage, shop, map[string]float64{shop: 1.25})
	expectSeries(t, "memory", NamespaceMemoryUsage, shop, map[string]float64{shop: 512 << 20})
	expectSeries(t, "cost", NamespaceMonthlyCost, shop, map[string]float64{"currency=USD," + shop: 80})

	RecordNamespace("telemetry-shop", 1, 256<<20, 70, "EUR")
	expectSeries(t, "cost after a currency change", NamespaceMonthlyCost, shop, map[string]float64{"currency=EUR," + shop: 70})

	ForgetNamespace("telemetry-shop")
	for name, c := range map[string]prometheus.Collector{"cpu": NamespaceCPUUsage, "memory": NamespaceMemoryUsage, "cost": NamespaceMonthlyCost} {
		expectSeries(t, name+" after forgetting", c, shop, map[string]float64{})
	}
	expectSeries(t, "another namespace's cost", NamespaceMonthlyCost, "namespace=telemetry-dev", map[string]float64{"currency=USD,namespace=telemetry-dev": 20})
}

func TestRecordClusterReplacesEveryPart(t *testing.T) {
	t.Cleanup(ClusterMonthlyCost.Reset)
	RecordCluster(map[string]float64{"nodes": 1000, "requested": 600, "storage": 40}, "USD")
	expectSeries(t, "cluster", ClusterMonthlyCost, "", map[string]float64{
		"currency=USD,part=nodes": 1000, "currency=USD,part=requested": 600, "currency=USD,part=storage": 40,
	})
	RecordCluster(map[string]float64{"nodes": 900}, "EUR")
	expectSeries(t, "cluster after the next sample", ClusterMonthlyCost, "", map[string]float64{"currency=EUR,part=nodes": 900})
}

func TestKeepBudgetsDropsRemovedBudgets(t *testing.T) {
	t.Cleanup(func() { KeepBudgets(nil) })
	RecordBudget("telemetry-team", 0.5, 1.2)
	RecordBudget("telemetry-cluster", 0.9, 0.95)
	KeepBudgets([]string{"telemetry-team", "telemetry-cluster"})
	expectSeries(t, "spent", BudgetSpent, "budget=telemetry-", map[string]float64{"budget=telemetry-team": 0.5, "budget=telemetry-cluster": 0.9})
	expectSeries(t, "forecast", BudgetForecast, "budget=telemetry-", map[string]float64{"budget=telemetry-team": 1.2, "budget=telemetry-cluster": 0.95})

	RecordBudget("telemetry-cluster", 1, 1.1)
	KeepBudgets([]string{"telemetry-cluster"})
	expectSeries(t, "spent after removing a budget", BudgetSpent, "budget=telemetry-", map[string]float64{"budget=telemetry-cluster": 1})
	expectSeries(t, "forecast after removing a budget", BudgetForecast, "budget=telemetry-", map[string]float64{"budget=telemetry-cluster": 1.1})
}
