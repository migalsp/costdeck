package controller

import (
	"context"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/migalsp/costdeck-operator/internal/scaling"
)

// ConditionGitOps names the GitOps tools that manage a schedule's workloads and says
// whether the schedule pauses them while down. It is absent when none does.
const ConditionGitOps = "GitOps"

// reconcileGitOps pauses, before a scale-down and when the schedule asks for it, the GitOps
// objects that would bring the workloads back, and reports them in the GitOps condition.
func reconcileGitOps(ctx context.Context, engine *scaling.Engine, conds *[]metav1.Condition, generation int64, namespaces []string, exclusionsOf func(string) []string, pause, active bool) {
	var owners, notes []string
	for _, ns := range namespaces {
		if pause && !active {
			n, err := engine.PauseGitOps(ctx, ns, exclusionsOf(ns), namespaces)
			if err != nil {
				logf.FromContext(ctx).Error(err, "Could not pause GitOps", "namespace", ns)
			}
			notes = append(notes, n...)
		}
		found, err := engine.GitOpsOwners(ctx, ns, exclusionsOf(ns))
		if err != nil {
			continue
		}
		for _, o := range found {
			if s := o.String(); !slices.Contains(owners, s) {
				owners = append(owners, s)
			}
		}
	}
	if len(owners) == 0 {
		meta.RemoveStatusCondition(conds, ConditionGitOps)
		return
	}
	cond := metav1.Condition{Type: ConditionGitOps, Status: metav1.ConditionTrue, ObservedGeneration: generation}
	managed := "The workloads are managed by " + strings.Join(owners, ", ") + "."
	switch {
	case !pause:
		cond.Reason = "NotPaused"
		cond.Message = managed + " Unless they ignore the replicas (Argo CD: managedFieldsManagers [" + scaling.FieldManager +
			"]), their sync brings scaled-down workloads back; spec.pauseGitOps pauses them while the schedule keeps them down."
	case len(notes) > 0:
		cond.Reason = "PartlyPaused"
		cond.Message = managed + " Not paused: " + strings.Join(notes, "; ") + "."
	default:
		cond.Reason = "Paused"
		cond.Message = managed + " They are paused while the schedule keeps the workloads down."
	}
	meta.SetStatusCondition(conds, cond)
}

// resumeGitOps resumes, once everything is up again, what reconcileGitOps paused.
func resumeGitOps(ctx context.Context, engine *scaling.Engine, namespaces []string, exclusionsOf func(string) []string) {
	for _, ns := range namespaces {
		if err := engine.ResumeGitOps(ctx, ns, exclusionsOf(ns)); err != nil {
			logf.FromContext(ctx).Error(err, "Could not resume GitOps", "namespace", ns)
		}
	}
}
