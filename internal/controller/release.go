package controller

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/scaling"
)

const ReleaseFinalizer = "finops.costdeck.io/release"

const releaseGiveUpAfter = 10 * time.Minute

const releaseRetry = 15 * time.Second

// namespaceRules is what releasing a namespace needs from the schedule that held it: its
// workload rules, the replica counts it recorded, whether it scaled anything there and
// whether it is a dry run.
type namespaceRules struct {
	exclusions   []string
	sequence     []string
	hibernate    []finopsv1.HibernateRule
	originals    map[string]int32
	scaledByThem bool
	dryRun       bool
}

// releaseNamespace gives back a namespace its schedule lets go of: it starts what the schedule
// scaled down, services first and all stages in one pass without waiting, resumes GitOps and
// wakes the HTTPRoutes. A dry run that scaled nothing leaves the namespace as it is, and a
// Forbidden answer under the optional GitOps or sleep page RBAC is not an error.
func releaseNamespace(ctx context.Context, engine *scaling.Engine, ns string, rules namespaceRules) error {
	if rules.dryRun && !rules.scaledByThem {
		return nil
	}
	if rules.scaledByThem {
		if err := engine.ReleaseServices(ctx, ns, rules.hibernate); err != nil {
			return fmt.Errorf("start the hibernated services of %s: %w", ns, err)
		}
		if _, _, err := engine.ScaleTarget(ctx, ns, true, rules.sequence, rules.exclusions, rules.originals, true); err != nil {
			return fmt.Errorf("scale %s back up: %w", ns, err)
		}
	}
	if err := engine.ResumeGitOps(ctx, ns, rules.exclusions); err != nil && !apierrors.IsForbidden(err) {
		return fmt.Errorf("resume GitOps for %s: %w", ns, err)
	}
	if _, err := engine.WakeRoutes(ctx, ns); err != nil && !apierrors.IsForbidden(err) {
		return fmt.Errorf("wake the HTTPRoutes of %s: %w", ns, err)
	}
	return nil
}

// phaseReachedByScaling reports whether a phase shows that the schedule has scaled
// something: any phase but none yet, ScaledUp or DryRun, since a dry run never scales.
func phaseReachedByScaling(phase string) bool {
	return phase != "" && phase != scaling.PhaseScaledUp && phase != scaling.PhaseDryRun
}

// syncSleepGrantAfterRelease narrows the sleep page ReferenceGrant to the namespaces still
// asleep once others were woken, deleting it when none is. Forbidden is not an error.
func syncSleepGrantAfterRelease(ctx context.Context, engine *scaling.Engine) error {
	if err := engine.SyncSleepGrant(ctx, SleepBackend()); err != nil && !apierrors.IsForbidden(err) {
		return fmt.Errorf("update the sleep page ReferenceGrant: %w", err)
	}
	return nil
}

// giveUpOnRelease reports whether a deleted schedule has retried its release for
// releaseGiveUpAfter and may go without restoring everything.
func giveUpOnRelease(deletedAt time.Time, now time.Time) bool {
	return now.Sub(deletedAt) >= releaseGiveUpAfter
}
