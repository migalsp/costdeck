package jvm

import (
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

const gibibyte = 1 << 30

func env(pairs ...string) []corev1.EnvVar {
	var out []corev1.EnvVar
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, corev1.EnvVar{Name: pairs[i], Value: pairs[i+1]})
	}
	return out
}

func TestJavaImage(t *testing.T) {
	for image, want := range map[string]bool{
		"eclipse-temurin:21-jre":                         true,
		"docker.io/library/openjdk:17":                   true,
		"amazoncorretto:21-alpine":                       true,
		"azul/zulu-openjdk:21":                           true,
		"bellsoft/liberica-runtime-container:jre-21":     true,
		"registry.example.com/team/orders:1.4-jre":       true,
		"ghcr.io/acme/billing-java:2.0":                  true,
		"icr.io/appcafe/ibm-semeru-runtimes:open-21-jre": true,
		"nginx:1.27":                        false,
		"node:22-alpine":                    false,
		"ghcr.io/acme/javascript-builder:1": false,
		"java.example.com/team/api:1":       false,
		"busybox@sha256:0123":               false,
	} {
		if got := JavaImage(image); got != want {
			t.Errorf("JavaImage(%q) = %v, want %v", image, got, want)
		}
	}
}

func TestInspect(t *testing.T) {
	fromConfigMap := corev1.EnvVar{Name: "JAVA_TOOL_OPTIONS", ValueFrom: &corev1.EnvVarSource{ConfigMapKeyRef: &corev1.ConfigMapKeySelector{Key: "opts"}}}
	for _, tc := range []struct {
		name       string
		container  corev1.Container
		annotation string
		want       string
	}{
		{"the java command", corev1.Container{Image: "acme/orders", Command: []string{"/opt/java/openjdk/bin/java"}, Args: []string{"-jar", "app.jar"}}, "", DetectedByCommand},
		{"java started by a shell", corev1.Container{Image: "acme/orders", Command: []string{"sh", "-c"}, Args: []string{"exec java $JAVA_OPTS -jar /app.jar"}}, "", DetectedByCommand},
		{"JVM options in the environment", corev1.Container{Image: "acme/orders", Env: env("JAVA_TOOL_OPTIONS", "-XX:+UseG1GC")}, "", DetectedByOptions},
		{"JVM options from a ConfigMap", corev1.Container{Image: "acme/orders", Env: []corev1.EnvVar{fromConfigMap}}, "", DetectedByOptions},
		{"a JVM image", corev1.Container{Image: "eclipse-temurin:21-jre"}, "", DetectedByImage},
		{"the annotation names Java", corev1.Container{Image: "acme/orders"}, RuntimeJava, DetectedByAnnotation},
		{"nothing Java about it", corev1.Container{Image: "nginx:1.27", Command: []string{"nginx"}}, "", ""},
		{"the annotation opts out", corev1.Container{Image: "eclipse-temurin:21-jre", Env: env("JAVA_TOOL_OPTIONS", "-Xmx1g")}, RuntimeNone, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, ok := Inspect(tc.container, tc.annotation)
			if ok != (tc.want != "") || rt.DetectedBy != tc.want {
				t.Errorf("Inspect = %+v, %v, want detected by %q", rt, ok, tc.want)
			}
		})
	}
	rt, _ := Inspect(corev1.Container{Env: []corev1.EnvVar{fromConfigMap}}, "")
	if !slices.Equal(rt.Options.UnreadFrom, []string{"JAVA_TOOL_OPTIONS"}) {
		t.Errorf("unread options = %v, want JAVA_TOOL_OPTIONS", rt.Options.UnreadFrom)
	}
}

func TestOptionsFollowTheJVMOrder(t *testing.T) {
	for _, tc := range []struct {
		name      string
		container corev1.Container
		maxHeap   int64
		flags     []string
	}{
		{
			"the command line beats JAVA_TOOL_OPTIONS",
			corev1.Container{Command: []string{"java", "-Xmx2g", "-jar", "app.jar"}, Env: env("JAVA_TOOL_OPTIONS", "-Xmx1g -XX:+UseG1GC")},
			2 * gibibyte, []string{"-Xmx2g", "-XX:+UseG1GC"},
		},
		{
			"_JAVA_OPTIONS beats everything",
			corev1.Container{Command: []string{"java", "-Xmx2g"}, Env: env("_JAVA_OPTIONS", "-Xmx512m")},
			512 << 20, []string{"-Xmx512m"},
		},
		{
			"Kubernetes expands $(VAR) in the arguments",
			corev1.Container{Command: []string{"java"}, Args: []string{"-Xmx$(HEAP)", "-jar", "app.jar"}, Env: env("HEAP", "768m")},
			768 << 20, []string{"-Xmx768m"},
		},
		{
			"a shell expands $JAVA_OPTS",
			corev1.Container{Command: []string{"sh", "-c", "exec java $JAVA_OPTS -jar app.jar"}, Env: env("JAVA_OPTS", "-XX:MaxRAMPercentage=70")},
			0, []string{"-XX:MaxRAMPercentage=70"},
		},
		{
			"the java command does not read JAVA_OPTS itself",
			corev1.Container{Command: []string{"java", "-jar", "app.jar"}, Env: env("JAVA_OPTS", "-Xmx4g")},
			0, nil,
		},
		{
			"a shell that starts java without $JAVA_OPTS does not pass it",
			corev1.Container{Command: []string{"sh", "-c", "exec java -jar app.jar"}, Env: env("JAVA_OPTS", "-Xmx4g")},
			0, nil,
		},
		{
			"start scripts read JAVA_OPTS",
			corev1.Container{Image: "tomcat:10-jre21", Env: env("JAVA_OPTS", "-Xmx3g")},
			3 * gibibyte, []string{"-Xmx3g"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := Parse(OptionTokens(tc.container))
			if o.MaxHeap != tc.maxHeap || !slices.Equal(o.Flags, tc.flags) {
				t.Errorf("max heap = %d, flags = %q; want %d, %q", o.MaxHeap, o.Flags, tc.maxHeap, tc.flags)
			}
		})
	}
}

