package rightsizing

import (
	"testing"

	"github.com/migalsp/costdeck-operator/internal/metrics"
)

func TestPolicyBoundsAndPadsTheAdvice(t *testing.T) {
	half := 1.5
	workloads := []Workload{{Kind: "Deployment", Name: "api", Replicas: 1, Containers: []Container{{Name: "api", CPURequest: 1, MemoryRequest: 2048 * mi}},
		Policy: &Policy{Name: "team-a", ApplyForbidden: true, CPU: Bounds{Min: 0.3, Headroom: &half}, Memory: Bounds{Max: 512 * mi}}}}
	demand := map[metrics.ContainerKey]metrics.Usage{{Pod: "api-7d9f8c-abcde", Container: "api"}: usage("100m", "300Mi")}

	w := Analyze(workloads, demand, nil, rates, DefaultSettings(true)).Workloads[0]
	c := w.Containers[0]
	if c.CPU.Recommended != "300m" {
		t.Errorf("CPU advice = %+v, want the policy's 300m floor over 150m (100m plus its 50%% headroom)", c.CPU)
	}
	if c.Memory.Recommended != "360Mi" {
		t.Errorf("memory advice = %+v, want 360Mi: 300Mi plus the default 20%%, inside the 512Mi maximum", c.Memory)
	}
	if w.Policy != "team-a" || !w.ApplyForbidden {
		t.Errorf("workload advice = %+v, want the policy named and applying forbidden", w)
	}

	demand[metrics.ContainerKey{Pod: "api-7d9f8c-abcde", Container: "api"}] = usage("100m", "1200Mi")
	if m := Analyze(workloads, demand, nil, rates, DefaultSettings(true)).Workloads[0].Containers[0].Memory; m.Recommended != "512Mi" {
		t.Errorf("memory advice = %+v, want the policy's 512Mi maximum", m)
	}

	workloads[0].Policy = nil
	if c := Analyze(workloads, demand, nil, rates, DefaultSettings(true)).Workloads[0]; c.Policy != "" || c.ApplyForbidden || c.Containers[0].CPU.Recommended != "120m" {
		t.Errorf("without a policy = %+v", c)
	}
}
