package scaling

import (
	"context"
	"sort"

	batchv1 "k8s.io/api/batch/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// WorkloadPreview is one Deployment or StatefulSet a schedule would scale.
type WorkloadPreview struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
	// Replicas it runs with when up: the current count, or the one a scale-up restores.
	Replicas int32 `json:"replicas"`
	// Excluded workloads (workload rules) are never scaled down; ManagedBy names the kind of
	// operator resource that stops them instead (a hibernate rule).
	Excluded  bool   `json:"excluded,omitempty"`
	ManagedBy string `json:"managedBy,omitempty"`
	// CPU and Memory are the requests of one replica.
	CPU    string `json:"cpu"`
	Memory string `json:"memory"`
	// GitOps is the GitOps object managing the workload, if any.
	GitOps *GitOpsOwner `json:"gitOps,omitempty"`
}

// NamespacePreview is what a schedule would scale in one namespace.
type NamespacePreview struct {
	Workloads []WorkloadPreview `json:"workloads"`
	CronJobs  int               `json:"cronJobs"`
	// Services are the operator-run services hibernate rules stop after the workloads.
	Services []HibernatedService `json:"services,omitempty"`
	// CPU and Memory are what scaling the namespace down frees: the requests of every
	// replica of the workloads that are not excluded.
	CPU    resource.Quantity `json:"-"`
	Memory resource.Quantity `json:"-"`
}

// PreviewNamespace lists, without changing anything, what scaling a namespace down would
// stop, with the same exclusions and recorded replica counts the real scale-down uses.
func (e *Engine) PreviewNamespace(ctx context.Context, ns string, exclusions []string) (NamespacePreview, error) {
	var out NamespacePreview
	objs, err := e.listScalableResources(ctx, ns, nil)
	if err != nil {
		return out, err
	}
	for _, obj := range objs {
		tmpl := podTemplate(obj)
		// The count a scale-up gives it, so a workload that is down shows what it returns to.
		replicas := targetReplicas(true, getReplicas(obj), recordedReplicas(obj, nil))
		cpu, mem := PodRequests(tmpl.Spec)
		w := WorkloadPreview{Kind: workloadKind(obj), Name: obj.GetName(), Replicas: replicas, Excluded: excludedObject(obj, exclusions),
			ManagedBy: ownedByKind(obj, exclusions), CPU: cpu.String(), Memory: mem.String(), GitOps: GitOpsOwnerOf(obj)}
		out.Workloads = append(out.Workloads, w)
		if !w.Excluded {
			cpu.Mul(int64(replicas))
			mem.Mul(int64(replicas))
			out.CPU.Add(cpu)
			out.Memory.Add(mem)
		}
	}
	sort.Slice(out.Workloads, func(i, j int) bool {
		if out.Workloads[i].Kind != out.Workloads[j].Kind {
			return out.Workloads[i].Kind < out.Workloads[j].Kind
		}
		return out.Workloads[i].Name < out.Workloads[j].Name
	})
	var cronJobs batchv1.CronJobList
	if err := e.Client.List(ctx, &cronJobs, client.InNamespace(ns)); err == nil {
		for _, cj := range cronJobs.Items {
			if !isExcluded(cj.Name, exclusions) {
				out.CronJobs++
			}
		}
	}
	return out, nil
}

// WouldChange counts the workloads of a namespace that scaling it to the desired state
// would change, for a dry run. It changes nothing.
func (e *Engine) WouldChange(ctx context.Context, ns string, active bool, exclusions []string) (int, error) {
	objs, err := e.listScalableResources(ctx, ns, exclusions)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, obj := range objs {
		current := getReplicas(obj)
		if targetReplicas(active, current, recordedReplicas(obj, nil)) != current {
			n++
		}
	}
	return n, nil
}
