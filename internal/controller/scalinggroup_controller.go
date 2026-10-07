/*
Copyright 2026 migalsp.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/pricing"
	"github.com/migalsp/costdeck-operator/internal/scaling"
	"github.com/migalsp/costdeck-operator/internal/telemetry"
)

// ScalingGroupReconciler scales a set of namespaces (and external targets) as one unit.
type ScalingGroupReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Engine   *scaling.Engine
	Recorder events.EventRecorder
	// Pricing values the savings of kept-down workloads; optional.
	Pricing *pricing.Resolver
	// Notifier announces finished transitions; optional.
	Notifier Notifier
}

// +kubebuilder:rbac:groups=finops.costdeck.io,namespace=costdeck,resources=scalinggroups,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=finops.costdeck.io,namespace=costdeck,resources=scalinggroups/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=finops.costdeck.io,namespace=costdeck,resources=scalinggroups/finalizers,verbs=update
// +kubebuilder:rbac:groups="",namespace=costdeck,resources=events,verbs=create;patch;get;list;watch
// +kubebuilder:rbac:groups=events.k8s.io,namespace=costdeck,resources=events,verbs=create;patch

func (r *ScalingGroupReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	l := logf.FromContext(ctx)

	// 1. Fetch the ScalingGroup
	group := &finopsv1.ScalingGroup{}
	if err := r.Get(ctx, req.NamespacedName, group); err != nil {
		if errors.IsNotFound(err) {
			telemetry.ForgetScaling("ScalingGroup", req.Name)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	before := group.Status.DeepCopy()

	// 2. Determine the desired state against every group in the namespace, so that
	// dependsOn edges and namespace ownership are taken into account.
	plan, err := r.planFor(ctx, group)
	if err != nil {
		return ctrl.Result{}, err
	}
	decision := plan.Decision
	targetActive := decision.Active
	l.Info("Reconciling ScalingGroup", "category", group.Spec.Category, "namespaces", group.Spec.Namespaces, "targetActive", targetActive, "mode", decision.Mode)

	// Initialize status maps if nil
	if group.Status.OriginalReplicas == nil {
		group.Status.OriginalReplicas = make(map[string]int32)
	}
	group.Status.RequiredBy = plan.RequiredBy
	group.Status.ConflictingNamespaces = plan.ConflictingNamespaces
	setDependencyConditions(group, plan)

	// 2.5 A group that wants to come up holds back until its dependencies are ScaledUp.
	// One that is already up keeps running even if a dependency briefly degrades.
	if plan.BlockedOnDependencies() && group.Status.Phase != scaling.PhaseScaledUp {
		return r.waitForDependencies(ctx, group, before, plan)
	}

	// 3. Define stages from group.Spec.Sequence, skipping namespaces an older group owns.
	stages := r.getScalingStages(group, targetActive, plan.ConflictingNamespaces)

	// A transition starts whenever the group is not already moving towards the target
	// state. It begins at the first stage; while it runs, status.currentStage and
	// status.lastAction record the stage being waited on and when it started.
	inProgress := scaling.PhaseScalingDown
	if targetActive {
		inProgress = scaling.PhaseScalingUp
	}
	currentStage, stageStarted := group.Status.CurrentStage, group.Status.LastAction.Time
	if group.Status.Phase != inProgress {
		currentStage, stageStarted = 0, time.Now()
	}
	skipOnTimeout := group.Spec.FeatureFlags != nil && group.Spec.FeatureFlags.SkipOnTimeout
	stageTimeout := time.Duration(stageTimeoutMinutes(group)) * time.Minute

	allReady := true
	managedCount := 0
	namespacesReady := 0
	namespacesTotal := 0
	for _, stage := range stages {
		namespacesTotal += len(stage)
	}

	var blockingNamespaces, readyNamespaces, skippedNamespaces []string

	// 4. Iterate over stages. A stage starts once the one before it is at the target
	// state or, with skipOnTimeout, has used up its time to get there.
	for i, stage := range stages {
		l.Info("Processing scaling stage", "stageIndex", i, "namespaces", stage)
		// Stages before currentStage were left behind in an earlier reconcile: their
		// stragglers are still reconciled but no longer hold back the stages after them.
		passed := i < currentStage
		timedOut := passed || (skipOnTimeout && time.Since(stageStarted) > stageTimeout)

		var waiting []string
		for _, ns := range stage {
			managedCount++

			isReady, err := r.reconcileTarget(ctx, group, ns, targetActive, timedOut)
			if err != nil {
				l.Error(err, "failed to reconcile target", "target", ns)
			}
			if err == nil && isReady {
				namespacesReady++
				readyNamespaces = append(readyNamespaces, ns)
				continue
			}
			allReady = false
			blockingNamespaces = append(blockingNamespaces, ns)
			waiting = append(waiting, ns)
		}

		if len(waiting) > 0 {
			if !timedOut {
				l.Info("Stage not ready, waiting before next stage", "stageIndex", i)
				r.Recorder.Eventf(group, nil, corev1.EventTypeNormal, "ScalingActive", "Scale",
					"Executing Stage %d. Waiting for targets in: %s", i+1, strings.Join(waiting, ", "))
				break
			}
			skippedNamespaces = append(skippedNamespaces, waiting...)
			if !passed {
				l.Info("Stage not ready within its timeout, moving on", "stageIndex", i, "skipped", waiting)
				r.Recorder.Eventf(group, nil, corev1.EventTypeWarning, "ScalingTimeout", "Scale",
					"Stage %d not ready after %d min, moving on without: %s", i+1, stageTimeoutMinutes(group), strings.Join(waiting, ", "))
			}
		}
		if !passed {
			currentStage, stageStarted = i+1, time.Now()
		}
	}
	// A settled group keeps no stage state, so its status stays unchanged between reconciles.
	group.Status.CurrentStage, group.Status.SkippedNamespaces = 0, skippedNamespaces
	if !allReady {
		group.Status.CurrentStage = currentStage
		group.Status.LastAction = metav1.NewTime(stageStarted)
	}

	if namespacesReady > group.Status.NamespacesReady {
		r.Recorder.Eventf(group, nil, corev1.EventTypeNormal, "ScalingProgress", "Scale", "Progress updated: %d of %d targets reached target state.", namespacesReady, namespacesTotal)
	}

	// 5. Update Status
	applyDecision(&group.Status.ScheduleStatus, &group.Status.Conditions, decision, group.Generation, "group")
	r.recordGroupSavings(ctx, group, plan.ConflictingNamespaces)
	detail := ""
	if len(blockingNamespaces) > 0 {
		detail = "Waiting for: " + strings.Join(blockingNamespaces, ", ")
	}
	return r.updateStatusAndPhase(ctx, group, before, allReady, managedCount, namespacesReady, namespacesTotal, readyNamespaces, decision, detail)
}

// recordGroupSavings estimates what the group's kept-down workloads would cost per hour.
func (r *ScalingGroupReconciler) recordGroupSavings(ctx context.Context, group *finopsv1.ScalingGroup, skip []string) {
	var cpu, mem resource.Quantity
	for _, ns := range group.Spec.Namespaces {
		if slices.Contains(skip, ns) {
			continue
		}
		prefix := ns + "/"
		originals := map[string]int32{}
		for k, v := range group.Status.OriginalReplicas {
			if rest, ok := strings.CutPrefix(k, prefix); ok {
				originals[rest] = v
			}
		}
		c, m, err := r.Engine.KeptDown(ctx, ns, originals)
		if err != nil {
			continue
		}
		cpu.Add(c)
		mem.Add(m)
	}
	recordSavings(ctx, &group.Status.ScheduleStatus, r.Pricing, cpu, mem)
}

// stageTimeoutMinutes returns the configured skip-on-timeout window, defaulting to five.
func stageTimeoutMinutes(group *finopsv1.ScalingGroup) int {
	if group.Spec.FeatureFlags != nil && group.Spec.FeatureFlags.TimeoutMinutes > 0 {
		return group.Spec.FeatureFlags.TimeoutMinutes
	}
	return 5
}

// planFor resolves this group within the dependency graph of its namespace.
func (r *ScalingGroupReconciler) planFor(ctx context.Context, group *finopsv1.ScalingGroup) (scaling.GroupPlan, error) {
	var all finopsv1.ScalingGroupList
	if err := r.List(ctx, &all, client.InNamespace(group.Namespace)); err != nil {
		return scaling.GroupPlan{}, err
	}
	// Plan with the freshly read object; the cache may lag behind or miss a new group,
	// and a missing entry would read as "desired down".
	found := false
	for i := range all.Items {
		if all.Items[i].Name == group.Name {
			all.Items[i] = *group.DeepCopy()
			found = true
		}
	}
	if !found {
		all.Items = append(all.Items, *group.DeepCopy())
	}
	return r.Engine.PlanGroups(time.Now(), all.Items)[group.Name], nil
}

// waitForDependencies records that the group is held back and checks again shortly.
func (r *ScalingGroupReconciler) waitForDependencies(ctx context.Context, group *finopsv1.ScalingGroup, before *finopsv1.ScalingGroupStatus, plan scaling.GroupPlan) (ctrl.Result, error) {
	waiting := append(append([]string{}, plan.WaitingFor...), plan.MissingDependencies...)
	msg := "Waiting for ScalingGroup(s) to be ScaledUp: " + strings.Join(waiting, ", ")
	if group.Status.Phase != scaling.PhaseWaitingForDependencies {
		group.Status.Phase = scaling.PhaseWaitingForDependencies
		group.Status.LastAction = metav1.Now()
		r.Recorder.Eventf(group, nil, corev1.EventTypeNormal, "WaitingForDependencies", "WaitForDependencies", "%s", msg)
	}
	applyDecision(&group.Status.ScheduleStatus, &group.Status.Conditions, plan.Decision, group.Generation, "group")
	setReadyCondition(&group.Status.Conditions, group.Status.Phase, true, group.Generation, msg)
	if err := updateStatus(ctx, r.Client, group, before, &group.Status); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
}

// setDependencyConditions reports dependency and namespace-ownership problems.
func setDependencyConditions(group *finopsv1.ScalingGroup, plan scaling.GroupPlan) {
	conds := &group.Status.Conditions
	gen := group.Generation

	if len(group.Spec.DependsOn) == 0 {
		meta.RemoveStatusCondition(conds, ConditionDependenciesReady)
	} else {
		cond := metav1.Condition{Type: ConditionDependenciesReady, ObservedGeneration: gen}
		switch {
		case plan.InCycle:
			cond.Status, cond.Reason = metav1.ConditionFalse, "Cycle"
			cond.Message = "dependsOn forms a cycle and is ignored: " + strings.Join(group.Spec.DependsOn, ", ")
		case len(plan.MissingDependencies) > 0:
			cond.Status, cond.Reason = metav1.ConditionFalse, "NotFound"
			cond.Message = "No such ScalingGroup: " + strings.Join(plan.MissingDependencies, ", ")
		case len(plan.WaitingFor) > 0:
			cond.Status, cond.Reason = metav1.ConditionFalse, "NotScaledUp"
			cond.Message = "Not ScaledUp yet: " + strings.Join(plan.WaitingFor, ", ")
		default:
			cond.Status, cond.Reason = metav1.ConditionTrue, "ScaledUp"
			cond.Message = "All dependencies are ScaledUp: " + strings.Join(group.Spec.DependsOn, ", ")
		}
		meta.SetStatusCondition(conds, cond)
	}

	if len(plan.ConflictingNamespaces) == 0 {
		meta.RemoveStatusCondition(conds, ConditionNamespaceConflict)
		return
	}
	parts := make([]string, 0, len(plan.ConflictingNamespaces))
	for _, ns := range plan.ConflictingNamespaces {
		parts = append(parts, fmt.Sprintf("%s (owned by %s)", ns, plan.NamespaceOwners[ns]))
	}
	meta.SetStatusCondition(conds, metav1.Condition{
		Type: ConditionNamespaceConflict, Status: metav1.ConditionTrue, Reason: "OwnedByOlderGroup",
		ObservedGeneration: gen,
		Message:            "Skipping namespaces already managed by another ScalingGroup: " + strings.Join(parts, ", "),
	})
}

func (r *ScalingGroupReconciler) getScalingStages(group *finopsv1.ScalingGroup, targetActive bool, skip []string) [][]string {
	var managedNamespaces []string
	for _, ns := range group.Spec.Namespaces {
		if !slices.Contains(skip, ns) {
			managedNamespaces = append(managedNamespaces, ns)
		}
	}
	var stages [][]string

	if len(group.Spec.Sequence) > 0 {
		for _, s := range group.Spec.Sequence {
			var nsInStage []string
			for target := range strings.FieldsSeq(s) {
				if !slices.Contains(skip, target) {
					nsInStage = append(nsInStage, target)
				}
			}
			if len(nsInStage) > 0 {
				stages = append(stages, nsInStage)
			}
		}
		// Add namespaces not mentioned in sequence as the last stage
		var missing []string
		for _, ns := range managedNamespaces {
			found := false
			for _, stage := range stages {
				if slices.Contains(stage, ns) {
					found = true
				}
				if found {
					break
				}
			}
			if !found {
				missing = append(missing, ns)
			}
		}
		if len(missing) > 0 {
			stages = append(stages, missing)
		}
	} else {
		stages = append(stages, managedNamespaces)
	}

	if !targetActive {
		for i, j := 0, len(stages)-1; i < j; i, j = i+1, j-1 {
			stages[i], stages[j] = stages[j], stages[i]
		}
	}
	return stages
}

func (r *ScalingGroupReconciler) reconcileTarget(ctx context.Context, group *finopsv1.ScalingGroup, ns string, targetActive bool, timeoutPassed bool) (bool, error) {
	if strings.HasPrefix(ns, "ext:") {
		return r.reconcileExternalTarget(ctx, group, ns, targetActive)
	}
	return r.reconcileK8sTarget(ctx, group, ns, targetActive, timeoutPassed)
}

func (r *ScalingGroupReconciler) reconcileExternalTarget(ctx context.Context, group *finopsv1.ScalingGroup, ns string, targetActive bool) (bool, error) {
	extId := strings.TrimPrefix(ns, "ext:")
	var extTarget *finopsv1.ExternalTarget
	for i := range group.Spec.ExternalTargets {
		if group.Spec.ExternalTargets[i].Identifier == extId {
			extTarget = &group.Spec.ExternalTargets[i]
			break
		}
	}

	if extTarget == nil {
		return false, fmt.Errorf("external target %s not found in spec", extId)
	}

	provider, err := r.Engine.Provider(ctx, extTarget.Provider)
	if err != nil {
		return false, err
	}

	if err := provider.Scale(ctx, *extTarget, targetActive); err != nil {
		return false, err
	}

	return provider.IsReady(ctx, *extTarget, targetActive)
}

func (r *ScalingGroupReconciler) reconcileK8sTarget(ctx context.Context, group *finopsv1.ScalingGroup, ns string, targetActive bool, timeoutPassed bool) (bool, error) {
	var exclusions []string
	var nsSequence []string

	configList := &finopsv1.ScalingConfigList{}
	if err := r.List(ctx, configList, client.InNamespace(group.Namespace)); err == nil {
		for _, cfg := range configList.Items {
			if cfg.Spec.TargetNamespace == ns {
				exclusions = cfg.Spec.Exclusions
				nsSequence = cfg.Spec.Sequence
				break
			}
		}
	}

	nsKeyPrefix := ns + "/"
	nsReplicas := make(map[string]int32)
	for k, v := range group.Status.OriginalReplicas {
		if after, ok := strings.CutPrefix(k, nsKeyPrefix); ok {
			nsReplicas[after] = v
		}
	}

	updatedOriginals, nsReady, err := r.Engine.ScaleTarget(ctx, ns, targetActive, nsSequence, exclusions, nsReplicas, timeoutPassed)
	if err != nil {
		return false, err
	}

	// Merge back
	for k := range group.Status.OriginalReplicas {
		if strings.HasPrefix(k, nsKeyPrefix) {
			delete(group.Status.OriginalReplicas, k)
		}
	}
	for k, v := range updatedOriginals {
		group.Status.OriginalReplicas[nsKeyPrefix+k] = v
	}

	if !nsReady {
		return false, nil
	}

	phase := r.Engine.ComputePhase(ctx, ns, targetActive, exclusions)
	return (targetActive && phase == scaling.PhaseScaledUp) || (!targetActive && phase == scaling.PhaseScaledDown), nil
}

func (r *ScalingGroupReconciler) updateStatusAndPhase(ctx context.Context, group *finopsv1.ScalingGroup, before *finopsv1.ScalingGroupStatus, allReady bool, managedCount, namespacesReady, namespacesTotal int, readyNamespaces []string, decision scaling.Decision, detail string) (ctrl.Result, error) {
	targetActive := decision.Active
	group.Status.ManagedCount = managedCount
	group.Status.NamespacesReady = namespacesReady
	group.Status.NamespacesTotal = namespacesTotal
	group.Status.ReadyNamespaces = readyNamespaces

	newPhase := scaling.PhaseScalingUp
	if allReady {
		if targetActive {
			newPhase = scaling.PhaseScaledUp
		} else {
			newPhase = scaling.PhaseScaledDown
		}
	} else if !targetActive {
		newPhase = scaling.PhaseScalingDown
	}

	oldPhase := group.Status.Phase
	if group.Status.Phase != newPhase {
		group.Status.Phase = newPhase
		group.Status.LastAction = metav1.Now()
		if oldPhase == "" {
			r.Recorder.Eventf(group, nil, corev1.EventTypeNormal, "PhaseTransition", "UpdatePhase", "Group phase is %s", newPhase)
		} else {
			r.Recorder.Eventf(group, nil, corev1.EventTypeNormal, "PhaseTransition", "UpdatePhase", "Group phase transitioned from %s to %s", oldPhase, newPhase)
		}
	} else if group.Status.LastAction.IsZero() {
		group.Status.LastAction = metav1.Now()
	}

	setReadyCondition(&group.Status.Conditions, newPhase, targetActive, group.Generation, detail)

	if err := updateStatus(ctx, r.Client, group, before, &group.Status); err != nil {
		return ctrl.Result{}, err
	}
	announceTransition(ctx, r.Notifier, "Group", group.Name, oldPhase, newPhase, group.Status.ScheduleStatus, group.Status.RequiredBy)
	savings, _ := strconv.ParseFloat(group.Status.EstimatedHourlySavings, 64)
	telemetry.RecordScaling("ScalingGroup", group.Name, targetActive, allReady,
		decision.Mode == scaling.ModeManualUp || decision.Mode == scaling.ModeManualDown, savings, group.Status.Currency)

	if !allReady {
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}
	return ctrl.Result{RequeueAfter: settledRequeue(decision, time.Now())}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ScalingGroupReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.Engine == nil {
		r.Engine = &scaling.Engine{Client: r.Client}
	}
	if r.Engine.Providers == nil {
		r.Engine.Providers = make(map[string]scaling.ExternalProvider)
	}

	// External providers are built from the live CostDeckConfig, so the credentials saved
	// in the settings page are the ones used to start and stop cloud resources.
	if r.Engine.Resolver == nil {
		r.Engine.Resolver = &scaling.ConfigProviderResolver{Client: mgr.GetClient()}
	}

	r.Recorder = mgr.GetEventRecorder("scalinggroup-controller")

	return ctrl.NewControllerManagedBy(mgr).
		For(&finopsv1.ScalingGroup{}).
		// A group's desired state depends on its dependents' and dependencies' state, and
		// namespace ownership on the other groups listing the same namespace.
		Watches(&finopsv1.ScalingGroup{}, handler.EnqueueRequestsFromMapFunc(r.relatedGroups)).
		Named("scalinggroup").
		Complete(r)
}

// relatedGroups maps a ScalingGroup event to the groups whose plan it can change: its
// dependencies, its dependents, and groups sharing one of its namespaces.
func (r *ScalingGroupReconciler) relatedGroups(ctx context.Context, obj client.Object) []reconcile.Request {
	changed, ok := obj.(*finopsv1.ScalingGroup)
	if !ok {
		return nil
	}
	var all finopsv1.ScalingGroupList
	if err := r.List(ctx, &all, client.InNamespace(changed.Namespace)); err != nil {
		return nil
	}
	var reqs []reconcile.Request
	for _, g := range all.Items {
		if g.Name == changed.Name {
			continue
		}
		related := slices.Contains(changed.Spec.DependsOn, g.Name) || slices.Contains(g.Spec.DependsOn, changed.Name)
		for _, ns := range g.Spec.Namespaces {
			related = related || slices.Contains(changed.Spec.Namespaces, ns)
		}
		if related {
			reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&g)})
		}
	}
	return reqs
}
