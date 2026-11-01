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
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/config"
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
	// Requests counts the traffic idle auto-sleep looks at; optional.
	Requests  RequestCounter
	announced transitionLog
	idle      idleWatch
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
	if handled, result, err := r.handleDeletion(ctx, group); handled {
		return result, err
	}
	before := group.Status.DeepCopy()

	// 1.5 Namespaces the selector matches join the group; those that stopped matching
	// are released below, once the plan says whether another group took them over.
	departed, err := r.selectNamespaces(ctx, group)
	if err != nil {
		return ctrl.Result{}, err
	}
	departed = append(departed, removedNamespaces(group, departed)...)

	// 2. Determine the desired state against every group in the namespace, so that
	// dependsOn edges and namespace ownership are taken into account.
	plan, err := r.planFor(ctx, group)
	if err != nil {
		return ctrl.Result{}, err
	}
	r.release(ctx, group, departed, plan)
	decision := plan.Decision
	targetActive := decision.Active
	l.Info("Reconciling ScalingGroup", "category", group.Spec.Category, "namespaces", group.Members(), "targetActive", targetActive, "mode", decision.Mode)

	// Initialize status maps if nil
	if group.Status.OriginalReplicas == nil {
		group.Status.OriginalReplicas = make(map[string]int32)
	}
	group.Status.RequiredBy = plan.RequiredBy
	group.Status.ConflictingNamespaces = plan.ConflictingNamespaces
	setDependencyConditions(group, plan)
	setExceptionsCondition(&group.Status.Conditions, plan.ExceptionProblems, group.Generation)
	if group.Spec.DryRun {
		return r.dryRun(ctx, group, before, plan)
	}
	setDryRunCondition(&group.Status.Conditions, "", group.Generation)

	// 2.2 Idle auto-sleep may take the group down inside its schedule's hours.
	plan.Decision = r.idle.reconcile(ctx, r.Requests, &group.Status.ScheduleStatus, &group.Status.Conditions, r.idleCheck(ctx, group, plan), plan.Decision, time.Now())
	decision, targetActive = plan.Decision, plan.Decision.Active

	// 2.5 A group that wants to come up holds back until its dependencies are ScaledUp.
	// One that is already up keeps running even if a dependency briefly degrades.
	if plan.BlockedOnDependencies() && group.Status.Phase != scaling.PhaseScaledUp {
		return r.waitForDependencies(ctx, group, before, plan)
	}

	// 3. Define stages from group.Spec.Sequence, skipping namespaces an older group owns.
	stages := r.getScalingStages(group, targetActive, plan.ConflictingNamespaces)
	namespaces, exclusionsOf := r.gitOpsBeforeStages(ctx, group, stages, targetActive)
	recordScaleDown(ctx, r.Engine, r.Pricing, &group.Status.ScheduleStatus, namespaces, exclusionsOf, targetActive, group.Status.Phase == scaling.PhaseScaledDown)

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
	if targetActive && allReady {
		resumeGitOps(ctx, r.Engine, namespaces, exclusionsOf)
	}
	reconcileSleepPage(ctx, r.Engine, &group.Status.Conditions, group.Generation, namespaces, group.Spec.SleepPage, targetActive, allReady)
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
	for _, ns := range group.Members() {
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
	return r.Engine.PlanGroups(time.Now(), all.Items, calendars(ctx, r.Client))[group.Name], nil
}

