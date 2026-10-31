package jvm

import (
	"cmp"
	"fmt"
	"math"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

const RuntimeAnnotation = "costdeck.io/runtime"

const (
	RuntimeJava = "java"
	RuntimeNone = "none"
)

const (
	DetectedByAnnotation = "annotation"
	DetectedByCommand    = "command"
	DetectedByOptions    = "options"
	DetectedByImage      = "image"
	DetectedByMetrics    = "metrics"
)

const (
	SeverityRisk  = "risk"
	SeverityWaste = "waste"
	SeverityNote  = "note"
)

const (
	CodeHeapOversized = "heap-oversized"
	CodeHeapTight     = "heap-tight"
)

const mebibyte = 1 << 20

const (
	defaultMaxRAMPercentage  = 25.0
	defaultMinRAMPercentage  = 50.0
	builtInMaxHeapSize       = 96 * mebibyte * 13 / 10
	minimumNativeMemory      = 128 * mebibyte
	nativeShareOfHeap        = 0.25
	threadsAndBuffers        = 64 * mebibyte
	observedNonHeapHeadroom  = 1.2
	liveSetMultiple          = 2.0
	minimumHeap              = 128 * mebibyte
	serverClassMemory        = 1792 * mebibyte
	serverClassProcessors    = 2
	tightHeapShare           = 0.8
	oversizedHeapShare       = 0.8
	reportedHeapTolerance    = 1.05
	recommendedRAMPercentage = 75
	defaultShareAppliesFrom  = builtInMaxHeapSize * 100 / defaultMaxRAMPercentage
)

const (
	toolOptions     = "JAVA_TOOL_OPTIONS"
	launcherOptions = "JDK_JAVA_OPTIONS"
	scriptOptions   = "JAVA_OPTS"
	overrideOptions = "_JAVA_OPTIONS"
)

var optionVariables = []string{toolOptions, launcherOptions, scriptOptions, overrideOptions}

var javaImageWords = []string{
	"java", "jre", "jdk", "openjdk", "temurin", "corretto", "amazoncorretto", "zulu", "liberica",
	"semeru", "graalvm", "sapmachine", "dragonwell",
}

var variableReference = regexp.MustCompile(`\$\(([A-Za-z_][A-Za-z0-9_]*)\)|\$\{([A-Za-z_][A-Za-z0-9_]*)\}|\$([A-Za-z_][A-Za-z0-9_]*)`)

type Options struct {
	MaxHeap              int64
	InitialHeap          int64
	MaxRAM               int64
	MaxRAMPercentage     float64
	MinRAMPercentage     float64
	ContainerSupportOff  bool
	ActiveProcessorCount int
	GC                   string
	Flags                []string
	UnreadFrom           []string
}

type Runtime struct {
	DetectedBy string
	Options    Options
}

type Heap struct {
	Max    float64
	Source string
	Known  bool
}

type Resources struct {
	MemoryRequest float64
	MemoryLimit   float64
	CPULimit      float64
}

type Usage struct {
	HeapUsed    float64
	HeapAfterGC float64
	NonHeap     float64
}

type Plan struct {
	Heap    float64
	Memory  float64
	Options string
}

type Finding struct {
	Severity string `json:"severity"`
	Code     string `json:"code"`
	Message  string `json:"message"`
}

func Inspect(c corev1.Container, annotation string) (Runtime, bool) {
	if annotation == RuntimeNone {
		return Runtime{}, false
	}
	options := Parse(OptionTokens(c))
	options.UnreadFrom = unreadOptionVariables(c.Env)
	var by string
	switch {
	case annotation == RuntimeJava:
		by = DetectedByAnnotation
	case runsJava(c.Command, c.Args):
		by = DetectedByCommand
	case setsOptionVariable(c.Env):
		by = DetectedByOptions
	case JavaImage(c.Image):
		by = DetectedByImage
	default:
		return Runtime{}, false
	}
	return Runtime{DetectedBy: by, Options: options}, true
}

func OptionTokens(c corev1.Container) []string {
	env := literalEnv(c.Env)
	var tokens []string
	for _, name := range []string{toolOptions, launcherOptions} {
		tokens = append(tokens, strings.Fields(env[name])...)
	}
	if !runsJava(c.Command, c.Args) {
		tokens = append(tokens, strings.Fields(env[scriptOptions])...)
	}
	for _, part := range slices.Concat(c.Command, c.Args) {
		tokens = append(tokens, strings.Fields(expand(part, env))...)
	}
	return append(tokens, strings.Fields(env[overrideOptions])...)
}

func Parse(tokens []string) Options {
	var o Options
	written := map[string]string{}
	var order []string
	remember := func(key, token string) {
		if _, seen := written[key]; !seen {
			order = append(order, key)
		}
		written[key] = token
	}
	for _, t := range tokens {
		switch {
		case strings.HasPrefix(t, "-Xmx"):
			if v, ok := parseSize(t[len("-Xmx"):]); ok {
				o.MaxHeap = v
				remember("max-heap", t)
			}
		case strings.HasPrefix(t, "-XX:MaxHeapSize="):
			if v, ok := parseSize(t[len("-XX:MaxHeapSize="):]); ok {
				o.MaxHeap = v
				remember("max-heap", t)
			}
		case strings.HasPrefix(t, "-Xms"):
			if v, ok := parseSize(t[len("-Xms"):]); ok {
				o.InitialHeap = v
				remember("initial-heap", t)
			}
		case strings.HasPrefix(t, "-XX:InitialHeapSize="):
			if v, ok := parseSize(t[len("-XX:InitialHeapSize="):]); ok {
				o.InitialHeap = v
				remember("initial-heap", t)
			}
		case strings.HasPrefix(t, "-XX:MaxRAMPercentage="):
			if v, ok := parsePercentage(t[len("-XX:MaxRAMPercentage="):]); ok {
				o.MaxRAMPercentage = v
				remember("max-ram-percentage", t)
			}
		case strings.HasPrefix(t, "-XX:MinRAMPercentage="):
			if v, ok := parsePercentage(t[len("-XX:MinRAMPercentage="):]); ok {
				o.MinRAMPercentage = v
				remember("min-ram-percentage", t)
			}
		case strings.HasPrefix(t, "-XX:MaxRAM="):
			if v, ok := parseSize(t[len("-XX:MaxRAM="):]); ok {
				o.MaxRAM = v
				remember("max-ram", t)
			}
		case t == "-XX:-UseContainerSupport", t == "-XX:+UseContainerSupport":
			o.ContainerSupportOff = t == "-XX:-UseContainerSupport"
			remember("container-support", t)
		case strings.HasPrefix(t, "-XX:ActiveProcessorCount="):
			if v, err := strconv.Atoi(t[len("-XX:ActiveProcessorCount="):]); err == nil && v > 0 {
				o.ActiveProcessorCount = v
				remember("processors", t)
			}
		default:
			if gc, ok := collectorFlags[t]; ok {
				o.GC = gc
				remember("gc", t)
			}
		}
	}
	for _, key := range order {
		o.Flags = append(o.Flags, written[key])
	}
	return o
}

var collectorFlags = map[string]string{
	"-XX:+UseG1GC":         "G1",
	"-XX:+UseParallelGC":   "Parallel",
	"-XX:+UseSerialGC":     "Serial",
	"-XX:+UseZGC":          "Z",
	"-XX:+UseShenandoahGC": "Shenandoah",
}

func (o Options) HeapFor(memoryLimit float64) Heap {
	if o.MaxHeap > 0 {
		return Heap{Max: float64(o.MaxHeap), Source: "set by -Xmx", Known: true}
	}
	physical, basis := memoryLimit, "the limit"
	if o.ContainerSupportOff {
		physical = 0
	}
	if o.MaxRAM > 0 {
		physical, basis = float64(o.MaxRAM), "-XX:MaxRAM"
	}
	if physical <= 0 {
		if o.ContainerSupportOff {
			return Heap{Source: "-XX:-UseContainerSupport: sized from the memory of the node"}
		}
		return Heap{Source: "no memory limit: sized from the memory of the node"}
	}
	maxShare := cmp.Or(o.MaxRAMPercentage, defaultMaxRAMPercentage)
	minShare := cmp.Or(o.MinRAMPercentage, defaultMinRAMPercentage)
	if physical*minShare/100 < builtInMaxHeapSize {
		return Heap{Max: physical * minShare / 100, Source: fmt.Sprintf("the JVM default for a small amount of memory, %g%% of %s", minShare, basis), Known: true}
	}
	if o.MaxRAMPercentage > 0 {
		return Heap{Max: max(physical*maxShare/100, builtInMaxHeapSize), Source: fmt.Sprintf("%g%% of %s, set by -XX:MaxRAMPercentage", maxShare, basis), Known: true}
	}
	return Heap{Max: max(physical*maxShare/100, builtInMaxHeapSize), Source: fmt.Sprintf("the JVM default, %g%% of %s", maxShare, basis), Known: true}
}

func (o Options) defaultHeap() bool {
	return o.MaxHeap == 0 && o.MaxRAMPercentage == 0 && o.MaxRAM == 0
}

func NativeMemory(heap, observedNonHeap float64) float64 {
	if observedNonHeap > 0 {
		return observedNonHeap*observedNonHeapHeadroom + threadsAndBuffers
	}
	return max(heap*nativeShareOfHeap, minimumNativeMemory)
}

func PlanFor(o Options, u Usage) (Plan, bool) {
	if u.HeapAfterGC <= 0 {
		return Plan{}, false
	}
	heap := roundMebibytes(max(u.HeapAfterGC*liveSetMultiple, minimumHeap))
	memory := roundMebibytes(heap + NativeMemory(heap, u.NonHeap))
	options := "-Xmx" + jvmSize(heap)
	if o.MaxHeap == 0 {
		options = fmt.Sprintf("-XX:MaxRAMPercentage=%d", int(heap/memory*100))
	}
	return Plan{Heap: heap, Memory: memory, Options: options}, true
}

func Audit(o Options, heap Heap, r Resources, u Usage) []Finding {
	var findings []Finding
	add := func(severity, code, format string, args ...any) {
		findings = append(findings, Finding{Severity: severity, Code: code, Message: fmt.Sprintf(format, args...)})
	}
	native := NativeMemory(heap.Max, u.NonHeap)
	switch {
	case o.ContainerSupportOff && o.MaxHeap == 0 && o.MaxRAM == 0:
		add(SeverityRisk, "container-support-off", "-XX:-UseContainerSupport makes the JVM ignore the container limit and size its heap "+
			"from the memory of the node, so the heap can outgrow the limit and the container is OOM-killed. Remove the flag or set -Xmx.")
	case !heap.Known:
		add(SeverityRisk, "no-memory-limit", "There is no memory limit and no -Xmx, so the JVM sizes its heap from the memory of whichever "+
			"node the pod lands on: the same image takes a different heap on every node size. Set a memory limit and "+
			"-XX:MaxRAMPercentage=%d.", recommendedRAMPercentage)
	case r.MemoryLimit > 0 && heap.Max >= r.MemoryLimit:
		add(SeverityRisk, "heap-over-limit", "The heap can grow to %s, as much as the %s memory limit or more, so the container is "+
			"OOM-killed before the heap fills. Keep the heap at about %d%% of the limit.", quantity(heap.Max), quantity(r.MemoryLimit), recommendedRAMPercentage)
	case r.MemoryLimit > 0 && r.MemoryLimit-heap.Max < native:
		add(SeverityRisk, "no-room-for-native", "The heap can grow to %s of the %s limit, which leaves %s for metaspace, thread stacks, "+
			"the code cache and buffers, where a JVM like this needs about %s. Lower the heap or raise the limit.",
			quantity(heap.Max), quantity(r.MemoryLimit), quantity(r.MemoryLimit-heap.Max), quantity(native))
	}
	if heap.Known && o.defaultHeap() && r.MemoryLimit > defaultShareAppliesFrom {
		add(SeverityWaste, "default-heap", "No heap size is set, so the JVM keeps to the default %g%% of the %s limit (%s) and most "+
			"of the memory the pod reserves stays unused. Set -XX:MaxRAMPercentage=%d, then lower the request and limit to "+
			"what the application needs.", defaultMaxRAMPercentage, quantity(r.MemoryLimit), quantity(heap.Max), recommendedRAMPercentage)
	}
	if heap.Known && r.MemoryRequest > 0 && r.MemoryRequest < heap.Max+native {
		add(SeverityRisk, "request-below-heap", "The memory request (%s) is below what the JVM can grow to: a %s heap plus about %s of "+
			"native memory. Once the heap fills, the pod uses more than it reserved and is among the first evicted under "+
			"memory pressure.", quantity(r.MemoryRequest), quantity(heap.Max), quantity(native))
	}
	if plan, ok := PlanFor(o, u); ok && heap.Known {
		switch {
		case u.HeapAfterGC > heap.Max*tightHeapShare:
			add(SeverityRisk, CodeHeapTight, "After garbage collection the heap still held %s of its %s, so the JVM collects more and "+
				"more often and risks OutOfMemoryError. Raise the heap to about %s.", quantity(u.HeapAfterGC), quantity(heap.Max), quantity(plan.Heap))
		case plan.Heap <= heap.Max*oversizedHeapShare:
			add(SeverityWaste, CodeHeapOversized, "After garbage collection the heap held at most %s, so a %s heap is enough instead of "+
				"%s, and the container then needs about %s of memory.", quantity(u.HeapAfterGC), quantity(plan.Heap), quantity(heap.Max), quantity(plan.Memory))
		}
	}
	if heap.Known && u.HeapUsed > heap.Max*reportedHeapTolerance {
		add(SeverityNote, "heap-differs", "The JVM reported %s of heap in use, more than the %s its flags allow, so its options also "+
			"come from somewhere Cost Deck does not see, such as the image.", quantity(u.HeapUsed), quantity(heap.Max))
	}
	if o.GC == "" && serialCollector(o, r) {
		add(SeverityNote, "serial-gc", "With fewer than %d CPUs or less than %s of memory the JVM picks the Serial garbage collector, "+
			"which stops the application for every collection. That suits a small service; for steady latency set "+
			"-XX:+UseG1GC or -XX:ActiveProcessorCount=%d.", serverClassProcessors, quantity(serverClassMemory), serverClassProcessors)
	}
	for _, name := range o.UnreadFrom {
		add(SeverityNote, "unread-options", "%s comes from a ConfigMap or Secret, which Cost Deck does not read, so flags set there "+
			"are not part of this analysis.", name)
	}
	slices.SortStableFunc(findings, func(a, b Finding) int { return cmp.Compare(severityRank[a.Severity], severityRank[b.Severity]) })
	return findings
}

var severityRank = map[string]int{SeverityRisk: 0, SeverityWaste: 1, SeverityNote: 2}

func serialCollector(o Options, r Resources) bool {
	processors := o.ActiveProcessorCount
	if processors == 0 && r.CPULimit > 0 {
		processors = int(math.Ceil(r.CPULimit))
	}
	return (processors > 0 && processors < serverClassProcessors) || (r.MemoryLimit > 0 && r.MemoryLimit < serverClassMemory)
}

func JavaImage(image string) bool {
	repository, tag := image, ""
	if i := strings.Index(repository, "@"); i >= 0 {
		repository = repository[:i]
	}
	if i := strings.LastIndex(repository, ":"); i > strings.LastIndex(repository, "/") {
		repository, tag = repository[:i], repository[i+1:]
	}
	segments := strings.Split(repository, "/")
	if len(segments) > 1 && strings.ContainsAny(segments[0], ".:") {
		segments = segments[1:]
	}
	for _, segment := range segments {
		if slices.ContainsFunc(words(segment), func(w string) bool { return slices.Contains(javaImageWords, w) }) {
			return true
		}
	}
	return slices.ContainsFunc(words(tag), func(w string) bool { return w == "jre" || w == "jdk" })
}

func words(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return r == '-' || r == '_' || r == '.' })
}

