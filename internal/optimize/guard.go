package optimize

import (
	"context"
	"errors"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/config"
)

// DefaultGuardMinutes is how long a change is watched without spec.rightsizing.guardMinutes.
const DefaultGuardMinutes = 30

// Sender delivers alerts; notify.Dispatcher does.
type Sender interface {
	Send(ctx context.Context, event finopsv1.NotificationEvent, markdown string) ([]string, error)
}

// Guard watches the new pods of applied changes and puts the old requests back when one
// of them is OOMKilled, restarts or crash-loops; a change that stays quiet for the guard
// period is kept. The leader runs it.
type Guard struct {
	Client client.Client
	Notify Sender
	Now    func() time.Time
}

// NeedLeaderElection makes only the leader revert changes.
func (g *Guard) NeedLeaderElection() bool { return true }

// Start checks every 30 seconds.
func (g *Guard) Start(ctx context.Context) error {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		if err := g.Check(ctx); err != nil {
			logf.FromContext(ctx).WithName("rightsizing-guard").Error(err, "Could not check right-sized workloads")
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// Check looks at every change still watched.
func (g *Guard) Check(ctx context.Context) error {
	cfg, err := config.Get(ctx, g.Client)
	if err != nil {
		return err
	}
	period := time.Duration(DefaultGuardMinutes) * time.Minute
	if m := cfg.Spec.Rightsizing.GuardMinutes; m > 0 {
		period = time.Duration(m) * time.Minute
	}
	now := time.Now()
	if g.Now != nil {
		now = g.Now()
	}
	var list finopsv1.NamespaceOptimizationList
	if err := g.Client.List(ctx, &list, client.InNamespace(config.OperatorNamespace())); err != nil {
		return err
	}
	log := logf.FromContext(ctx).WithName("rightsizing-guard")
	var errs []error
	for _, opt := range list.Items {
		ns := opt.Spec.TargetNamespace
		for _, w := range opt.Status.Workloads {
			if w.State != StateApplied || w.AppliedAt == nil {
				continue
			}
			if trouble := g.trouble(ctx, ns, w); trouble != "" {
				if _, err := Revert(ctx, g.Client, ns, w.Kind, w.Name, trouble); err != nil {
					errs = append(errs, fmt.Errorf("put back %s %s/%s: %w", w.Kind, ns, w.Name, err))
					continue
				}
				log.Info("Put back the requests of a right-sized workload", "namespace", ns, "workload", w.Name, "reason", trouble)
				if g.Notify != nil {
					msg := fmt.Sprintf("↩️ Put back the requests of %s `%s/%s`: %s after right-sizing.", w.Kind, ns, w.Name, trouble)
					_, _ = g.Notify.Send(ctx, "alerts", msg)
				}
				continue
			}
			if now.Sub(w.AppliedAt.Time) >= period {
				w.State = StateKept
				if err := Record(ctx, g.Client, ns, w); err != nil {
					errs = append(errs, fmt.Errorf("keep %s %s/%s: %w", w.Kind, ns, w.Name, err))
				}
			}
		}
	}
	return errors.Join(errs...)
}

// trouble says what went wrong with the pods a change started, if anything.
func (g *Guard) trouble(ctx context.Context, ns string, w finopsv1.WorkloadOptimization) string {
	obj, err := load(ctx, g.Client, ns, w.Kind, w.Name)
	if err != nil {
		return ""
	}
	_, _, selector, err := podSpec(obj)
	if err != nil || selector == nil {
		return ""
	}
	sel, err := metav1.LabelSelectorAsSelector(selector)
	if err != nil || sel.Empty() {
		return ""
	}
	var pods corev1.PodList
	if err := g.Client.List(ctx, &pods, client.InNamespace(ns), client.MatchingLabelsSelector{Selector: sel}); err != nil {
		return ""
	}
	for _, p := range pods.Items {
		if p.CreationTimestamp.Before(w.AppliedAt) {
			continue // a pod from before the change
		}
		for _, cs := range p.Status.ContainerStatuses {
			if t := cs.LastTerminationState.Terminated; t != nil && t.Reason == "OOMKilled" {
				return fmt.Sprintf("container %s of pod %s was OOMKilled", cs.Name, p.Name)
			}
			if wt := cs.State.Waiting; wt != nil && wt.Reason == "CrashLoopBackOff" {
				return fmt.Sprintf("container %s of pod %s is crash-looping", cs.Name, p.Name)
			}
			if cs.RestartCount > 0 {
				return fmt.Sprintf("container %s of pod %s restarted %d time(s)", cs.Name, p.Name, cs.RestartCount)
			}
		}
	}
	return ""
}
