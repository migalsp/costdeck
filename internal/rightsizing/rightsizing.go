// Package rightsizing turns observed container demand into resource request advice. It
// only advises: nothing in this package changes a workload.
package rightsizing

import (
	"cmp"
	"math"
	"regexp"
	"slices"

	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/migalsp/costdeck-operator/internal/jvm"
	"github.com/migalsp/costdeck-operator/internal/metrics"
	"github.com/migalsp/costdeck-operator/internal/pricing"
)

// Actions recommended for one resource of one container.
const (
	// ActionReduce: the request is well above what the container uses.
	ActionReduce = "reduce"
	// ActionIncrease: the container uses more than it requests, so it competes for
	// capacity it never reserved (CPU throttling, eviction under memory pressure).
	ActionIncrease = "increase"
	// ActionKeep: the request fits the observed demand.
	ActionKeep = "keep"
	// ActionSet: no request is set; the recommendation is a starting point.
	ActionSet = "set"
	// ActionUnknown: no usage was observed for the container.
	ActionUnknown = "unknown"
)

const mebibyte = 1 << 20

// Settings tune how demand becomes a recommendation.
type Settings struct {
	// Headroom multiplies the observed demand. A p95/peak over weeks needs less margin than
	// a single reading, which can easily miss a peak.
	CPUHeadroom    float64
	MemoryHeadroom float64
	// MinCPU (cores) and MinMemory (bytes) floor every recommendation.
	MinCPU    float64
	MinMemory float64
	// ReduceThreshold is the smallest saving, as a fraction of the current request, worth
	// recommending. Smaller trims are noise.
	ReduceThreshold float64
}

// DefaultSettings returns the settings for history-based (p95/peak) or snapshot demand.
func DefaultSettings(historical bool) Settings {
	s := Settings{CPUHeadroom: 1.2, MemoryHeadroom: 1.2, MinCPU: 0.01, MinMemory: 32 * mebibyte, ReduceThreshold: 0.2}
	if !historical {
		// A reading from a quiet moment says little about the working set a process grows
		// into (caches, informers), so the memory floor is higher too.
		s.CPUHeadroom, s.MemoryHeadroom, s.MinMemory = 1.5, 1.5, 64*mebibyte
	}
	return s
}

// Workload is a Deployment or StatefulSet and the requests of its containers.
type Workload struct {
	Kind       string
	Name       string
	Replicas   int32
	Containers []Container
	Policy     *Policy
}

type Policy struct {
	Name           string
	ApplyForbidden bool
	Exclude        bool
	CPU            Bounds
	Memory         Bounds
}

type Bounds struct {
	Min      float64
	Max      float64
	Headroom *float64
}

func (b Bounds) clamp(v float64) float64 {
	if b.Min > 0 {
		v = max(v, b.Min)
	}
	if b.Max > 0 {
		v = min(v, b.Max)
	}
	return v
}

// Container holds the requests of one container: cores and bytes, zero when unset.
type Container struct {
	Name          string
	CPURequest    float64
	MemoryRequest float64
	MemoryLimit   float64
	CPULimit      float64
	Java          *jvm.Runtime
	HoldCPU       string
	HoldMemory    string
}

// Report is the advice for one namespace.
type Report struct {
	Namespace string `json:"namespace"`
	// Source is the metrics source the demand came from.
	Source string `json:"source"`
	// Historical is true for p95/peak demand over Window, false for a single reading.
	Historical bool   `json:"historical"`
	Window     string `json:"window,omitempty"`
	// Basis explains in one sentence what the recommendations rest on.
	Basis string `json:"basis"`
	// Warning carries a problem worth showing next to the advice, such as VictoriaMetrics
	// being unreachable.
	Warning            string           `json:"warning,omitempty"`
	Currency           string           `json:"currency"`
	MonthlySavings     float64          `json:"monthlySavings"`
	JavaMonthlySavings float64          `json:"javaMonthlySavings,omitempty"`
	Workloads          []WorkloadAdvice `json:"workloads"`
}

