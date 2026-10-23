package controller

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/migalsp/costdeck-operator/internal/config"
	"github.com/migalsp/costdeck-operator/internal/scaling"
)

// ConditionSleepPage says whether a schedule's routes show the sleep page.
const ConditionSleepPage = "SleepPage"

// SleepBackend is the Service serving the sleep page: the chart passes its name and port.
func SleepBackend() scaling.SleepBackend {
	name := os.Getenv("COSTDECK_SLEEP_PAGE_SERVICE")
	if name == "" {
		name = "costdeck-operator-api"
	}
	port, err := strconv.ParseInt(os.Getenv("COSTDECK_SLEEP_PAGE_PORT"), 10, 64)
	if err != nil || port <= 0 {
		port = 8082
	}
	return scaling.SleepBackend{Name: name, Namespace: config.OperatorNamespace(), Port: port}
}

// reconcileSleepPage points the namespaces' HTTPRoutes at the sleep page once the
// schedule has brought them all down, and back once they are all up again, so the page
// shows while nothing answers and never in the way of the application. Switching the
// option off wakes what it put to sleep.
func reconcileSleepPage(ctx context.Context, engine *scaling.Engine, conds *[]metav1.Condition, generation int64, namespaces []string, enabled, targetActive, settled bool) {
	log := logf.FromContext(ctx)
	page := SleepBackend()
	current := meta.FindStatusCondition(*conds, ConditionSleepPage)
	asleep := current != nil && current.Reason == "Asleep"
	set := func(status metav1.ConditionStatus, reason, msg string) {
		meta.SetStatusCondition(conds, metav1.Condition{Type: ConditionSleepPage, Status: status, Reason: reason, Message: msg, ObservedGeneration: generation})
	}
	fail := func(err error) {
		reason, msg := "Failed", err.Error()
		if apierrors.IsForbidden(err) {
			reason, msg = "Forbidden", "No permission to change HTTPRoutes or the ReferenceGrant; enable sleepPage.enabled in the Helm chart."
		}
		log.Error(err, "Could not update the sleep page routes")
		set(metav1.ConditionFalse, reason, msg)
	}
	wake := func() error {
		for _, ns := range namespaces {
			if _, err := engine.WakeRoutes(ctx, ns); err != nil {
				return err
			}
		}
		return engine.SyncSleepGrant(ctx, page)
	}

	switch {
	case !enabled:
		if asleep {
			if err := wake(); err != nil {
				fail(err)
				return
			}
		}
		meta.RemoveStatusCondition(conds, ConditionSleepPage)
	case !settled:
		// Mid-transition: the routes stay as they are until every namespace is there.
	case targetActive:
		if err := wake(); err != nil {
			fail(err)
			return
		}
		set(metav1.ConditionFalse, "Awake", "The routes lead to the applications.")
	default:
		// The grant first, so no route ever points at the page without being allowed to.
		if err := engine.SyncSleepGrant(ctx, page, namespaces...); err != nil {
			fail(err)
			return
		}
		total := 0
		for _, ns := range namespaces {
			n, err := engine.SleepRoutes(ctx, ns, page)
			if err != nil {
				fail(err)
				return
			}
			total += n
		}
		if total == 0 {
			_ = engine.SyncSleepGrant(ctx, page)
			set(metav1.ConditionFalse, "NoRoutes", fmt.Sprintf("No Gateway API HTTPRoute in %s to show the sleep page on.", strings.Join(namespaces, ", ")))
			return
		}
		set(metav1.ConditionTrue, "Asleep", fmt.Sprintf("%d HTTPRoute(s) in %s show the sleep page.", total, strings.Join(namespaces, ", ")))
	}
}
