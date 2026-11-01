package rightsizing

import (
	"math"
	"slices"
	"testing"

	"github.com/migalsp/costdeck-operator/internal/jvm"
	"github.com/migalsp/costdeck-operator/internal/metrics"
)

func javaWorkload(name string, containers ...Container) []Workload {
	return []Workload{{Kind: "Deployment", Name: name, Replicas: 2, Containers: containers}}
}

func javaWith(flags ...string) *jvm.Runtime {
	return &jvm.Runtime{DetectedBy: jvm.DetectedByOptions, Options: jvm.Parse(flags)}
}

func findingCodes(a *JVMAdvice) []string {
	codes := make([]string, 0, len(a.Findings))
	for _, f := range a.Findings {
		codes = append(codes, f.Code)
	}
	return codes
}

func TestJavaMemoryAdviceCoversTheHeap(t *testing.T) {
	workloads := javaWorkload("orders", Container{Name: "app", CPURequest: 1, MemoryRequest: 4096 * mi, MemoryLimit: 4096 * mi, CPULimit: 2, Java: javaWith("-Xmx2g", "-XX:+UseG1GC")})
	demand := map[metrics.ContainerKey]metrics.Usage{{Pod: "orders-7d9f8c-bcdfg", Container: "app"}: usage("200m", "1200Mi")}

	c := Analyze(workloads, demand, nil, rates, DefaultSettings(true)).Workloads[0].Containers[0]
	if c.Memory.Action != ActionReduce || c.Memory.Recommended != "2560Mi" {
		t.Errorf("memory advice = %+v, want 2560Mi: the 2Gi heap plus a quarter of it for native memory, not 1440Mi from usage alone", c.Memory)
	}
	if c.JVM == nil || c.JVM.Heap != "2Gi" || c.JVM.HeapSource != "set by -Xmx" || !slices.Equal(c.JVM.Options, []string{"-Xmx2g", "-XX:+UseG1GC"}) {
		t.Errorf("JVM advice = %+v", c.JVM)
	}
	if c.JVM.RecommendedHeap != "" || c.JVM.MonthlySavings != 0 {
		t.Errorf("without JVM metrics there is no heap advice, got %+v", c.JVM)
	}
}

func TestJavaHeapAdviceFromMetrics(t *testing.T) {
	workloads := javaWorkload("orders", Container{Name: "app", CPURequest: 1, MemoryRequest: 4096 * mi, MemoryLimit: 4096 * mi, CPULimit: 2, Java: javaWith("-Xmx3g", "-XX:+UseG1GC")})
	demand := map[metrics.ContainerKey]metrics.Usage{{Pod: "orders-7d9f8c-bcdfg", Container: "app"}: usage("200m", "3500Mi")}
	heap := map[metrics.ContainerKey]metrics.HeapUsage{
		{Pod: "orders-7d9f8c-bcdfg", Container: "app"}: {Used: 2900 * mi, AfterGC: 300 * mi, NonHeap: 150 * mi},
		{Pod: "orders-7d9f8c-fghjk", Container: "app"}: {Used: 2800 * mi, AfterGC: 400 * mi, NonHeap: 160 * mi},
	}

	r := Analyze(workloads, demand, heap, rates, DefaultSettings(true))
	a := r.Workloads[0].Containers[0].JVM
	if a.HeapAfterGC != "400Mi" || a.NonHeap != "160Mi" || a.RecommendedHeap != "800Mi" || a.RecommendedMemory != "1056Mi" || a.RecommendedOptions != "-Xmx800m" {
		t.Errorf("JVM advice = %+v, want the busiest pod's 400Mi after GC doubled, plus its non-heap memory", a)
	}
	if !slices.Contains(findingCodes(a), "heap-oversized") {
		t.Errorf("findings = %q, want heap-oversized", findingCodes(a))
	}
	want := (4096 - 1056) * mi / float64(1<<30) * rates.MemoryGBHour * 2 * 730
	if math.Abs(a.MonthlySavings-want) > 0.001 || math.Abs(r.JavaMonthlySavings-want) > 0.001 {
		t.Errorf("savings = %v, report = %v, want %v for two replicas", a.MonthlySavings, r.JavaMonthlySavings, want)
	}
	if m := r.Workloads[0].Containers[0].Memory; m.Action == ActionReduce {
		t.Errorf("memory advice = %+v: while the heap is 3Gi the request must keep covering it; lowering the heap is a separate change", m)
	}
}