func TestParseReadsSizesLikeTheJVM(t *testing.T) {
	for token, want := range map[string]int64{
		"-Xmx1024":               1024,
		"-Xmx64k":                64 << 10,
		"-Xmx512M":               512 << 20,
		"-Xmx2g":                 2 * gibibyte,
		"-XX:MaxHeapSize=1t":     1 << 40,
		"-Xmx":                   0,
		"-Xmx1.5g":               0,
		"-Xmx-1g":                0,
		"-Xmx99999999999999999g": 0,
		"-XX:MaxHeapSize=wrong":  0,
	} {
		if got := Parse([]string{token}).MaxHeap; got != want {
			t.Errorf("Parse(%q).MaxHeap = %d, want %d", token, got, want)
		}
	}
	o := Parse([]string{"-XX:MaxRAMPercentage=150", "-XX:MinRAMPercentage=0", "-XX:ActiveProcessorCount=0", "-XX:+UseZGC", "-XX:-UseContainerSupport"})
	if o.MaxRAMPercentage != 0 || o.MinRAMPercentage != 0 || o.ActiveProcessorCount != 0 || o.GC != "Z" || !o.ContainerSupportOff {
		t.Errorf("options = %+v: percentages outside 0-100 and a zero processor count are ignored", o)
	}
}

func TestHeapFor(t *testing.T) {
	for _, tc := range []struct {
		name    string
		options Options
		limit   float64
		want    float64
		known   bool
		source  string
	}{
		{"-Xmx fixes the heap", Options{MaxHeap: gibibyte}, 4 * gibibyte, gibibyte, true, "-Xmx"},
		{"the default is a quarter of the limit", Options{}, 2 * gibibyte, 512 << 20, true, "default, 25%"},
		{"a small limit gets half", Options{}, 200 << 20, 100 << 20, true, "small amount of memory, 50% of the limit"},
		{"between them the built-in heap size wins", Options{}, 400 << 20, builtInMaxHeapSize, true, "default, 25%"},
		{"MaxRAMPercentage sets the share", Options{MaxRAMPercentage: 75}, 2 * gibibyte, 1536 << 20, true, "MaxRAMPercentage"},
		{"MaxRAM stands in for a missing limit", Options{MaxRAM: gibibyte}, 0, 256 << 20, true, "default, 25% of -XX:MaxRAM"},
		{"MaxRAM beats the limit, as in the JVM", Options{MaxRAM: 8 * gibibyte}, 2 * gibibyte, 2 * gibibyte, true, "25% of -XX:MaxRAM"},
		{"no limit leaves it to the node", Options{}, 0, 0, false, "no memory limit"},
		{"without container support the limit does not count", Options{ContainerSupportOff: true}, 2 * gibibyte, 0, false, "UseContainerSupport"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := tc.options.HeapFor(tc.limit)
			if h.Known != tc.known || h.Max != tc.want || !strings.Contains(h.Source, tc.source) {
				t.Errorf("HeapFor = %+v, want %v bytes, known %v, source mentioning %q", h, tc.want, tc.known, tc.source)
			}
		})
	}
}

func TestPlanFor(t *testing.T) {
	plan, ok := PlanFor(Options{MaxHeap: 3 * gibibyte}, Usage{HeapAfterGC: 400 << 20})
	if !ok || plan.Heap != 800<<20 || plan.Memory != 1000<<20 || plan.Options != "-Xmx800m" {
		t.Errorf("plan = %+v: twice the heap left after GC, a quarter of it again for native memory, as -Xmx", plan)
	}
	plan, _ = PlanFor(Options{}, Usage{HeapAfterGC: 512 << 20, NonHeap: 200 << 20})
	if plan.Heap != gibibyte || plan.Memory != 1328<<20 || plan.Options != "-XX:MaxRAMPercentage=77" {
		t.Errorf("plan = %+v: observed non-heap memory plus threads and buffers, as a share of the new limit", plan)
	}
	if plan, _ := PlanFor(Options{}, Usage{HeapAfterGC: 10 << 20}); plan.Heap != minimumHeap {
		t.Errorf("plan heap = %v, want the %v floor", plan.Heap, minimumHeap)
	}
	if _, ok := PlanFor(Options{}, Usage{HeapUsed: gibibyte}); ok {
		t.Error("a plan needs the heap left after garbage collection")
	}
}

