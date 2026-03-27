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
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/scaling"
)

// ScalingConfigReconciler reconciles a ScalingConfig object
type ScalingConfigReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	Engine *scaling.Engine
}

// +kubebuilder:rbac:groups=finops.costdeck.io,resources=scalingconfigs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=finops.costdeck.io,resources=scalingconfigs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=finops.costdeck.io,resources=scalingconfigs/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments;statefulsets,verbs=get;list;watch;update;patch

func (r *ScalingConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	l := logf.FromContext(ctx)

	// 1. Fetch the ScalingConfig
	config := &finopsv1.ScalingConfig{}
	if err := r.Get(ctx, req.NamespacedName, config); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// 1.5 Conflict Resolution: "Group Wins"
	if managed, groupName, err := r.isManagedByGroup(ctx, config.Spec.TargetNamespace); err == nil && managed {
		l.Info("Namespace managed by group, overriding individual config", "namespace", config.Spec.TargetNamespace, "group", groupName)
		return r.markAsOverridden(ctx, config)
	}

	// 2. Determine desired state
	targetActive := r.Engine.IsActive(config.Spec.Schedules, config.Spec.Active)

	l.Info("Reconciling ScalingConfig", "targetNamespace", config.Spec.TargetNamespace, "targetActive", targetActive)

	// 2.5 Phase and Timeout Logic
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

	if err := r.Status().Update(ctx, config); err != nil {
		return ctrl.Result{}, err
	}

	// Faster requeue if scaling is in progress
	if !ready {
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	// Check again in 1 minute for schedule changes
	return ctrl.Result{RequeueAfter: time.Minute}, nil
}

func (r *ScalingConfigReconciler) isManagedByGroup(ctx context.Context, ns string) (bool, string, error) {
	groups := &finopsv1.ScalingGroupList{}
	if err := r.List(ctx, groups); err != nil {
		return false, "", err
	}
	for _, g := range groups.Items {
		for _, managedNs := range g.Spec.Namespaces {
			if managedNs == ns {
				return true, g.Name, nil
			}
		}
	}
	return false, "", nil
}

func (r *ScalingConfigReconciler) markAsOverridden(ctx context.Context, config *finopsv1.ScalingConfig) (ctrl.Result, error) {
	config.Status.Phase = "OverriddenByGroup"
	config.Status.LastAction = metav1.Now()
	if err := r.Status().Update(ctx, config); err != nil {
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
