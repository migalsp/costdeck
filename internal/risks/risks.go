package risks

import (
	"cmp"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/migalsp/costdeck-operator/internal/jvm"
	"github.com/migalsp/costdeck-operator/internal/pricing"
)

const (
	SeverityCritical = "critical"
	SeverityWarning  = "warning"
)

const ReasonOOMKilled = "OOMKilled"

const (
	CodeCPUThrottled    = "cpu-throttled"
	CodeOOMKilled       = "oom-killed"
	CodeRestarts        = "restarts"
	CodeEvicted         = "evicted"
	CodeMemoryNearLimit = "memory-near-limit"
	CodeHPAAtMax        = "hpa-at-max"
	CodeSlowWakeUp      = "slow-wake-up"
)

const slowWakeUp = 2 * time.Minute

// Wake describes the recent wake-ups of a ScalingGroup or ScalingConfig for AssessWakeUp: the
// slowest of status.recentWakeUps and how many there are, the prewarm lead in effect and the one
// spec.prewarm.auto would use, how often its schedule starts a month and what its workloads cost
// an hour while up.
type Wake struct {
	Kind           string
	Name           string
	Slowest        time.Duration
	Count          int
	Lead           time.Duration
	SuggestedLead  time.Duration
	StartsPerMonth float64
	HourlyCost     float64
}

const (
	mebibyte          = 1 << 20
	gibibyte          = 1 << 30
	throttledCritical = 0.25
	throttledWarning  = 0.10
	nearLimitShare    = 0.9
	restartsWarning   = 3
	oomLimitGrowth    = 1.5
	oomPeakHeadroom   = 1.3
	evictionHeadroom  = 1.2
	nearLimitHeadroom = 1.25
	throttledLimitX   = 2
)

var criticalJVMFindings = []string{jvm.CodeHeapTight, jvm.CodeHeapOverLimit}

// Pressure is what a container has been through: the share of CPU periods it was throttled in,
// OOM kills counted per pod, restarts, the reason and exit code of its last termination and
// whether it is crash-looping now.
type Pressure struct {
	ThrottledShare float64
	OOMKills       int
	Restarts       int
	LastReason     string
	LastExitCode   int32
	CrashLooping   bool
}

// Container is one container to assess: its requests and limits in cores and bytes, its peak
// memory, its Pressure and, for a JVM, the findings of jvm.Audit and the memory right-sizing
// recommends for it.
type Container struct {
	Name                 string
	CPURequest           float64
	CPULimit             float64
	MemoryRequest        float64
	MemoryLimit          float64
	PeakMemory           float64
	Pressure             Pressure
	JVMFindings          []jvm.Finding
	JVMRecommendedMemory float64
}

// HPA is the HorizontalPodAutoscaler of a workload: its maxReplicas, the replicas its resource
// metrics ask for (zero when they cannot tell), and Limited when its ScalingLimited condition says
// TooManyReplicas.
type HPA struct {
	Name    string
	Max     int32
	Wanted  int32
	Limited bool
}

// Workload is one workload to assess, with its containers, how many of its pods were evicted and
// the kubelet's message about an eviction, and its HPA, if it has one.
type Workload struct {
	Kind            string
	Name            string
	Replicas        int32
	Containers      []Container
	Evictions       int
	EvictionMessage string
	HPA             *HPA
}

// Risk is one performance risk as the dashboard and the risk alerts show it. FixMonthlyCost is
// what the fix adds to the monthly bill: zero when it only raises limits, which are not billed,
// and nil when the cost is unknown.
type Risk struct {
	Kind           string   `json:"kind"`
	Workload       string   `json:"workload"`
	Container      string   `json:"container,omitempty"`
	Severity       string   `json:"severity"`
	Code           string   `json:"code"`
	Message        string   `json:"message"`
	Fix            string   `json:"fix,omitempty"`
	FixMonthlyCost *float64 `json:"fixMonthlyCost,omitempty"`
}

// Report is the performance risk report of one namespace. Historical and Window say whether
// VictoriaMetrics history over that window went into it, Basis tells the reader what it rests on,
// and Warning carries the first data source that could not be read.
type Report struct {
	Namespace  string `json:"namespace"`
	Historical bool   `json:"historical"`
	Window     string `json:"window,omitempty"`
	Basis      string `json:"basis"`
	Warning    string `json:"warning,omitempty"`
	Currency   string `json:"currency"`
	Risks      []Risk `json:"risks"`
}

// Context carries what Assess needs besides the workload: the namespace for the kubectl hint, the
// history window named in messages (empty when only the current state is known) and the rates
// fix costs are priced at.
type Context struct {
	Namespace string
	Window    string
	Rates     pricing.Rates
}