// WorkloadAdvice is the advice for one workload, per container.
type WorkloadAdvice struct {
	Kind           string            `json:"kind"`
	Name           string            `json:"name"`
	Replicas       int32             `json:"replicas"`
	MonthlySavings float64           `json:"monthlySavings"`
	Policy         string            `json:"policy,omitempty"`
	ApplyForbidden bool              `json:"applyForbidden,omitempty"`
	Containers     []ContainerAdvice `json:"containers"`
}

// ContainerAdvice is the advice for one container.
type ContainerAdvice struct {
	Name   string         `json:"name"`
	CPU    ResourceAdvice `json:"cpu"`
	Memory ResourceAdvice `json:"memory"`
	JVM    *JVMAdvice     `json:"jvm,omitempty"`
}

type JVMAdvice struct {
	DetectedBy         string        `json:"detectedBy"`
	Options            []string      `json:"options,omitempty"`
	Heap               string        `json:"heap,omitempty"`
	HeapSource         string        `json:"heapSource"`
	HeapUsed           string        `json:"heapUsed,omitempty"`
	HeapAfterGC        string        `json:"heapAfterGC,omitempty"`
	NonHeap            string        `json:"nonHeap,omitempty"`
	RecommendedHeap    string        `json:"recommendedHeap,omitempty"`
	RecommendedMemory  string        `json:"recommendedMemory,omitempty"`
	RecommendedOptions string        `json:"recommendedOptions,omitempty"`
	MonthlySavings     float64       `json:"monthlySavings,omitempty"`
	Findings           []jvm.Finding `json:"findings"`
}

// ResourceAdvice compares a request with observed demand. Quantities are Kubernetes
// strings such as "250m" or "512Mi"; Request is empty when none is set.
type ResourceAdvice struct {
	Request     string `json:"request,omitempty"`
	Observed    string `json:"observed,omitempty"`
	Recommended string `json:"recommended,omitempty"`
	Action      string `json:"action"`
	Held        string `json:"held,omitempty"`
}

// Analyze compares each workload's requests with the demand of its pods and prices the
// reductions with the given rates.
func Analyze(workloads []Workload, demand map[metrics.ContainerKey]metrics.Usage, heapDemand map[metrics.ContainerKey]metrics.HeapUsage, rates pricing.Rates, s Settings) Report {
	report := Report{Currency: rates.Currency, Workloads: []WorkloadAdvice{}}
	for _, w := range workloads {
		if w.Policy != nil && w.Policy.Exclude {
			continue
		}
		peaks := peakDemand(w, demand)
		heapPeaks := peakHeapDemand(w, heapDemand)
		advice := WorkloadAdvice{Kind: w.Kind, Name: w.Name, Replicas: w.Replicas}
		policy := Policy{}
		if w.Policy != nil {
			policy = *w.Policy
			advice.Policy, advice.ApplyForbidden = policy.Name, policy.ApplyForbidden
		}
		cpuHeadroom, memHeadroom := s.CPUHeadroom, s.MemoryHeadroom
		if policy.CPU.Headroom != nil {
			cpuHeadroom = *policy.CPU.Headroom
		}
		if policy.Memory.Headroom != nil {
			memHeadroom = *policy.Memory.Headroom
		}
		for _, c := range w.Containers {
			observed, seen := peaks[c.Name]
			cpuRec := roundCPU(policy.CPU.clamp(max(observed.cpu*cpuHeadroom, s.MinCPU)))
			memRec := roundMemory(max(observed.mem*memHeadroom, s.MinMemory))
			var javaAdvice *JVMAdvice
			var memoryFloor float64
			if java, heapUse, ok := javaRuntime(w, c, heapPeaks); ok {
				javaAdvice, memoryFloor = adviseJVM(java, c, heapUse, rates, w.Replicas)
				memRec = max(memRec, memoryFloor)
				report.JavaMonthlySavings += javaAdvice.MonthlySavings
			}
			memRec = roundMemory(policy.Memory.clamp(memRec))
			memoryFloor = min(memoryFloor, memRec)
			ca := ContainerAdvice{
				Name:   c.Name,
				CPU:    advise(c.CPURequest, observed.cpu, cpuRec, seen, s.ReduceThreshold, formatCPU),
				Memory: advise(c.MemoryRequest, observed.mem, memRec, seen, s.ReduceThreshold, formatMemory),
				JVM:    javaAdvice,
			}
			if memoryFloor > c.MemoryRequest {
				ca.Memory.Action, ca.Memory.Recommended = ActionIncrease, formatMemory(memRec)
				if c.MemoryRequest <= 0 {
					ca.Memory.Action = ActionSet
				}
			}
			holdReduction(&ca.CPU, c.HoldCPU)
			holdReduction(&ca.Memory, c.HoldMemory)
			hourly := 0.0
			if ca.CPU.Action == ActionReduce {
				hourly += (c.CPURequest - cpuRec) * rates.CPUCoreHour
			}
			if ca.Memory.Action == ActionReduce {
				hourly += (c.MemoryRequest - memRec) / (1 << 30) * rates.MemoryGBHour
			}
			advice.MonthlySavings += hourly * float64(w.Replicas) * pricing.HoursPerMonth
			advice.Containers = append(advice.Containers, ca)
		}
		report.MonthlySavings += advice.MonthlySavings
		report.Workloads = append(report.Workloads, advice)
	}
	slices.SortStableFunc(report.Workloads, func(a, b WorkloadAdvice) int {
		return cmp.Or(cmp.Compare(b.MonthlySavings, a.MonthlySavings), cmp.Compare(a.Name, b.Name))
	})
	return report
}