// waitForDependencies records that the group is held back and checks again shortly.
func (r *ScalingGroupReconciler) waitForDependencies(ctx context.Context, group *finopsv1.ScalingGroup, before *finopsv1.ScalingGroupStatus, plan scaling.GroupPlan) (ctrl.Result, error) {
	waiting := append(append([]string{}, plan.WaitingFor...), plan.MissingDependencies...)
	msg := "Waiting for ScalingGroup(s) to be ScaledUp: " + strings.Join(waiting, ", ")
	if group.Status.Phase != scaling.PhaseWaitingForDependencies {
		trackWakeUp(&group.Status.ScheduleStatus, group.Status.Phase, scaling.PhaseWaitingForDependencies, time.Now())
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
	for _, ns := range group.Members() {
		if !slices.Contains(skip, ns) {
			managedNamespaces = append(managedNamespaces, ns)
		}
	}
	var stages [][]string

	if len(group.Spec.Sequence) > 0 {
		for _, s := range group.Spec.Sequence {
			var nsInStage []string
			for target := range strings.FieldsSeq(s) {
				if strings.HasPrefix(target, "ext:") || slices.Contains(managedNamespaces, target) {
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

// namespaceOriginals returns the replica counts the group recorded for a namespace, keyed
// as the engine keys them.
func namespaceOriginals(group *finopsv1.ScalingGroup, ns string) map[string]int32 {
	out := make(map[string]int32)
	for k, v := range group.Status.OriginalReplicas {
		if after, ok := strings.CutPrefix(k, ns+"/"); ok {
			out[after] = v
		}
	}
	return out
}

// workloadRules returns the exclusions, workload order and hibernate rules of a namespace's
// own ScalingConfig, which the group applies, plus the group's own hibernate rules. The
// exclusions include those the hibernate rules add.
func (r *ScalingGroupReconciler) workloadRules(ctx context.Context, group *finopsv1.ScalingGroup, ns string) (exclusions, sequence []string, hibernate []finopsv1.HibernateRule) {
	hibernate = group.Spec.Hibernate
	configList := &finopsv1.ScalingConfigList{}
	if err := r.List(ctx, configList, client.InNamespace(group.Namespace)); err == nil {
		for _, cfg := range configList.Items {
			if cfg.Spec.TargetNamespace == ns {
				exclusions, sequence = cfg.Spec.Exclusions, cfg.Spec.Sequence
				hibernate = append(slices.Clone(hibernate), cfg.Spec.Hibernate...)
				break
			}
		}
	}
	return scaling.ExclusionsWith(exclusions, hibernate), sequence, hibernate
}

// gitOpsBeforeStages pauses GitOps, when the group asks for it, before anything scales down,
// so it cannot race the scale-down, and reports it in the GitOps condition. It returns the
// namespaces the stages scale and how to find their workload rules, to resume GitOps once
// everything is up.
func (r *ScalingGroupReconciler) gitOpsBeforeStages(ctx context.Context, group *finopsv1.ScalingGroup, stages [][]string, targetActive bool) ([]string, func(string) []string) {
	var namespaces []string
	for _, stage := range stages {
		for _, ns := range stage {
			if !strings.HasPrefix(ns, "ext:") {
				namespaces = append(namespaces, ns)
			}
		}
	}
	exclusionsOf := func(ns string) []string { e, _, _ := r.workloadRules(ctx, group, ns); return e }
	reconcileGitOps(ctx, r.Engine, &group.Status.Conditions, group.Generation, namespaces, exclusionsOf, group.Spec.PauseGitOps, targetActive)
	return namespaces, exclusionsOf
}

// dryRun reports what the group would do now, without scaling or notifying anything.
func (r *ScalingGroupReconciler) dryRun(ctx context.Context, group *finopsv1.ScalingGroup, before *finopsv1.ScalingGroupStatus, plan scaling.GroupPlan) (ctrl.Result, error) {
	decision := plan.Decision
	applyDecision(&group.Status.ScheduleStatus, &group.Status.Conditions, decision, group.Generation, "group")
	workloads, namespaces, cloud := 0, 0, 0
	for _, stage := range r.getScalingStages(group, decision.Active, plan.ConflictingNamespaces) {
		for _, ns := range stage {
			if strings.HasPrefix(ns, "ext:") {
				cloud++
				continue
			}
			exclusions, _, hibernate := r.workloadRules(ctx, group, ns)
			n, err := wouldChange(ctx, r.Engine, ns, decision.Active, exclusions, hibernate)
			if err != nil {
				logf.FromContext(ctx).Error(err, "Could not work out the dry run", "namespace", ns)
				continue
			}
			if n > 0 {
				workloads += n
				namespaces++
			}
		}
	}
	var waiting []string
	if plan.BlockedOnDependencies() {
		waiting = append(append(waiting, plan.WaitingFor...), plan.MissingDependencies...)
	}
	group.Status.Phase, group.Status.CurrentStage, group.Status.SkippedNamespaces = scaling.PhaseDryRun, 0, nil
	setDryRunCondition(&group.Status.Conditions, dryRunMessage("group", decision.Active, workloads, namespaces, cloud, waiting), group.Generation)
	if err := updateStatus(ctx, r.Client, group, before, &group.Status); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: settledRequeue(decision, time.Now())}, nil
}

func (r *ScalingGroupReconciler) reconcileK8sTarget(ctx context.Context, group *finopsv1.ScalingGroup, ns string, targetActive bool, timeoutPassed bool) (bool, error) {
	exclusions, nsSequence, hibernate := r.workloadRules(ctx, group, ns)

	nsKeyPrefix := ns + "/"
	nsReplicas := namespaceOriginals(group, ns)

	updatedOriginals, nsReady, err := r.Engine.ScaleWithServices(ctx, ns, targetActive, nsSequence, exclusions, hibernate, nsReplicas, timeoutPassed)
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

// idleCheck describes the group for idle auto-sleep: its own namespaces, how long they
// have been up and whether dependents need them.
func (r *ScalingGroupReconciler) idleCheck(ctx context.Context, group *finopsv1.ScalingGroup, plan scaling.GroupPlan) idleCheck {
	var namespaces []string
	for _, ns := range group.Members() {
		if !slices.Contains(plan.ConflictingNamespaces, ns) {
			namespaces = append(namespaces, ns)
		}
	}
	return idleCheck{
		UID: group.UID, Generation: group.Generation, IdleAfter: group.Spec.IdleAfter, Namespaces: namespaces,
		Phase: group.Status.Phase, UpSince: group.Status.LastAction.Time, Required: len(plan.RequiredBy) > 0,
		At: func(t time.Time) scaling.Decision {
			exceptions, _ := scaling.ResolveExceptions(group.Spec.Exceptions, group.Spec.Calendars, calendars(ctx, r.Client), scaling.ExceptionZone(group.Spec.Schedules))
			tt := scaling.Timetable{Schedules: group.Spec.Schedules, Exceptions: exceptions, Lead: scaling.PrewarmLead(group.Spec.Prewarm, group.Status.RecentWakeUps)}
			return r.Engine.Decide(t, tt, group.Spec.Active, group.Spec.ActiveUntil, group.Annotations)
		},
	}
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
		trackWakeUp(&group.Status.ScheduleStatus, oldPhase, newPhase, time.Now())
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
	r.announced.announce(ctx, r.Notifier, group.UID, "Group", group.Name, oldPhase, newPhase, group.Status.ScheduleStatus, group.Status.RequiredBy)
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
		// A changed holiday calendar applies at once to the groups that use one.
		Watches(&finopsv1.CostDeckConfig{}, handler.EnqueueRequestsFromMapFunc(r.groupsWithCalendars),
			builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		// A namespace created, relabelled or deleted can join or leave a selector's matches.
		Watches(&corev1.Namespace{}, handler.EnqueueRequestsFromMapFunc(r.groupsWithSelectors)).
		Named("scalinggroup").
		Complete(r)
}

// selectNamespaces records in status.selectedNamespaces what the group's selector matches
// now, and returns the namespaces that matched before but no longer do (and are not
// listed by name either).
func (r *ScalingGroupReconciler) selectNamespaces(ctx context.Context, group *finopsv1.ScalingGroup) ([]string, error) {
	previous := group.Status.SelectedNamespaces
	if group.Spec.NamespaceSelector == nil && len(previous) == 0 {
		return nil, nil
	}
	var selected []string
	if group.Spec.NamespaceSelector != nil {
		var namespaces corev1.NamespaceList
		if err := r.List(ctx, &namespaces); err != nil {
			return nil, err
		}
		var err error
		selected, err = scaling.SelectNamespaces(group.Spec.NamespaceSelector, namespaces.Items, group.Namespace)
		if err != nil {
			// The API refuses an invalid selector; one written past it selects nothing.
			r.Recorder.Eventf(group, nil, corev1.EventTypeWarning, "InvalidSelector", "Select", "%v", err)
			selected = nil
		}
	}
	group.Status.SelectedNamespaces = selected
	var departed []string
	for _, ns := range previous {
		if !slices.Contains(selected, ns) && !slices.Contains(group.Spec.Namespaces, ns) {
			departed = append(departed, ns)
		}
	}
	return departed, nil
}

// release brings back what the group scaled down in namespaces that left it, unless
// another group now owns them, and forgets their replica counts.
func (r *ScalingGroupReconciler) release(ctx context.Context, group *finopsv1.ScalingGroup, departed []string, plan scaling.GroupPlan) {
	released := false
	for _, ns := range departed {
		if owner, ok := plan.NamespaceOwners[ns]; ok && owner != group.Name {
			continue
		}
		if err := releaseNamespace(ctx, r.Engine, ns, r.rulesFor(ctx, group, ns)); err != nil {
			logf.FromContext(ctx).Error(err, "Could not restore a namespace that left the group", "namespace", ns)
			continue
		}
		released = true
		for k := range group.Status.OriginalReplicas {
			if strings.HasPrefix(k, ns+"/") {
				delete(group.Status.OriginalReplicas, k)
			}
		}
		r.Recorder.Eventf(group, nil, corev1.EventTypeNormal, "NamespaceReleased", "Release",
			"Namespace %s left the group; restored what the group had scaled down", ns)
	}
	if released {
		if err := syncSleepGrantAfterRelease(ctx, r.Engine); err != nil {
			logf.FromContext(ctx).Error(err, "Could not update the sleep page grant after a namespace left the group")
		}
	}
}

func (r *ScalingGroupReconciler) rulesFor(ctx context.Context, group *finopsv1.ScalingGroup, ns string) namespaceRules {
	exclusions, sequence, hibernate := r.workloadRules(ctx, group, ns)
	originals := namespaceOriginals(group, ns)
	return namespaceRules{
		exclusions: exclusions, sequence: sequence, hibernate: hibernate, originals: originals,
		scaledByThem: group.Status.Phase != scaling.PhaseScaledUp || len(originals) > 0,
	}
}

func removedNamespaces(group *finopsv1.ScalingGroup, already []string) []string {
	members := group.Members()
	var out []string
	for k := range group.Status.OriginalReplicas {
		ns, _, found := strings.Cut(k, "/")
		if !found || slices.Contains(members, ns) || slices.Contains(already, ns) || slices.Contains(out, ns) {
			continue
		}
		out = append(out, ns)
	}
	slices.Sort(out)
	return out
}

func (r *ScalingGroupReconciler) handleDeletion(ctx context.Context, group *finopsv1.ScalingGroup) (bool, ctrl.Result, error) {
	if !group.DeletionTimestamp.IsZero() {
		result, err := r.finalize(ctx, group)
		return true, result, err
	}
	if controllerutil.AddFinalizer(group, ReleaseFinalizer) {
		if err := r.Update(ctx, group); err != nil {
			return true, ctrl.Result{}, err
		}
	}
	return false, ctrl.Result{}, nil
}

func (r *ScalingGroupReconciler) finalize(ctx context.Context, group *finopsv1.ScalingGroup) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(group, ReleaseFinalizer) {
		return ctrl.Result{}, nil
	}
	if err := r.releaseEverything(ctx, group); err != nil {
		if !giveUpOnRelease(group.DeletionTimestamp.Time, time.Now()) {
			r.Recorder.Eventf(group, nil, corev1.EventTypeWarning, "ReleaseFailed", "Release",
				"Could not restore everything the group scaled down yet, retrying: %v", err)
			return ctrl.Result{RequeueAfter: releaseRetry}, nil
		}
		r.Recorder.Eventf(group, nil, corev1.EventTypeWarning, "ReleaseAbandoned", "Release",
			"Deleted after %s without restoring everything the group scaled down: %v", releaseGiveUpAfter, err)
	}
	controllerutil.RemoveFinalizer(group, ReleaseFinalizer)
	if err := r.Update(ctx, group); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	telemetry.ForgetScaling("ScalingGroup", group.Name)
	return ctrl.Result{}, nil
}

func (r *ScalingGroupReconciler) releaseEverything(ctx context.Context, group *finopsv1.ScalingGroup) error {
	plan, err := r.planFor(ctx, group)
	if err != nil {
		return err
	}
	var errs []error
	for _, ns := range group.Members() {
		if owner, ok := plan.NamespaceOwners[ns]; ok && owner != group.Name {
			continue
		}
		if err := releaseNamespace(ctx, r.Engine, ns, r.rulesFor(ctx, group, ns)); err != nil {
			errs = append(errs, err)
		}
	}
	if group.Status.Phase != scaling.PhaseScaledUp {
		for _, target := range group.Spec.ExternalTargets {
			if _, err := r.reconcileExternalTarget(ctx, group, "ext:"+target.Identifier, true); err != nil {
				errs = append(errs, fmt.Errorf("start %s: %w", target.Identifier, err))
			}
		}
	}
	if err := syncSleepGrantAfterRelease(ctx, r.Engine); err != nil {
		errs = append(errs, err)
	}
	return utilerrors.NewAggregate(errs)
}

// groupsWithSelectors maps a Namespace event to the groups that select namespaces.
func (r *ScalingGroupReconciler) groupsWithSelectors(ctx context.Context, _ client.Object) []reconcile.Request {
	var groups finopsv1.ScalingGroupList
	if err := r.List(ctx, &groups, client.InNamespace(config.OperatorNamespace())); err != nil {
		return nil
	}
	var out []reconcile.Request
	for _, g := range groups.Items {
		if g.Spec.NamespaceSelector != nil || len(g.Status.SelectedNamespaces) > 0 {
			out = append(out, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&g)})
		}
	}
	return out
}

// groupsWithCalendars maps a CostDeckConfig event to the groups that name a calendar.
func (r *ScalingGroupReconciler) groupsWithCalendars(ctx context.Context, obj client.Object) []reconcile.Request {
	var groups finopsv1.ScalingGroupList
	if err := r.List(ctx, &groups, client.InNamespace(obj.GetNamespace())); err != nil {
		return nil
	}
	var out []reconcile.Request
	for _, g := range groups.Items {
		if len(g.Spec.Calendars) > 0 {
			out = append(out, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&g)})
		}
	}
	return out
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
		for _, ns := range g.Members() {
			related = related || slices.Contains(changed.Members(), ns)
		}
		if related {
			reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&g)})
		}
	}
	return reqs
}