// Assess returns the performance risks of one workload: CPU throttling under a limit, OOM kills
// or else memory near the limit, crash loops and restarts that an OOM kill does not explain, JVM
// heap risks, evictions and an HPA capped at its maximum. Fix costs are monthly at ctx.Rates.
func Assess(w Workload, ctx Context) []Risk {
	var out []Risk
	add := func(container, severity, code, message, fix string, cost *float64) {
		out = append(out, Risk{Kind: w.Kind, Workload: w.Name, Container: container, Severity: severity, Code: code, Message: message, Fix: fix, FixMonthlyCost: cost})
	}
	replicas := float64(max(w.Replicas, 1))
	during := ""
	if ctx.Window != "" {
		during = " in the last " + ctx.Window
	}
	for _, c := range w.Containers {
		p := c.Pressure
		if p.ThrottledShare >= throttledWarning && c.CPULimit > 0 {
			severity := SeverityWarning
			if p.ThrottledShare >= throttledCritical {
				severity = SeverityCritical
			}
			add(c.Name, severity, CodeCPUThrottled,
				fmt.Sprintf("Throttled in %d%% of CPU periods at busy times%s: the %s CPU limit holds it back, so its work takes longer.",
					percent(p.ThrottledShare), during, cpu(c.CPULimit)),
				fmt.Sprintf("Raise the CPU limit to %s or remove it. Limits are not billed, so this costs nothing extra.", cpu(c.CPULimit*throttledLimitX)),
				zero())
		}
		switch {
		case p.OOMKills > 0:
			limit := roundMemory(max(c.MemoryLimit*oomLimitGrowth, c.PeakMemory*oomPeakHeadroom))
			add(c.Name, SeverityCritical, CodeOOMKilled,
				fmt.Sprintf("OOM-killed%s%s: every kill restarts the container and drops the work it was doing.", times(p.OOMKills), oomLimitPhrase(c.MemoryLimit)),
				fmt.Sprintf("Raise the memory limit to %s and the request with it.", memory(limit)),
				memoryCost(c.MemoryRequest, limit, replicas, ctx.Rates))
		case c.MemoryLimit > 0 && c.PeakMemory >= c.MemoryLimit*nearLimitShare:
			add(c.Name, SeverityWarning, CodeMemoryNearLimit,
				fmt.Sprintf("Peak memory%s was %s, %d%% of the %s limit: one larger spike and the container is OOM-killed.",
					during, memory(c.PeakMemory), percent(c.PeakMemory/c.MemoryLimit), memory(c.MemoryLimit)),
				fmt.Sprintf("Raise the memory limit to %s. Limits are not billed, so this costs nothing extra.", memory(roundMemory(c.PeakMemory*nearLimitHeadroom))),
				zero())
		}
		if p.CrashLooping || (p.Restarts >= restartsWarning && p.LastReason != ReasonOOMKilled) {
			severity := SeverityWarning
			if p.CrashLooping {
				severity = SeverityCritical
			}
			add(c.Name, severity, CodeRestarts, restartMessage(p, during),
				fmt.Sprintf("Read the log of the previous run: kubectl logs -n %s %s/%s -c %s --previous. A failing liveness probe or the application "+
					"itself is a likelier cause than its resources.", ctx.Namespace, strings.ToLower(w.Kind), w.Name, c.Name),
				nil)
		}
		for _, f := range c.JVMFindings {
			if f.Severity != jvm.SeverityRisk {
				continue
			}
			severity := SeverityWarning
			if slices.Contains(criticalJVMFindings, f.Code) {
				severity = SeverityCritical
			}
			var cost *float64
			if f.Code == jvm.CodeHeapTight && c.JVMRecommendedMemory > 0 {
				cost = memoryCost(c.MemoryRequest, c.JVMRecommendedMemory, replicas, ctx.Rates)
			}
			add(c.Name, severity, f.Code, f.Message, "", cost)
		}
	}
	if w.Evictions > 0 {
		add("", SeverityCritical, CodeEvicted, evictionMessage(w, during), evictionFix(w), evictionCost(w, ctx.Rates))
	}
	if h := w.HPA; h != nil && h.Limited {
		pod := podMonthly(w.Containers, ctx.Rates)
		if h.Wanted > h.Max {
			cost := float64(h.Wanted-h.Max) * pod
			add("", SeverityWarning, CodeHPAAtMax,
				fmt.Sprintf("The HorizontalPodAutoscaler %s wants %d replicas but stops at its maximum of %d, so load beyond that slows every replica down.", h.Name, h.Wanted, h.Max),
				fmt.Sprintf("Raise maxReplicas of %s to %d.", h.Name, h.Wanted), &cost)
		} else {
			add("", SeverityWarning, CodeHPAAtMax,
				fmt.Sprintf("The HorizontalPodAutoscaler %s wants more replicas than its maximum of %d, so load beyond that slows every replica down.", h.Name, h.Max),
				fmt.Sprintf("Raise maxReplicas of %s above %d; each replica adds %s a month.", h.Name, h.Max, money(pod, ctx.Rates.Currency)), &pod)
		}
	}
	return out
}