func advise(request, observed, recommended float64, seen bool, threshold float64, format func(float64) string) ResourceAdvice {
	a := ResourceAdvice{Action: ActionUnknown}
	if request > 0 {
		a.Request = format(request)
	}
	if !seen {
		return a
	}
	a.Observed = format(observed)
	a.Recommended = format(recommended)
	switch {
	case request <= 0:
		a.Action = ActionSet
	case observed > request:
		a.Action = ActionIncrease
	case recommended <= request*(1-threshold):
		a.Action = ActionReduce
	default:
		a.Action = ActionKeep
		a.Recommended = ""
	}
	return a
}

func holdReduction(a *ResourceAdvice, reason string) {
	if reason == "" || a.Action != ActionReduce {
		return
	}
	a.Action, a.Recommended, a.Held = ActionKeep, "", reason
}

func OwnsPod(kind, name, pod string) bool { return podPattern(kind, name).MatchString(pod) }

type demandPair struct{ cpu, mem float64 }

// peakDemand returns, per container name, the highest demand across the workload's pods.
// Pods are matched by name because history outlives them: a pod from last week's rollout
// no longer exists to ask for its owner.
func peakDemand(w Workload, demand map[metrics.ContainerKey]metrics.Usage) map[string]demandPair {
	pattern := podPattern(w.Kind, w.Name)
	out := map[string]demandPair{}
	for key, u := range demand {
		if !pattern.MatchString(key.Pod) {
			continue
		}
		p := out[key.Container]
		p.cpu = max(p.cpu, u.CPU.AsApproximateFloat64())
		p.mem = max(p.mem, u.Memory.AsApproximateFloat64())
		out[key.Container] = p
	}
	return out
}

func peakHeapDemand(w Workload, heapDemand map[metrics.ContainerKey]metrics.HeapUsage) map[string]jvm.Usage {
	pattern := podPattern(w.Kind, w.Name)
	out := map[string]jvm.Usage{}
	for key, u := range heapDemand {
		if !pattern.MatchString(key.Pod) {
			continue
		}
		p := out[key.Container]
		p.HeapUsed = max(p.HeapUsed, u.Used)
		p.HeapAfterGC = max(p.HeapAfterGC, u.AfterGC)
		p.NonHeap = max(p.NonHeap, u.NonHeap)
		out[key.Container] = p
	}
	return out
}

func javaRuntime(w Workload, c Container, heapPeaks map[string]jvm.Usage) (jvm.Runtime, jvm.Usage, bool) {
	heapUse, measured := heapPeaks[c.Name]
	if unlabelled, ok := heapPeaks[""]; ok && !measured && ownsUnlabelledSeries(w, c) {
		heapUse, measured = unlabelled, true
	}
	switch {
	case c.Java != nil:
		return *c.Java, heapUse, true
	case measured:
		return jvm.Runtime{DetectedBy: jvm.DetectedByMetrics}, heapUse, true
	}
	return jvm.Runtime{}, jvm.Usage{}, false
}