func runsJava(command, args []string) bool {
	for _, part := range slices.Concat(command, args) {
		for field := range strings.FieldsSeq(part) {
			if path.Base(field) == "java" {
				return true
			}
		}
	}
	return false
}

func setsOptionVariable(env []corev1.EnvVar) bool {
	return slices.ContainsFunc(env, func(e corev1.EnvVar) bool { return slices.Contains(optionVariables, e.Name) })
}

func unreadOptionVariables(env []corev1.EnvVar) []string {
	var names []string
	for _, e := range env {
		if e.ValueFrom != nil && slices.Contains(optionVariables, e.Name) {
			names = append(names, e.Name)
		}
	}
	return names
}

func literalEnv(env []corev1.EnvVar) map[string]string {
	values := map[string]string{}
	for _, e := range env {
		if e.ValueFrom == nil {
			values[e.Name] = expand(e.Value, values)
		}
	}
	return values
}

func expand(s string, env map[string]string) string {
	return variableReference.ReplaceAllStringFunc(s, func(reference string) string {
		match := variableReference.FindStringSubmatch(reference)
		if v, ok := env[cmp.Or(match[1], match[2], match[3])]; ok {
			return v
		}
		return reference
	})
}

func parseSize(s string) (int64, bool) {
	if s == "" {
		return 0, false
	}
	multiplier := int64(1)
	switch s[len(s)-1] {
	case 'k', 'K':
		multiplier = 1 << 10
	case 'm', 'M':
		multiplier = 1 << 20
	case 'g', 'G':
		multiplier = 1 << 30
	case 't', 'T':
		multiplier = 1 << 40
	}
	digits := s
	if multiplier > 1 {
		digits = s[:len(s)-1]
	}
	if digits == "" || strings.ContainsFunc(digits, func(r rune) bool { return r < '0' || r > '9' }) {
		return 0, false
	}
	n, err := strconv.ParseInt(digits, 10, 64)
	if err != nil || n <= 0 || n > math.MaxInt64/multiplier {
		return 0, false
	}
	return n * multiplier, true
}

func parsePercentage(s string) (float64, bool) {
	v, err := strconv.ParseFloat(s, 64)
	return v, err == nil && v > 0 && v <= 100
}

func roundMebibytes(bytes float64) float64 { return math.Ceil(bytes/mebibyte) * mebibyte }

func jvmSize(bytes float64) string {
	mebibytes := int64(math.Ceil(bytes / mebibyte))
	if mebibytes%1024 == 0 {
		return strconv.FormatInt(mebibytes/1024, 10) + "g"
	}
	return strconv.FormatInt(mebibytes, 10) + "m"
}

func quantity(bytes float64) string {
	if bytes >= mebibyte {
		bytes = math.Round(bytes/mebibyte) * mebibyte
	}
	return resource.NewQuantity(int64(bytes), resource.BinarySI).String()
}
