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

type namespaceRules struct {
	exclusions   []string
	sequence     []string
	hibernate    []finopsv1.HibernateRule
	originals    map[string]int32
	scaledByThem bool
}

func releaseNamespace(ctx context.Context, engine *scaling.Engine, ns string, rules namespaceRules) error {
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

func syncSleepGrantAfterRelease(ctx context.Context, engine *scaling.Engine) error {
	if err := engine.SyncSleepGrant(ctx, SleepBackend()); err != nil && !apierrors.IsForbidden(err) {
		return fmt.Errorf("update the sleep page ReferenceGrant: %w", err)
	}
	return nil
}

func giveUpOnRelease(deletedAt time.Time, now time.Time) bool {
	return now.Sub(deletedAt) >= releaseGiveUpAfter
}