func ownsUnlabelledSeries(w Workload, c Container) bool {
	javaContainers := 0
	for _, other := range w.Containers {
		if other.Java != nil {
			javaContainers++
		}
	}
	if javaContainers == 0 {
		return len(w.Containers) == 1
	}
	return c.Java != nil && javaContainers == 1
}

func adviseJVM(java jvm.Runtime, c Container, heapUse jvm.Usage, rates pricing.Rates, replicas int32) (*JVMAdvice, float64) {
	resources := jvm.Resources{MemoryRequest: c.MemoryRequest, MemoryLimit: c.MemoryLimit, CPULimit: c.CPULimit}
	heap := java.Options.HeapFor(c.MemoryLimit)
	a := &JVMAdvice{
		DetectedBy: java.DetectedBy,
		Options:    java.Options.Flags,
		HeapSource: heap.Source,
		Findings:   jvm.Audit(java.Options, heap, resources, heapUse),
	}
	if a.Findings == nil {
		a.Findings = []jvm.Finding{}
	}
	var memoryFloor float64
	if heap.Known {
		a.Heap = formatMemory(heap.Max)
		memoryFloor = roundMemory(heap.Max + jvm.NativeMemory(heap.Max, heapUse.NonHeap))
	}
	if heapUse.HeapUsed > 0 {
		a.HeapUsed = formatMemory(heapUse.HeapUsed)
	}
	if heapUse.HeapAfterGC > 0 {
		a.HeapAfterGC = formatMemory(heapUse.HeapAfterGC)
	}
	if heapUse.NonHeap > 0 {
		a.NonHeap = formatMemory(heapUse.NonHeap)
	}
	oversized := slices.ContainsFunc(a.Findings, func(f jvm.Finding) bool { return f.Code == jvm.CodeHeapOversized })
	tight := slices.ContainsFunc(a.Findings, func(f jvm.Finding) bool { return f.Code == jvm.CodeHeapTight })
	if plan, ok := jvm.PlanFor(java.Options, heapUse); ok && (oversized || tight || !heap.Known) {
		a.RecommendedHeap, a.RecommendedMemory, a.RecommendedOptions = formatMemory(plan.Heap), formatMemory(plan.Memory), plan.Options
		if oversized && c.MemoryRequest > plan.Memory {
			a.MonthlySavings = (c.MemoryRequest - plan.Memory) / (1 << 30) * rates.MemoryGBHour * float64(replicas) * pricing.HoursPerMonth
		}
	}
	return a, memoryFloor
}

// podPattern matches the pods a workload creates: <deployment>-<replicaset hash>-<suffix>
// or <statefulset>-<ordinal>. Kubernetes writes the hash and the five-character suffix in
// an alphabet without vowels, so "api" claims neither the pods of "api-gateway" nor those
// of the StatefulSet "api-postgresql" or the Job "api-migrate".
func podPattern(kind, name string) *regexp.Regexp {
	if kind == "StatefulSet" {
		return regexp.MustCompile("^" + regexp.QuoteMeta(name) + `-[0-9]+$`)
	}
	return regexp.MustCompile("^" + regexp.QuoteMeta(name) + `-` + safeAlphabet + `{1,10}-` + safeAlphabet + `{5}$`)
}

const safeAlphabet = `[bcdfghjklmnpqrstvwxz2456789]`

// roundingSlack keeps float noise (0.1*1.5 = 0.15000000000000002) from rounding a value
// that is already on a step up to the next one.
const roundingSlack = 1e-9

// roundCPU rounds up to 5 millicores.
func roundCPU(cores float64) float64 { return math.Ceil(cores*1000/5-roundingSlack) * 5 / 1000 }

// roundMemory rounds up to a whole mebibyte.
func roundMemory(bytes float64) float64 { return math.Ceil(bytes/mebibyte-roundingSlack) * mebibyte }

func formatCPU(cores float64) string {
	return resource.NewMilliQuantity(int64(math.Round(cores*1000)), resource.DecimalSI).String()
}

func formatMemory(bytes float64) string {
	if bytes >= mebibyte {
		bytes = math.Round(bytes/mebibyte) * mebibyte
	}
	return resource.NewQuantity(int64(bytes), resource.BinarySI).String()
}
