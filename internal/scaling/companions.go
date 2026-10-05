package scaling

import (
	"context"
	"fmt"
	"sync"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// Companion objects that would undo a scale-down if left alone: CronJobs keep starting
// pods on their own schedule, and a KEDA ScaledObject scales its target straight back up.
// CostDeck suspends and pauses them while a namespace is down and marks what it touched,
// so it only resumes what it paused itself and never a CronJob a user suspended by hand.
const (
	// AnnotationSuspended marks a CronJob suspended by CostDeck.
	AnnotationSuspended = "costdeck.io/suspended"
	// AnnotationKEDAPaused marks a ScaledObject paused by CostDeck.
	AnnotationKEDAPaused = "costdeck.io/keda-paused"
	// kedaPausedReplicas makes KEDA scale the target to the given count and stop.
	kedaPausedReplicas = "autoscaling.keda.sh/paused-replicas"
	markerValue        = "true"
)

var scaledObjectGVK = schema.GroupVersionKind{Group: "keda.sh", Version: "v1alpha1", Kind: "ScaledObjectList"}

// kedaProbe remembers that the KEDA CRDs are absent, so clusters without KEDA do not pay
// for a failing list call on every reconcile.
type kedaProbe struct {
	mu        sync.Mutex
	missing   bool
	checkedAt time.Time
}

func (p *kedaProbe) skip() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.missing && time.Since(p.checkedAt) < 10*time.Minute
}

func (p *kedaProbe) record(missing bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.missing, p.checkedAt = missing, time.Now()
}

// +kubebuilder:rbac:groups=batch,resources=cronjobs,verbs=get;list;watch;patch
// +kubebuilder:rbac:groups=keda.sh,resources=scaledobjects,verbs=get;list;patch

// holdCompanions suspends CronJobs and pauses ScaledObjects before a namespace scales down.
func (e *Engine) holdCompanions(ctx context.Context, ns string, exclusions []string) error {
	if err := e.setCronJobsSuspended(ctx, ns, exclusions, true); err != nil {
		return err
	}
	return e.setScaledObjectsPaused(ctx, ns, exclusions, true)
}

// releaseCompanions resumes what holdCompanions paused, once the namespace is back up.
func (e *Engine) releaseCompanions(ctx context.Context, ns string, exclusions []string) error {
	if err := e.setScaledObjectsPaused(ctx, ns, exclusions, false); err != nil {
		return err
	}
	return e.setCronJobsSuspended(ctx, ns, exclusions, false)
}

func (e *Engine) setCronJobsSuspended(ctx context.Context, ns string, exclusions []string, suspend bool) error {
	var list batchv1.CronJobList
	if err := e.Client.List(ctx, &list, client.InNamespace(ns)); err != nil {
		return fmt.Errorf("list CronJobs in %s: %w", ns, err)
	}
	for i := range list.Items {
		cj := &list.Items[i]
		if isExcluded(cj.Name, exclusions) {
			continue
		}
		ours := cj.Annotations[AnnotationSuspended] == markerValue
		alreadySuspended := cj.Spec.Suspend != nil && *cj.Spec.Suspend
		switch {
		case suspend && !alreadySuspended:
		case !suspend && ours:
		default:
			continue
		}
		patch := client.MergeFrom(cj.DeepCopy())
		if suspend {
			cj.Spec.Suspend = new(true)
			if cj.Annotations == nil {
				cj.Annotations = map[string]string{}
			}
			cj.Annotations[AnnotationSuspended] = markerValue
		} else {
			cj.Spec.Suspend = new(false)
			delete(cj.Annotations, AnnotationSuspended)
		}
		if err := e.Client.Patch(ctx, cj, patch); err != nil {
			return fmt.Errorf("update CronJob %s/%s: %w", ns, cj.Name, err)
		}
		log.FromContext(ctx).Info("Updated CronJob", "namespace", ns, "name", cj.Name, "suspended", suspend)
	}
	return nil
}

func (e *Engine) setScaledObjectsPaused(ctx context.Context, ns string, exclusions []string, pause bool) error {
	if e.keda.skip() {
		return nil
	}
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(scaledObjectGVK)
	if err := e.Client.List(ctx, list, client.InNamespace(ns)); err != nil {
		if meta.IsNoMatchError(err) {
			e.keda.record(true)
			return nil
		}
		return fmt.Errorf("list ScaledObjects in %s: %w", ns, err)
	}
	e.keda.record(false)

	for i := range list.Items {
		so := &list.Items[i]
		annotations := so.GetAnnotations()
		ours := annotations[AnnotationKEDAPaused] == markerValue
		_, paused := annotations[kedaPausedReplicas]
		if isExcluded(so.GetName(), exclusions) || (pause && paused) || (!pause && !ours) {
			continue
		}
		patch := client.MergeFrom(so.DeepCopy())
		if annotations == nil {
			annotations = map[string]string{}
		}
		if pause {
			annotations[kedaPausedReplicas] = "0"
			annotations[AnnotationKEDAPaused] = markerValue
		} else {
			delete(annotations, kedaPausedReplicas)
			delete(annotations, AnnotationKEDAPaused)
		}
		so.SetAnnotations(annotations)
		if err := e.Client.Patch(ctx, so, patch); err != nil {
			return fmt.Errorf("update ScaledObject %s/%s: %w", ns, so.GetName(), err)
		}
		log.FromContext(ctx).Info("Updated ScaledObject", "namespace", ns, "name", so.GetName(), "paused", pause)
	}
	return nil
}