func TestJavaHeapAdviceOnlyWhenTheHeapIsOff(t *testing.T) {
	workloads := javaWorkload("orders", Container{Name: "app", MemoryRequest: 2048 * mi, MemoryLimit: 2048 * mi, CPULimit: 2, Java: javaWith("-Xmx1g", "-XX:+UseG1GC")})
	heap := map[metrics.ContainerKey]metrics.HeapUsage{{Pod: "orders-7d9f8c-bcdfg", Container: "app"}: {Used: 900 * mi, AfterGC: 450 * mi}}
	a := Analyze(workloads, nil, heap, rates, DefaultSettings(true)).Workloads[0].Containers[0].JVM
	if a.HeapAfterGC != "450Mi" || a.RecommendedHeap != "" || a.RecommendedOptions != "" || a.MonthlySavings != 0 {
		t.Errorf("JVM advice = %+v: a 1Gi heap for 450Mi left after GC is fine, so no heap advice", a)
	}
}

func TestJavaFoundByItsMetrics(t *testing.T) {
	heap := map[metrics.ContainerKey]metrics.HeapUsage{{Pod: "billing-7d9f8c-bcdfg"}: {Used: 700 * mi, AfterGC: 200 * mi}}
	for _, tc := range []struct {
		name       string
		containers []Container
		want       string
	}{
		{"a single container takes series without a container label", []Container{{Name: "app", MemoryLimit: 2048 * mi}}, "app"},
		{"the one Java container of several takes them", []Container{{Name: "proxy"}, {Name: "app", Java: javaWith()}}, "app"},
		{"nobody takes them when it is unclear whose they are", []Container{{Name: "proxy"}, {Name: "app"}}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := Analyze(javaWorkload("billing", tc.containers...), nil, heap, rates, DefaultSettings(true))
			got := ""
			for _, c := range r.Workloads[0].Containers {
				if c.JVM != nil && c.JVM.HeapAfterGC != "" {
					got = c.Name
				}
			}
			if got != tc.want {
				t.Errorf("heap data went to %q, want %q", got, tc.want)
			}
		})
	}
	c := Analyze(javaWorkload("billing", Container{Name: "app", MemoryLimit: 2048 * mi}), nil, heap, rates, DefaultSettings(true)).Workloads[0].Containers[0]
	if c.JVM.DetectedBy != jvm.DetectedByMetrics {
		t.Errorf("detected by %q, want metrics", c.JVM.DetectedBy)
	}
}

func TestNonJavaContainersGetNoJVMAdvice(t *testing.T) {
	r := Analyze(javaWorkload("web", Container{Name: "nginx", MemoryRequest: 256 * mi}), map[metrics.ContainerKey]metrics.Usage{{Pod: "web-7d9f8c-bcdfg", Container: "nginx"}: usage("10m", "50Mi")}, nil, rates, DefaultSettings(true))
	if c := r.Workloads[0].Containers[0]; c.JVM != nil || r.JavaMonthlySavings != 0 {
		t.Errorf("container = %+v, report Java savings = %v", c, r.JavaMonthlySavings)
	}
}

func TestPressureHoldsReductions(t *testing.T) {
	workloads := []Workload{{Kind: "Deployment", Name: "api", Replicas: 2, Containers: []Container{{
		Name: "api", CPURequest: 2, MemoryRequest: 4096 * mi, HoldCPU: "throttled in 30% of CPU periods at busy times", HoldMemory: "OOM-killed",
	}}}}
	demand := map[metrics.ContainerKey]metrics.Usage{{Pod: "api-7d9f8c-bcdfg", Container: "api"}: usage("100m", "300Mi")}
	r := Analyze(workloads, demand, nil, rates, DefaultSettings(true))
	c := r.Workloads[0].Containers[0]
	if c.CPU.Action != ActionKeep || c.CPU.Held != "throttled in 30% of CPU periods at busy times" || c.CPU.Recommended != "" {
		t.Errorf("CPU advice = %+v, want the reduction held back by the throttling", c.CPU)
	}
	if c.Memory.Action != ActionKeep || c.Memory.Held != "OOM-killed" || r.MonthlySavings != 0 {
		t.Errorf("memory advice = %+v, savings %v; a held reduction saves nothing", c.Memory, r.MonthlySavings)
	}
	workloads[0].Containers[0].MemoryRequest = 100 * mi
	if m := Analyze(workloads, demand, nil, rates, DefaultSettings(true)).Workloads[0].Containers[0].Memory; m.Action != ActionIncrease || m.Held != "" {
		t.Errorf("memory advice = %+v: pressure holds reductions, never increases", m)
	}
}
