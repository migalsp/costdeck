package risks

import (
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/migalsp/costdeck-operator/internal/jvm"
	"github.com/migalsp/costdeck-operator/internal/pricing"
)

var rates = pricing.Rates{CPUCoreHour: 0.04, MemoryGBHour: 0.005, Currency: "USD"}

var shop = Context{Namespace: "shop", Window: "14d", Rates: rates}

func api(c Container) Workload {
	return Workload{Kind: "Deployment", Name: "api", Replicas: 2, Containers: []Container{c}}
}

func codes(risks []Risk) []string {
	out := make([]string, 0, len(risks))
	for _, r := range risks {
		out = append(out, r.Code)
	}
	return out
}

func monthly(gib float64, replicas float64) float64 {
	return gib * rates.MemoryGBHour * replicas * pricing.HoursPerMonth
}

func TestAssess(t *testing.T) {
	base := Container{Name: "main", CPURequest: 0.5, CPULimit: 1, MemoryRequest: 512 * mebibyte, MemoryLimit: gibibyte, PeakMemory: 400 * mebibyte}
	with := func(change func(*Container)) Container {
		c := base
		change(&c)
		return c
	}
	for _, tc := range []struct {
		name     string
		workload Workload
		want     []string
		severity string
		cost     *float64
	}{
		{"a calm workload", api(base), nil, "", nil},
		{"throttled now and then", api(with(func(c *Container) { c.Pressure.ThrottledShare = 0.12 })), []string{CodeCPUThrottled}, SeverityWarning, new(0.0)},
		{"throttled a quarter of the time", api(with(func(c *Container) { c.Pressure.ThrottledShare = 0.3 })), []string{CodeCPUThrottled}, SeverityCritical, new(0.0)},
		{"throttling without a limit is not reported", api(with(func(c *Container) { c.Pressure.ThrottledShare = 0.3; c.CPULimit = 0 })), nil, "", nil},
		{"OOM-killed", api(with(func(c *Container) {
			c.Pressure.OOMKills = 2
			c.Pressure.Restarts = 2
			c.Pressure.LastReason = "OOMKilled"
		})),
			[]string{CodeOOMKilled}, SeverityCritical, new(monthly(1, 2))},
		{"close to the memory limit", api(with(func(c *Container) { c.PeakMemory = 950 * mebibyte })), []string{CodeMemoryNearLimit}, SeverityWarning, new(0.0)},
		{"restarting with errors", api(with(func(c *Container) {
			c.Pressure.Restarts = 5
			c.Pressure.LastReason = "Error"
			c.Pressure.LastExitCode = 1
		})),
			[]string{CodeRestarts}, SeverityWarning, nil},
		{"crash-looping", api(with(func(c *Container) {
			c.Pressure.CrashLooping = true
			c.Pressure.Restarts = 1
			c.Pressure.LastReason = "Error"
		})),
			[]string{CodeRestarts}, SeverityCritical, nil},
		{"a JVM heap still full after GC", api(with(func(c *Container) {
			c.JVMFindings = []jvm.Finding{{Severity: jvm.SeverityRisk, Code: jvm.CodeHeapTight, Message: "full"}, {Severity: jvm.SeverityNote, Code: "serial-gc", Message: "serial"}}
			c.JVMRecommendedMemory = 1536 * mebibyte
		})), []string{jvm.CodeHeapTight}, SeverityCritical, new(monthly(1, 2))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Assess(tc.workload, shop)
			if !slices.Equal(codes(got), tc.want) {
				t.Fatalf("risks = %q, want %q", codes(got), tc.want)
			}
			if len(got) == 0 {
				return
			}
			r := got[0]
			if r.Severity != tc.severity || r.Workload != "api" || r.Kind != "Deployment" || r.Message == "" || strings.ContainsRune(r.Message+r.Fix, '\u2014') {
				t.Errorf("risk = %+v", r)
			}
			if (r.FixMonthlyCost == nil) != (tc.cost == nil) || (tc.cost != nil && math.Abs(*r.FixMonthlyCost-*tc.cost) > 1e-9) {
				t.Errorf("cost of the fix = %v, want %v", r.FixMonthlyCost, tc.cost)
			}
		})
	}
}

