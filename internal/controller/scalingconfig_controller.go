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
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/pricing"
	"github.com/migalsp/costdeck-operator/internal/scaling"
	"github.com/migalsp/costdeck-operator/internal/telemetry"
)

// PhaseOverriddenByGroup marks a ScalingConfig whose namespace belongs to a ScalingGroup.
const PhaseOverriddenByGroup = "OverriddenByGroup"

// ScalingConfigReconciler reconciles a ScalingConfig object
type ScalingConfigReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	Engine *scaling.Engine
	// Pricing values the savings of kept-down workloads; optional.
	Pricing *pricing.Resolver
	// Notifier announces finished transitions; optional.
	Notifier Notifier
}

// +kubebuilder:rbac:groups=finops.costdeck.io,namespace=costdeck,resources=scalingconfigs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=finops.costdeck.io,namespace=costdeck,resources=scalingconfigs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=finops.costdeck.io,namespace=costdeck,resources=scalingconfigs/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments;statefulsets,verbs=get;list;watch;update;patch

func (r *ScalingConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	l := logf.FromContext(ctx)

	// 1. Fetch the ScalingConfig
	config := &finopsv1.ScalingConfig{}
	if err := r.Get(ctx, req.NamespacedName, config); err != nil {
		if errors.IsNotFound(err) {
			telemetry.ForgetScaling("ScalingConfig", req.Name)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	before := config.Status.DeepCopy()

	// 1.5 Conflict Resolution: "Group Wins"
	if managed, groupName, err := r.isManagedByGroup(ctx, config.Spec.TargetNamespace); err == nil && managed {
		l.Info("Namespace managed by group, overriding individual config", "namespace", config.Spec.TargetNamespace, "group", groupName)
		return r.markAsOverridden(ctx, config, before, groupName)
	}

	// 2. Determine desired state
	decision := r.Engine.Decide(time.Now(), config.Spec.Schedules, config.Spec.Active, config.Spec.ActiveUntil, config.Annotations)
	targetActive := decision.Active

	l.Info("Reconciling ScalingConfig", "targetNamespace", config.Spec.TargetNamespace, "targetActive", targetActive, "mode", decision.Mode)

	// 2.5 Phase and Timeout Logic
	previousPhase := config.Status.Phase
	timeoutPassed := r.updateStatusPhase(ctx, config, targetActive)

	// 3. Execute Scaling if needed
	newReplicas, ready, err := r.Engine.ScaleTarget(ctx, config.Spec.TargetNamespace, targetActive, config.Spec.Sequence, config.Spec.Exclusions, config.Status.OriginalReplicas, timeoutPassed)
	if err != nil {
		l.Error(err, "failed to execute scaling")
		return ctrl.Result{RequeueAfter: time.Minute}, err
	}

	// 4. Update Status
	config.Status.OriginalReplicas = newReplicas
	// Phase and LastAction are tracked before ScaleTarget so the timeout window starts immediately.
	applyDecision(&config.Status.ScheduleStatus, &config.Status.Conditions, decision, config.Generation, "namespace")
	if ready {
		config.Status.Phase = scaling.PhaseScaledDown
		if targetActive {
			config.Status.Phase = scaling.PhaseScaledUp
		}
	}
	setReadyCondition(&config.Status.Conditions, config.Status.Phase, targetActive, config.Generation, "")
	var savings float64
	if cpu, mem, err := r.Engine.KeptDown(ctx, config.Spec.TargetNamespace, config.Status.OriginalReplicas); err == nil {
		savings = recordSavings(ctx, &config.Status.ScheduleStatus, r.Pricing, cpu, mem)
	}

	if err := updateStatus(ctx, r.Client, config, before, &config.Status); err != nil {
		return ctrl.Result{}, err
	}
	announceTransition(ctx, r.Notifier, "Namespace", config.Spec.TargetNamespace, previousPhase, config.Status.Phase, config.Status.ScheduleStatus, nil)
	telemetry.RecordScaling("ScalingConfig", config.Name, targetActive, ready,
		decision.Mode == scaling.ModeManualUp || decision.Mode == scaling.ModeManualDown, savings, config.Status.Currency)

	// Faster requeue if scaling is in progress
	if !ready {
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}
	return ctrl.Result{RequeueAfter: settledRequeue(decision, time.Now())}, nil
}

func (r *ScalingConfigReconciler) isManagedByGroup(ctx context.Context, ns string) (bool, string, error) {
	groups := &finopsv1.ScalingGroupList{}
	if err := r.List(ctx, groups); err != nil {
		return false, "", err
	}
	for _, g := range groups.Items {
		if slices.Contains(g.Spec.Namespaces, ns) {
			return true, g.Name, nil
		}
	}
	return false, "", nil
}

func (r *ScalingConfigReconciler) markAsOverridden(ctx context.Context, config *finopsv1.ScalingConfig, before *finopsv1.ScalingConfigStatus, groupName string) (ctrl.Result, error) {
	if config.Status.Phase != PhaseOverriddenByGroup {
		config.Status.LastAction = metav1.Now()
	}
	config.Status.Phase = PhaseOverriddenByGroup
	config.Status.ScheduleStatus = finopsv1.ScheduleStatus{}
	meta.SetStatusCondition(&config.Status.Conditions, metav1.Condition{
		Type: ConditionReady, Status: metav1.ConditionFalse, Reason: PhaseOverriddenByGroup,
		ObservedGeneration: config.Generation,
		Message: fmt.Sprintf("Namespace %s is managed by ScalingGroup %s; this config only contributes its sequence and exclusions.",
			config.Spec.TargetNamespace, groupName),
	})
	if err := updateStatus(ctx, r.Client, config, before, &config.Status); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
}

func (r *ScalingConfigReconciler) updateStatusPhase(ctx context.Context, config *finopsv1.ScalingConfig, targetActive bool) bool {
	l := logf.FromContext(ctx)
	currentPhase := config.Status.Phase
	computedPhase := r.Engine.ComputePhase(ctx, config.Spec.TargetNamespace, targetActive)

	if currentPhase != computedPhase {
		config.Status.Phase = computedPhase
		config.Status.LastAction = metav1.Now()
	} else if config.Status.LastAction.IsZero() {
		config.Status.LastAction = metav1.Now()
	}

	if config.Status.Phase == "ScalingUp" || config.Status.Phase == "ScalingDown" {
		if time.Since(config.Status.LastAction.Time) > time.Minute {
			l.Info("Scaling timeout exceeded", "namespace", config.Spec.TargetNamespace)
			return true
		}
	}
	return false
}

// SetupWithManager sets up the controller with the Manager.
func (r *ScalingConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.Engine == nil {
		r.Engine = &scaling.Engine{Client: r.Client}
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&finopsv1.ScalingConfig{}).
		Named("scalingconfig").
		Complete(r)
}