// AssessWakeUp warns when the slowest recent wake-up took two minutes or more and the prewarm
// lead does not cover it. The fix costs the extra lead time the workloads run early each month;
// it returns nil when there is nothing to warn about or the schedule never starts.
func AssessWakeUp(w Wake) []Risk {
	if w.Count == 0 || w.Slowest < slowWakeUp || w.Lead >= w.Slowest || w.StartsPerMonth <= 0 {
		return nil
	}
	cost := max(w.SuggestedLead-w.Lead, 0).Hours() * w.HourlyCost * w.StartsPerMonth
	fix := fmt.Sprintf("Start it ahead of its schedule by how long it takes to wake up (spec.prewarm.auto), about %s earlier.", minutes(w.SuggestedLead))
	if w.Lead > 0 {
		fix = fmt.Sprintf("Raise the prewarm lead from %s to about %s, or let it follow the wake-ups (spec.prewarm.auto).", minutes(w.Lead), minutes(w.SuggestedLead))
	}
	return []Risk{{
		Kind: w.Kind, Workload: w.Name, Severity: SeverityWarning, Code: CodeSlowWakeUp,
		Message: fmt.Sprintf("The last %s took up to %s until every workload was ready, so whoever opens it when its schedule starts waits that long.",
			wakeUps(w.Count), minutes(w.Slowest)),
		Fix: fix, FixMonthlyCost: &cost,
	}}
}

// wakeUps words a count of wake-ups for a message: "wake-up" for one, "3 wake-ups" otherwise.
func wakeUps(n int) string {
	if n == 1 {
		return "wake-up"
	}
	return fmt.Sprintf("%d wake-ups", n)
}

// minutes formats a duration as seconds under a minute and as whole minutes, rounded up, above.
func minutes(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	return fmt.Sprintf("%dm", int(math.Ceil(d.Minutes())))
}

// Sort orders risks critical first, then by workload, container and code, so the report keeps
// the same order between requests.
func Sort(risks []Risk) {
	rank := map[string]int{SeverityCritical: 0, SeverityWarning: 1}
	slices.SortStableFunc(risks, func(a, b Risk) int {
		return cmp.Or(cmp.Compare(rank[a.Severity], rank[b.Severity]), cmp.Compare(a.Workload, b.Workload), cmp.Compare(a.Container, b.Container), cmp.Compare(a.Code, b.Code))
	})
}

// HoldReductions returns why right-sizing must not cut a container's CPU or memory request, as
// phrases that complete a sentence; an empty reason allows the cut. CPU is held while throttled in
// 10% of periods or more, memory after an OOM kill, an eviction or a peak at 90% of the limit.
func HoldReductions(p Pressure, evictions int, peakMemory, memoryLimit float64) (cpuReason, memoryReason string) {
	if p.ThrottledShare >= throttledWarning {
		cpuReason = fmt.Sprintf("throttled in %d%% of CPU periods at busy times", percent(p.ThrottledShare))
	}
	switch {
	case p.OOMKills > 0:
		memoryReason = "OOM-killed"
	case evictions > 0:
		memoryReason = "its pods were evicted"
	case memoryLimit > 0 && peakMemory >= memoryLimit*nearLimitShare:
		memoryReason = fmt.Sprintf("at %d%% of its memory limit", percent(peakMemory/memoryLimit))
	}
	return cpuReason, memoryReason
}

// restartMessage describes a crash loop or repeated restarts and how the last run ended.
func restartMessage(p Pressure, during string) string {
	if p.CrashLooping {
		return fmt.Sprintf("Crash-looping: it keeps failing on start and Kubernetes waits longer before each retry. The last run ended with %s.", ending(p))
	}
	return fmt.Sprintf("Restarted %d times%s; the last run ended with %s.", p.Restarts, during, ending(p))
}

// ending names how the last run ended: its termination reason, with the exit code when it is not
// zero, or "an unknown reason" when none was recorded.
func ending(p Pressure) string {
	reason := cmp.Or(p.LastReason, "an unknown reason")
	if p.LastExitCode != 0 {
		return fmt.Sprintf("%s (exit code %d)", reason, p.LastExitCode)
	}
	return reason
}

