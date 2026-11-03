package api

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/migalsp/costdeck-operator/internal/config"
	"github.com/migalsp/costdeck-operator/internal/finops"
	"github.com/migalsp/costdeck-operator/internal/risks"
)

const (
	riskAlertInterval = 30 * time.Minute
	riskRealertAfter  = 24 * time.Hour
	riskFiredKept     = 7 * 24 * time.Hour
	riskAlertLines    = 10
)

// RiskAlerter announces new critical performance risks and keeps the open ones in the
// costdeck-risk-alerts ConfigMap, which the digest lists. It runs on the leader.
type RiskAlerter struct {
	Server    *Server
	Poster    finops.Poster
	Namespace string
	Now       func() time.Time
}

// NeedLeaderElection implements manager.LeaderElectionRunnable.
func (a *RiskAlerter) NeedLeaderElection() bool { return true }

// Start implements manager.Runnable: it checks at once and then every half hour, logging a
// failed check instead of stopping.
func (a *RiskAlerter) Start(ctx context.Context) error {
	ticker := time.NewTicker(riskAlertInterval)
	defer ticker.Stop()
	for {
		if err := a.Check(ctx); err != nil {
			logf.FromContext(ctx).WithName("risk-alerts").Error(err, "Could not check performance risks")
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// Check assesses every namespace but the operator's, kube-* and terminating ones, and posts the
// critical risks not announced in the last day. A failed post leaves them unannounced for the next
// check; with no channel subscribed they are only recorded. A namespace that cannot be assessed
// keeps its open risks, with when they began, but none of them is announced again until they are
// seen. Turned off, it forgets the open risks.
func (a *RiskAlerter) Check(ctx context.Context) error {
	now := time.Now()
	if a.Now != nil {
		now = a.Now()
	}
	cfg, err := config.Get(ctx, a.Server.Client)
	if err != nil {
		return err
	}
	state, err := finops.LoadRiskState(ctx, a.Server.Client, a.Namespace)
	if err != nil {
		return err
	}
	if p := cfg.Spec.Alerts.PerformanceRisks; p == nil || !p.Enabled {
		if len(state.Open) == 0 {
			return nil
		}
		state.Open = map[string]finops.OpenRisk{}
		return finops.SaveRiskState(ctx, a.Server.Client, a.Namespace, state)
	}
	var namespaces corev1.NamespaceList
	if err := a.Server.Client.List(ctx, &namespaces); err != nil {
		return err
	}
	open := map[string]finops.OpenRisk{}
	unseen := map[string]bool{}
	for _, ns := range namespaces.Items {
		if ns.Name == a.Namespace || strings.HasPrefix(ns.Name, "kube-") || ns.Status.Phase == corev1.NamespaceTerminating {
			continue
		}
		report, err := a.Server.risks(ctx, ns.Name)
		if err != nil {
			logf.FromContext(ctx).WithName("risk-alerts").Error(err, "Could not assess performance risks; the namespace keeps its open risks", "namespace", ns.Name)
			keepUnseen(open, unseen, state.Open, ns.Name)
			continue
		}
		for _, r := range report.Risks {
			if r.Severity != risks.SeverityCritical {
				continue
			}
			o := finops.OpenRisk{Namespace: ns.Name, Kind: r.Kind, Workload: r.Workload, Container: r.Container, Code: r.Code, Message: r.Message, Fix: r.Fix, Since: now}
			if prev, seen := state.Open[o.Key()]; seen {
				o.Since = prev.Since
			}
			open[o.Key()] = o
		}
	}
	state.Open, state.CheckedAt = open, now
	fresh := announceable(state, unseen, now)
	var sendErr error
	if len(fresh) > 0 {
		delivered := ""
		if a.Poster != nil {
			delivered, sendErr = a.Poster.Post(ctx, riskAlertMarkdown(fresh))
			if errors.Is(sendErr, finops.ErrNoDestination) {
				sendErr = nil
			}
		}
		if sendErr == nil {
			events := make([]finops.AlertEvent, 0, len(fresh))
			for _, o := range fresh {
				state.Fired[o.Key()] = now
				events = append(events, finops.AlertEvent{At: now, Kind: finops.AlertRisk, Title: "Performance risk: " + o.Subject(), Detail: o.Message, Namespace: o.Namespace, Delivered: delivered})
			}
			if err := finops.AppendAlertEvents(ctx, a.Server.Client, a.Namespace, events); err != nil {
				return err
			}
		}
	}
	for key, fired := range state.Fired {
		if _, still := open[key]; !still && now.Sub(fired) > riskFiredKept {
			delete(state.Fired, key)
		}
	}
	if err := finops.SaveRiskState(ctx, a.Server.Client, a.Namespace, state); err != nil {
		return err
	}
	return sendErr
}

// keepUnseen copies the risks a namespace had open at the last check into open and marks them
// unseen, for a namespace that could not be assessed this time.
func keepUnseen(open map[string]finops.OpenRisk, unseen map[string]bool, last map[string]finops.OpenRisk, namespace string) {
	for key, prev := range last {
		if prev.Namespace == namespace {
			open[key], unseen[key] = prev, true
		}
	}
}

// announceable returns the open risks seen in this check that were never announced or not in the
// last day, in the order the alert lists them.
func announceable(state finops.RiskState, unseen map[string]bool, now time.Time) []finops.OpenRisk {
	var fresh []finops.OpenRisk
	for _, o := range state.Sorted() {
		if unseen[o.Key()] {
			continue
		}
		if fired, ok := state.Fired[o.Key()]; !ok || now.Sub(fired) >= riskRealertAfter {
			fresh = append(fresh, o)
		}
	}
	return fresh
}

// riskAlertMarkdown lists the new risks with their fixes, at most riskAlertLines of them, and
// points to the namespace pages for the rest.
func riskAlertMarkdown(fresh []finops.OpenRisk) string {
	var b strings.Builder
	fmt.Fprintf(&b, "**Performance risks: %d new**\n", len(fresh))
	for _, o := range fresh[:min(len(fresh), riskAlertLines)] {
		fmt.Fprintf(&b, "- %s: %s", o.Subject(), o.Message)
		if o.Fix != "" {
			fmt.Fprintf(&b, " Fix: %s", o.Fix)
		}
		b.WriteString("\n")
	}
	if more := len(fresh) - riskAlertLines; more > 0 {
		fmt.Fprintf(&b, "- and %d more on their namespace pages\n", more)
	}
	return b.String()
}
