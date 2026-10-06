// Package rightsizing turns observed container demand into resource request advice. It
// only advises: nothing in this package changes a workload.
package rightsizing

import (
	"cmp"
	"math"
	"regexp"
	"slices"

	"k8s.io/apimachinery/pkg/api/resource"

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
}

// Container holds the requests of one container: cores and bytes, zero when unset.
type Container struct {
	Name          string
	CPURequest    float64
	MemoryRequest float64
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
	Warning        string           `json:"warning,omitempty"`
	Currency       string           `json:"currency"`
	MonthlySavings float64          `json:"monthlySavings"`
	Workloads      []WorkloadAdvice `json:"workloads"`
}

// WorkloadAdvice is the advice for one workload, per container.
type WorkloadAdvice struct {
	Kind           string            `json:"kind"`
	Name           string            `json:"name"`
	Replicas       int32             `json:"replicas"`
	MonthlySavings float64           `json:"monthlySavings"`
	Containers     []ContainerAdvice `json:"containers"`
}

// ContainerAdvice is the advice for one container.
type ContainerAdvice struct {
	Name   string         `json:"name"`
	CPU    ResourceAdvice `json:"cpu"`
	Memory ResourceAdvice `json:"memory"`
}

// ResourceAdvice compares a request with observed demand. Quantities are Kubernetes
// strings such as "250m" or "512Mi"; Request is empty when none is set.
type ResourceAdvice struct {
	Request     string `json:"request,omitempty"`
	Observed    string `json:"observed,omitempty"`
	Recommended string `json:"recommended,omitempty"`
	Action      string `json:"action"`
}

// Analyze compares each workload's requests with the demand of its pods and prices the
// reductions with the given rates.
func Analyze(workloads []Workload, demand map[metrics.ContainerKey]metrics.Usage, rates pricing.Rates, s Settings) Report {
	report := Report{Currency: rates.Currency, Workloads: []WorkloadAdvice{}}
	for _, w := range workloads {
		peaks := peakDemand(w, demand)
		advice := WorkloadAdvice{Kind: w.Kind, Name: w.Name, Replicas: w.Replicas}
		for _, c := range w.Containers {
			observed, seen := peaks[c.Name]
			cpuRec := roundCPU(max(observed.cpu*s.CPUHeadroom, s.MinCPU))
			memRec := roundMemory(max(observed.mem*s.MemoryHeadroom, s.MinMemory))
			ca := ContainerAdvice{
				Name:   c.Name,
				CPU:    advise(c.CPURequest, observed.cpu, cpuRec, seen, s.ReduceThreshold, formatCPU),
				Memory: advise(c.MemoryRequest, observed.mem, memRec, seen, s.ReduceThreshold, formatMemory),
			}
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

// podPattern matches the pods a workload creates: <deployment>-<replicaset hash>-<suffix>
// or <statefulset>-<ordinal>. Hash segments never contain dashes, so "api" does not
// claim the pods of "api-gateway".
func podPattern(kind, name string) *regexp.Regexp {
	if kind == "StatefulSet" {
		return regexp.MustCompile("^" + regexp.QuoteMeta(name) + `-[0-9]+$`)
	}
	return regexp.MustCompile("^" + regexp.QuoteMeta(name) + `-[a-z0-9]+-[a-z0-9]+$`)
}

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