func TestAudit(t *testing.T) {
	codes := func(f []Finding) []string {
		out := make([]string, 0, len(f))
		for _, x := range f {
			out = append(out, x.Code)
		}
		return out
	}
	for _, tc := range []struct {
		name    string
		options Options
		res     Resources
		usage   Usage
		want    []string
	}{
		{"a well sized JVM", Options{MaxRAMPercentage: 75, GC: "G1"}, Resources{MemoryRequest: 2 * gibibyte, MemoryLimit: 2 * gibibyte, CPULimit: 2}, Usage{}, nil},
		{"the default quarter of the limit", Options{GC: "G1"}, Resources{MemoryRequest: 2 * gibibyte, MemoryLimit: 2 * gibibyte}, Usage{}, []string{"default-heap"}},
		{"a heap as large as the limit", Options{MaxHeap: 2 * gibibyte, GC: "G1"}, Resources{MemoryRequest: 3 * gibibyte, MemoryLimit: 2 * gibibyte}, Usage{}, []string{"heap-over-limit"}},
		{"no room left for native memory", Options{MaxHeap: 900 << 20, GC: "G1"}, Resources{MemoryRequest: 2 * gibibyte, MemoryLimit: gibibyte, CPULimit: 2}, Usage{}, []string{"no-room-for-native"}},
		{"a request below the heap", Options{MaxHeap: gibibyte, GC: "G1"}, Resources{MemoryRequest: 512 << 20, MemoryLimit: 2 * gibibyte}, Usage{}, []string{"request-below-heap"}},
		{"no limit and no heap size", Options{GC: "G1"}, Resources{MemoryRequest: gibibyte}, Usage{}, []string{"no-memory-limit"}},
		{"container support turned off", Options{ContainerSupportOff: true, GC: "G1"}, Resources{MemoryRequest: gibibyte, MemoryLimit: gibibyte}, Usage{}, []string{"container-support-off"}},
		{"MaxRAM larger than the limit", Options{MaxRAM: 8 * gibibyte, GC: "G1"}, Resources{MemoryRequest: 3 * gibibyte, MemoryLimit: 2 * gibibyte}, Usage{}, []string{"heap-over-limit"}},
		{"one CPU picks the Serial collector", Options{MaxRAMPercentage: 75}, Resources{MemoryRequest: 2 * gibibyte, MemoryLimit: 2 * gibibyte, CPULimit: 1}, Usage{}, []string{"serial-gc"}},
		{"options from a ConfigMap", Options{MaxRAMPercentage: 75, GC: "G1", UnreadFrom: []string{"JAVA_TOOL_OPTIONS"}}, Resources{MemoryRequest: 2 * gibibyte, MemoryLimit: 2 * gibibyte}, Usage{}, []string{"unread-options"}},
		{"a heap far larger than its live data", Options{MaxHeap: 3 * gibibyte, GC: "G1"}, Resources{MemoryRequest: 4 * gibibyte, MemoryLimit: 4 * gibibyte}, Usage{HeapUsed: 2 * gibibyte, HeapAfterGC: 400 << 20}, []string{"heap-oversized"}},
		{"a heap still full after collection", Options{MaxHeap: gibibyte, GC: "G1"}, Resources{MemoryRequest: 2 * gibibyte, MemoryLimit: 2 * gibibyte}, Usage{HeapUsed: gibibyte, HeapAfterGC: 900 << 20}, []string{"heap-tight"}},
		{"more heap in use than the flags allow", Options{MaxHeap: gibibyte, GC: "G1"}, Resources{MemoryRequest: 2 * gibibyte, MemoryLimit: 2 * gibibyte}, Usage{HeapUsed: 1536 << 20}, []string{"heap-differs"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Audit(tc.options, tc.options.HeapFor(tc.res.MemoryLimit), tc.res, tc.usage)
			if !slices.Equal(codes(got), tc.want) {
				t.Errorf("findings = %q, want %q", codes(got), tc.want)
			}
			for _, f := range got {
				if f.Message == "" || strings.ContainsRune(f.Message, '\u2014') {
					t.Errorf("finding %s has a message that is empty or uses an em dash: %q", f.Code, f.Message)
				}
			}
		})
	}
}

func TestAuditListsRisksFirst(t *testing.T) {
	got := Audit(Options{UnreadFrom: []string{"JAVA_TOOL_OPTIONS"}}, Options{}.HeapFor(2*gibibyte), Resources{MemoryRequest: 256 << 20, MemoryLimit: 2 * gibibyte, CPULimit: 1}, Usage{})
	severities := make([]string, 0, len(got))
	for _, f := range got {
		severities = append(severities, f.Severity)
	}
	if !slices.IsSortedFunc(severities, func(a, b string) int { return severityRank[a] - severityRank[b] }) || severities[0] != SeverityRisk {
		t.Errorf("severities = %q, want risks, then waste, then notes", severities)
	}
}
