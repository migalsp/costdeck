package rightsizing

import (
	"math"
	"testing"

	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/migalsp/costdeck-operator/internal/metrics"
	"github.com/migalsp/costdeck-operator/internal/pricing"
)

const mi = mebibyte

func usage(cpu, mem string) metrics.Usage {
	return metrics.Usage{CPU: resource.MustParse(cpu), Memory: resource.MustParse(mem)}
}

var rates = pricing.Rates{CPUCoreHour: 0.04, MemoryGBHour: 0.005, Currency: "USD"}

func TestAnalyzeRecommendsReductionsAndPricesThem(t *testing.T) {
	workloads := []Workload{{
		Kind: "Deployment", Name: "api", Replicas: 2,
		Containers: []Container{{Name: "api", CPURequest: 1, MemoryRequest: 1024 * mi}},
	}}
	demand := map[metrics.ContainerKey]metrics.Usage{
		// The busiest replica decides: 100m and 200Mi.
		{Pod: "api-7d9f8c-abcde", Container: "api"}: usage("100m", "200Mi"),
		{Pod: "api-7d9f8c-fghij", Container: "api"}: usage("40m", "150Mi"),
		// A different workload whose name merely starts with "api".
		{Pod: "api-gateway-5c6b7-klmno", Container: "api"}: usage("4", "8Gi"),
	}

	r := Analyze(workloads, demand, rates, DefaultSettings(true))
	c := r.Workloads[0].Containers[0]
	if c.CPU.Action != ActionReduce || c.CPU.Observed != "100m" || c.CPU.Recommended != "120m" || c.CPU.Request != "1" {
		t.Errorf("CPU advice = %+v, want reduce 1 -> 120m (100m p95 + 20%%)", c.CPU)
	}
	if c.Memory.Action != ActionReduce || c.Memory.Recommended != "240Mi" {
		t.Errorf("memory advice = %+v, want reduce to 240Mi", c.Memory)
	}

	// (1 - 0.12) cores * 0.04 + (1024 - 240) MiB in GiB * 0.005, per replica-hour.
	want := ((1-0.12)*0.04 + float64(1024-240)/1024*0.005) * 2 * pricing.HoursPerMonth
	if math.Abs(r.MonthlySavings-want) > 1e-6 || math.Abs(r.Workloads[0].MonthlySavings-want) > 1e-6 {
		t.Errorf("monthly savings = %.4f / %.4f, want %.4f", r.MonthlySavings, r.Workloads[0].MonthlySavings, want)
	}
}

func TestAnalyzeActions(t *testing.T) {
	workloads := []Workload{
		{Kind: "Deployment", Name: "busy", Replicas: 1, Containers: []Container{{Name: "app", CPURequest: 0.1, MemoryRequest: 128 * mi}}},
		{Kind: "Deployment", Name: "fit", Replicas: 1, Containers: []Container{{Name: "app", CPURequest: 0.13, MemoryRequest: 256 * mi}}},
		{Kind: "StatefulSet", Name: "db", Replicas: 1, Containers: []Container{{Name: "pg"}}},
		{Kind: "Deployment", Name: "idle", Replicas: 1, Containers: []Container{{Name: "app", CPURequest: 0.5}}},
	}
	demand := map[metrics.ContainerKey]metrics.Usage{
		{Pod: "busy-6f7d8-aaaaa", Container: "app"}: usage("300m", "400Mi"),
		{Pod: "fit-6f7d8-bbbbb", Container: "app"}:  usage("100m", "200Mi"),
		{Pod: "db-0", Container: "pg"}:              usage("5m", "10Mi"),
	}
	byName := map[string]ContainerAdvice{}
	for _, w := range Analyze(workloads, demand, rates, DefaultSettings(true)).Workloads {
		byName[w.Name] = w.Containers[0]
	}

	if a := byName["busy"]; a.CPU.Action != ActionIncrease || a.Memory.Action != ActionIncrease {
		t.Errorf("busy = %+v, usage above the request must be flagged", a)
	}
	if a := byName["fit"]; a.CPU.Action != ActionKeep || a.CPU.Recommended != "" || a.Memory.Action != ActionKeep {
		t.Errorf("fit = %+v, a trim under 20%% is not worth recommending", a)
	}
	if a := byName["db"]; a.CPU.Action != ActionSet || a.CPU.Recommended != "10m" || a.Memory.Recommended != "32Mi" {
		t.Errorf("db = %+v, unset requests get a floored starting point", a)
	}
	if a := byName["idle"]; a.CPU.Action != ActionUnknown || a.CPU.Request != "500m" || a.CPU.Recommended != "" {
		t.Errorf("idle = %+v, no observed usage must not produce a recommendation", a)
	}
}

func TestSnapshotDemandGetsMoreHeadroom(t *testing.T) {
	workloads := []Workload{{Kind: "Deployment", Name: "api", Replicas: 1, Containers: []Container{{Name: "api", CPURequest: 2}}}}
	demand := map[metrics.ContainerKey]metrics.Usage{{Pod: "api-1a2b3-ccccc", Container: "api"}: usage("100m", "64Mi")}
	c := Analyze(workloads, demand, rates, DefaultSettings(false)).Workloads[0].Containers[0]
	if c.CPU.Recommended != "150m" {
		t.Errorf("snapshot recommendation = %s, want 150m (100m + 50%%)", c.CPU.Recommended)
	}
}

func TestWorkloadsSortBySavings(t *testing.T) {
	workloads := []Workload{
		{Kind: "Deployment", Name: "small", Replicas: 1, Containers: []Container{{Name: "c", CPURequest: 0.5}}},
		{Kind: "Deployment", Name: "big", Replicas: 1, Containers: []Container{{Name: "c", CPURequest: 4}}},
	}
	demand := map[metrics.ContainerKey]metrics.Usage{
		{Pod: "small-1a2b3-aaaaa", Container: "c"}: usage("10m", "10Mi"),
		{Pod: "big-1a2b3-aaaaa", Container: "c"}:   usage("10m", "10Mi"),
	}
	if got := Analyze(workloads, demand, rates, DefaultSettings(true)).Workloads; got[0].Name != "big" {
		t.Errorf("first workload = %s, want the one that saves most", got[0].Name)
	}
}
