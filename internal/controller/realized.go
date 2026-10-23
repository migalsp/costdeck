package controller

import (
	"context"
	"slices"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/finops"
	"github.com/migalsp/costdeck-operator/internal/pricing"
	"github.com/migalsp/costdeck-operator/internal/scaling"
)

// maxScaleDownNodes bounds status.scaleDownNodes (the CRD allows no more).
const maxScaleDownNodes = 200

// recordScaleDown remembers, as a scale-down begins, the nodes the workloads it stops run
// on and what they cost, for the realized-savings check; it forgets them once the target
// is up again. A target that was down already (an upgrade, a restart) gets no record: its
// pods are gone, so there is nothing left to compare.
func recordScaleDown(ctx context.Context, engine *scaling.Engine, resolver *pricing.Resolver, st *finopsv1.ScheduleStatus,
	namespaces []string, exclusionsOf func(string) []string, active, alreadyDown bool) {
	if active {
		st.ScaleDownNodes, st.ScaleDownAt = nil, nil
		return
	}
	if st.ScaleDownAt != nil || alreadyDown {
		return
	}
	var names []string
	for _, ns := range namespaces {
		nodes, err := engine.WorkloadNodes(ctx, ns, exclusionsOf(ns))
		if err != nil {
			logf.FromContext(ctx).Error(err, "Could not record the nodes of a scale-down", "namespace", ns)
			return // the next reconcile tries again, before anything has stopped
		}
		for _, n := range nodes {
			if !slices.Contains(names, n) {
				names = append(names, n)
			}
		}
	}
	slices.Sort(names)
	var rates pricing.Rates
	if resolver != nil {
		rates = resolver.Rates(ctx)
	}
	st.ScaleDownNodes = nil
	for _, name := range names[:min(len(names), maxScaleDownNodes)] {
		rec := finopsv1.ScaleDownNode{Name: name}
		var node corev1.Node
		if err := engine.Client.Get(ctx, types.NamespacedName{Name: name}, &node); err == nil {
			rec.HourlyCost = strconv.FormatFloat(finops.NodeHourlyCost(node, rates.CPUCoreHour, rates.MemoryGBHour), 'f', 4, 64)
		}
		st.ScaleDownNodes = append(st.ScaleDownNodes, rec)
	}
	now := metav1.Now()
	st.ScaleDownAt = &now
}