// evictionMessage explains why pods get evicted and appends the kubelet's own message when there
// is one.
func evictionMessage(w Workload, during string) string {
	message := fmt.Sprintf("%d pod%s evicted%s: under node pressure the kubelet evicts first the pods that use more than they request.",
		w.Evictions, plural(w.Evictions), during)
	if w.EvictionMessage != "" {
		message += " The kubelet said: " + strings.TrimSpace(w.EvictionMessage)
	}
	return message
}

// evictionFix proposes an ephemeral-storage request when the eviction was for local disk, and
// otherwise memory requests raised to peak plus headroom for the containers below that.
func evictionFix(w Workload) string {
	if strings.Contains(w.EvictionMessage, "ephemeral-storage") {
		return "Set an ephemeral-storage request that covers what the pod writes to its local disk."
	}
	var parts []string
	for _, c := range w.Containers {
		if target := evictionRequest(c); target > c.MemoryRequest {
			parts = append(parts, fmt.Sprintf("%s to %s", c.Name, memory(target)))
		}
	}
	if len(parts) == 0 {
		return "Keep the memory request at least at what the containers use at peak."
	}
	return "Raise the memory request of " + strings.Join(parts, ", ") + ", what it uses at peak plus headroom."
}

// evictionCost prices evictionFix's memory request increases per month across all replicas, and
// is nil for an ephemeral-storage eviction, whose cost is unknown.
func evictionCost(w Workload, rates pricing.Rates) *float64 {
	if strings.Contains(w.EvictionMessage, "ephemeral-storage") {
		return nil
	}
	total := 0.0
	for _, c := range w.Containers {
		if cost := memoryCost(c.MemoryRequest, evictionRequest(c), float64(max(w.Replicas, 1)), rates); cost != nil {
			total += *cost
		}
	}
	return &total
}

// evictionRequest is the memory request that covers a container's peak memory with 20% headroom,
// rounded up to a mebibyte.
func evictionRequest(c Container) float64 {
	return roundMemory(c.PeakMemory * evictionHeadroom)
}

// memoryCost prices raising a memory request to target, per month for every replica; a target at
// or below the request costs zero. It never returns nil.
func memoryCost(request, target, replicas float64, rates pricing.Rates) *float64 {
	cost := max(target-request, 0) / gibibyte * rates.MemoryGBHour * replicas * pricing.HoursPerMonth
	return &cost
}

// money formats an amount with two decimals and its currency, USD when none is set.
func money(amount float64, currency string) string {
	return fmt.Sprintf("%.2f %s", amount, cmp.Or(currency, "USD"))
}

// podMonthly prices the CPU and memory requests of one pod for a month.
func podMonthly(containers []Container, rates pricing.Rates) float64 {
	hourly := 0.0
	for _, c := range containers {
		hourly += c.CPURequest*rates.CPUCoreHour + c.MemoryRequest/gibibyte*rates.MemoryGBHour
	}
	return hourly * pricing.HoursPerMonth
}

// oomLimitPhrase returns " at its <limit> limit" for an OOM message, or nothing when the
// container has no memory limit.
func oomLimitPhrase(limit float64) string {
	if limit <= 0 {
		return ""
	}
	return " at its " + memory(limit) + " limit"
}

// times returns " N times" for more than one event and nothing for one, so a single event reads
// plainly.
func times(n int) string {
	if n <= 1 {
		return ""
	}
	return fmt.Sprintf(" %d times", n)
}

// plural returns what follows "pod" in "N pods were evicted": " was" for one, "s were" otherwise.
func plural(n int) string {
	if n == 1 {
		return " was"
	}
	return "s were"
}

// zero returns a zero cost, for fixes that only raise limits, which are not billed.
func zero() *float64 {
	v := 0.0
	return &v
}

// percent turns a share into whole percent, rounded to the nearest.
func percent(share float64) int { return int(math.Round(share * 100)) }

// roundMemory rounds a size in bytes up to a whole mebibyte.
func roundMemory(bytes float64) float64 { return math.Ceil(bytes/mebibyte) * mebibyte }

// memory formats bytes as a Kubernetes binary quantity, rounded to a whole mebibyte from 1Mi up.
func memory(bytes float64) string {
	if bytes >= mebibyte {
		bytes = math.Round(bytes/mebibyte) * mebibyte
	}
	return resource.NewQuantity(int64(bytes), resource.BinarySI).String()
}

// cpu formats cores as a Kubernetes CPU quantity rounded to a millicore, such as "500m" or "2".
func cpu(cores float64) string {
	return resource.NewMilliQuantity(int64(math.Round(cores*1000)), resource.DecimalSI).String()
}