func TestAssessWorkloadRisks(t *testing.T) {
	w := Workload{Kind: "Deployment", Name: "api", Replicas: 3, Evictions: 2,
		EvictionMessage: "The node was low on resource: memory. Container main was using 900Mi, which exceeds its request of 256Mi.",
		HPA:             &HPA{Name: "api", Max: 4, Wanted: 7, Limited: true},
		Containers:      []Container{{Name: "main", CPURequest: 0.5, MemoryRequest: 256 * mebibyte, PeakMemory: 900 * mebibyte}}}
	got := Assess(w, shop)
	if !slices.Equal(codes(got), []string{CodeEvicted, CodeHPAAtMax}) {
		t.Fatalf("risks = %q", codes(got))
	}
	evicted := got[0]
	if !strings.Contains(evicted.Message, "2 pods were evicted in the last 14d") || !strings.Contains(evicted.Message, "exceeds its request of 256Mi") || !strings.Contains(evicted.Fix, "main to 1080Mi") {
		t.Errorf("eviction = %+v", evicted)
	}
	if want := monthly((1080.0-256)/1024, 3); math.Abs(*evicted.FixMonthlyCost-want) > 1e-9 {
		t.Errorf("eviction fix costs %v, want %v", *evicted.FixMonthlyCost, want)
	}
	hpa := got[1]
	pod := (0.5*rates.CPUCoreHour + 0.25*rates.MemoryGBHour) * pricing.HoursPerMonth
	if !strings.Contains(hpa.Fix, "to 7") || math.Abs(*hpa.FixMonthlyCost-3*pod) > 1e-9 {
		t.Errorf("HPA risk = %+v, want three more pods at %v", hpa, pod)
	}
	w.HPA.Wanted = 0
	if r := Assess(w, shop)[1]; !strings.Contains(r.Message, "wants more replicas than its maximum of 4") || !strings.Contains(r.Fix, "above 4") || math.Abs(*r.FixMonthlyCost-pod) > 1e-9 {
		t.Errorf("an HPA whose wish is unknown = %+v, want the cost of one more replica", r)
	}
	w.HPA.Limited = false
	if slices.Contains(codes(Assess(w, shop)), CodeHPAAtMax) {
		t.Error("an HPA that does not ask for more replicas than its maximum is no risk")
	}
	w.EvictionMessage = "The node was low on resource: ephemeral-storage."
	if r := Assess(w, shop)[0]; !strings.Contains(r.Fix, "ephemeral-storage request") || r.FixMonthlyCost != nil {
		t.Errorf("an eviction for local disk asks for an ephemeral-storage request, got %+v", r)
	}
}

func TestSortPutsCriticalRisksFirst(t *testing.T) {
	risks := []Risk{
		{Workload: "a", Severity: SeverityWarning, Code: CodeHPAAtMax},
		{Workload: "b", Severity: SeverityCritical, Code: CodeOOMKilled},
		{Workload: "a", Severity: SeverityCritical, Code: CodeEvicted},
	}
	Sort(risks)
	if risks[0].Workload != "a" || risks[0].Code != CodeEvicted || risks[1].Workload != "b" || risks[2].Severity != SeverityWarning {
		t.Errorf("order = %+v", risks)
	}
}

func TestHoldReductions(t *testing.T) {
	for _, tc := range []struct {
		name             string
		pressure         Pressure
		evictions        int
		peak, limit      float64
		cpuHeld, memHeld string
	}{
		{"nothing to hold", Pressure{ThrottledShare: 0.05}, 0, 100, 1000, "", ""},
		{"throttling holds CPU", Pressure{ThrottledShare: 0.2}, 0, 100, 1000, "throttled in 20% of CPU periods at busy times", ""},
		{"an OOM kill holds memory", Pressure{OOMKills: 1}, 0, 100, 1000, "", "OOM-killed"},
		{"evictions hold memory", Pressure{}, 1, 100, 1000, "", "its pods were evicted"},
		{"the limit close by holds memory", Pressure{}, 0, 950, 1000, "", "at 95% of its memory limit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cpuHeld, memHeld := HoldReductions(tc.pressure, tc.evictions, tc.peak, tc.limit)
			if cpuHeld != tc.cpuHeld || memHeld != tc.memHeld {
				t.Errorf("held = %q, %q; want %q, %q", cpuHeld, memHeld, tc.cpuHeld, tc.memHeld)
			}
		})
	}
}

func TestAssessWakeUp(t *testing.T) {
	slow := Wake{Kind: "ScalingGroup", Name: "dev", Slowest: 7*time.Minute + 20*time.Second, Count: 5, SuggestedLead: 9 * time.Minute, StartsPerMonth: 22, HourlyCost: 0.5}
	got := AssessWakeUp(slow)
	if len(got) != 1 || got[0].Code != CodeSlowWakeUp || got[0].Severity != SeverityWarning || got[0].Workload != "dev" {
		t.Fatalf("risks = %+v", got)
	}
	r := got[0]
	if !strings.Contains(r.Message, "last 5 wake-ups took up to 8m") || !strings.Contains(r.Fix, "about 9m earlier") || math.Abs(*r.FixMonthlyCost-0.15*0.5*22) > 1e-9 {
		t.Errorf("risk = %+v, cost %v", r, *r.FixMonthlyCost)
	}
	short := slow
	short.Lead = 5 * time.Minute
	if r := AssessWakeUp(short)[0]; !strings.Contains(r.Fix, "from 5m to about 9m") || math.Abs(*r.FixMonthlyCost-(4.0/60)*0.5*22) > 1e-9 {
		t.Errorf("a prewarm that is too short = %+v", r)
	}
	for name, w := range map[string]Wake{
		"quick wake-ups":          {Slowest: 90 * time.Second, Count: 3, StartsPerMonth: 22},
		"prewarm long enough":     {Slowest: 7 * time.Minute, Count: 3, Lead: 9 * time.Minute, StartsPerMonth: 22},
		"never woken by schedule": {Slowest: 7 * time.Minute, Count: 3},
		"nothing measured":        {StartsPerMonth: 22},
	} {
		if got := AssessWakeUp(w); got != nil {
			t.Errorf("%s: risks = %+v", name, got)
		}
	}
}
