package jvm

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func FuzzJVMOptions(f *testing.F) {
	f.Add("-Xmx2g -XX:+UseG1GC", "java -Xmx$(HEAP) -jar app.jar", "768m", uint32(2048))
	f.Add("-XX:MaxRAMPercentage=75 -XX:MinRAMPercentage=60", "sh -c exec java $JAVA_OPTS -jar app.jar", "", uint32(300))
	f.Add("-XX:-UseContainerSupport -XX:MaxRAM=1g", "java", "", uint32(0))
	f.Add("-Xmx99999999999999999t -XX:MaxRAMPercentage=1e309", "${HEAP} $(HEAP) $HEAP", "$(HEAP)", uint32(64))
	f.Fuzz(func(t *testing.T, toolOptionsValue, commandLine, heapValue string, limitMebibytes uint32) {
		c := corev1.Container{
			Command: strings.Fields(commandLine),
			Env:     env("JAVA_TOOL_OPTIONS", toolOptionsValue, "HEAP", heapValue, "JAVA_OPTS", toolOptionsValue),
		}
		rt, _ := Inspect(c, RuntimeJava)
		o := rt.Options
		if o.MaxHeap < 0 || o.InitialHeap < 0 || o.MaxRAM < 0 || o.ActiveProcessorCount < 0 {
			t.Fatalf("negative option values: %+v", o)
		}
		if o.MaxRAMPercentage < 0 || o.MaxRAMPercentage > 100 || o.MinRAMPercentage < 0 || o.MinRAMPercentage > 100 {
			t.Fatalf("a percentage outside 0-100: %+v", o)
		}
		limit := float64(limitMebibytes) * mebibyte
		heap := o.HeapFor(limit)
		if heap.Known && heap.Max <= 0 {
			t.Fatalf("a known heap of %v bytes from %+v and a %v limit", heap.Max, o, limit)
		}
		if heap.Known && o.MaxHeap == 0 && o.MaxRAM == 0 && heap.Max > limit {
			t.Fatalf("an ergonomic heap of %v bytes above the %v limit, from %+v", heap.Max, limit, o)
		}
		res := Resources{MemoryRequest: limit / 2, MemoryLimit: limit, CPULimit: 1}
		for _, finding := range Audit(o, heap, res, Usage{HeapUsed: limit / 3, HeapAfterGC: limit / 5}) {
			if finding.Message == "" || severityRank[finding.Severity] == 0 && finding.Severity != SeverityRisk {
				t.Fatalf("a finding without a message or with an unknown severity: %+v", finding)
			}
		}
	})
}
