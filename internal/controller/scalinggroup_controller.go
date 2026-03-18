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
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/scaling"
)

type ScalingGroupReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Engine   *scaling.Engine
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=finops.costdeck.io,resources=scalinggroups,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=finops.costdeck.io,resources=scalinggroups/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=finops.costdeck.io,resources=scalinggroups/finalizers,verbs=update
// +kubebuilder:rbac:groups=finops.costdeck.io,resources=scalingpolicies,verbs=get;list;watch

func (r *ScalingGroupReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	l := logf.FromContext(ctx)

	// 1. Fetch the ScalingGroup
	group := &finopsv1.ScalingGroup{}
	if err := r.Get(ctx, req.NamespacedName, group); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// 2. Determine desired state
	targetActive := r.Engine.IsActive(group.Spec.Schedules, group.Spec.Active)
	l.Info("Reconciling ScalingGroup", "category", group.Spec.Category, "namespaces", group.Spec.Namespaces, "targetActive", targetActive)

	// Initialize status maps if nil
	if group.Status.OriginalReplicas == nil {
		group.Status.OriginalReplicas = make(map[string]int32)
	}

	// 3. Define stages from group.Spec.Sequence
	// Default: all namespaces in one stage if no sequence defined
	managedNamespaces := group.Spec.Namespaces
	var stages [][]string

	if len(group.Spec.Sequence) > 0 {
		for _, s := range group.Spec.Sequence {
			nsInStage := strings.Fields(s)
			stages = append(stages, nsInStage)
		}
		// Add namespaces not mentioned in sequence as the last stage
		var missing []string
		for _, ns := range managedNamespaces {
			found := false
			for _, stage := range stages {
				for _, sn := range stage {
					if sn == ns {
						found = true
						break
					}
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

	// Reverse stages for Scaling Up if needed?
	// Usually sequence is defined for "Shutdown" order.
	// User said: "first 1, then 3,4,5, then 2".
	// This usually means Up order. Let's assume sequence defines UP order, and reverse for DOWN.
	if !targetActive {
		for i, j := 0, len(stages)-1; i < j; i, j = i+1, j-1 {
			stages[i], stages[j] = stages[j], stages[i]
		}
	}

	allReady := true
	managedCount := 0

	timeoutPassed := false
	if group.Status.Phase == "ScalingUp" || group.Status.Phase == "ScalingDown" {
		if time.Since(group.Status.LastAction.Time) > time.Minute {
			timeoutPassed = true
		}
	}

	namespacesReady := 0
	namespacesTotal := 0
	for _, stage := range stages {
		namespacesTotal += len(stage)
	}

	var blockingNamespaces []string
	var readyNamespaces []string

	// 4. Iterate over stages
	for i, stage := range stages {
		l.Info("Processing scaling stage", "stageIndex", i, "namespaces", stage)

		stageReady := true
		for _, ns := range stage {
			managedCount++

			// Handle External Targets embedded in the sequence
			if strings.HasPrefix(ns, "ext:") {
				extId := strings.TrimPrefix(ns, "ext:")
				var extTarget *finopsv1.ExternalTarget
				for i := range group.Spec.ExternalTargets {
					if group.Spec.ExternalTargets[i].Identifier == extId {
						extTarget = &group.Spec.ExternalTargets[i]
						break
					}
				}

				if extTarget == nil {
					l.Info("External target in sequence not found in spec", "target", extId)
					continue
				}

				provider, ok := r.Engine.Providers[extTarget.Provider]
				if !ok {
					l.Error(fmt.Errorf("provider not found"), "cannot scale external target", "target", extTarget.Identifier)
					stageReady = false
					allReady = false
					// check if not already in blocking string
					found := false
					for _, b := range blockingNamespaces {
						if b == ns {
							found = true
							break
						}
					}
					if !found {
						blockingNamespaces = append(blockingNamespaces, ns)
					}
					continue
				}

				if err := provider.Scale(ctx, *extTarget, targetActive); err != nil {
					l.Error(err, "failed to scale external target", "target", extTarget.Identifier)
					stageReady = false
					allReady = false
					found := false
					for _, b := range blockingNamespaces {
						if b == ns {
							found = true
							break
						}
					}
					if !found {
						blockingNamespaces = append(blockingNamespaces, ns)
					}
					continue
				}

				isRdy, err := provider.IsReady(ctx, *extTarget, targetActive)
				if err != nil {
					l.Error(err, "failed to check readiness of external target", "target", extTarget.Identifier)
					stageReady = false
					allReady = false
					found := false
					for _, b := range blockingNamespaces {
						if b == ns {
							found = true
							break
						}
					}
					if !found {
						blockingNamespaces = append(blockingNamespaces, ns)
					}
				} else if !isRdy {
					stageReady = false
					allReady = false
					found := false
					for _, b := range blockingNamespaces {
						if b == ns {
							found = true
							break
						}
					}
					if !found {
						blockingNamespaces = append(blockingNamespaces, ns)
					}
				} else {
					namespacesReady++
					readyNamespaces = append(readyNamespaces, ns)
				}
				continue
			}

			// a. Fetch individual ScalingConfig for exclusions and sequence inheritance
			var exclusions []string
			var nsSequence []string

			// Try to find a ScalingConfig that manages this target namespace
			configList := &finopsv1.ScalingConfigList{}
			if err := r.List(ctx, configList, client.InNamespace(group.Namespace)); err == nil {
				for _, cfg := range configList.Items {
					if cfg.Spec.TargetNamespace == ns {
						exclusions = cfg.Spec.Exclusions
						nsSequence = cfg.Spec.Sequence
						l.Info("Found ScalingConfig for inheritance", "namespace", ns, "config", cfg.Name)
						break
					}
				}
			}

			// b. Scale Target
			nsKeyPrefix := ns + "/"
			nsReplicas := make(map[string]int32)
			if group.Status.OriginalReplicas != nil {
				for k, v := range group.Status.OriginalReplicas {
					if strings.HasPrefix(k, nsKeyPrefix) {
						nsReplicas[strings.TrimPrefix(k, nsKeyPrefix)] = v
					}
				}
			}

			updatedOriginals, nsReady, err := r.Engine.ScaleTarget(ctx, ns, targetActive, nsSequence, exclusions, nsReplicas, timeoutPassed)
			if err != nil {
				l.Error(err, "failed to scale namespace", "namespace", ns)
				allReady = false
				stageReady = false
				blockingNamespaces = append(blockingNamespaces, ns)
				continue
			}

			if !nsReady {
				stageReady = false
				allReady = false
			}

			// Merge back
			if group.Status.OriginalReplicas == nil {
				group.Status.OriginalReplicas = make(map[string]int32)
			}
			// First clear old ones for this namespace to sync deletions from engine
			for k := range group.Status.OriginalReplicas {
				if strings.HasPrefix(k, nsKeyPrefix) {
					delete(group.Status.OriginalReplicas, k)
				}
			}
			for k, v := range updatedOriginals {
				group.Status.OriginalReplicas[nsKeyPrefix+k] = v
			}

			// c. Check if namespace reached target phase
			phase := r.Engine.ComputePhase(ctx, ns, targetActive)
			if (targetActive && phase == "ScaledUp") || (!targetActive && phase == "ScaledDown") {
				namespacesReady++
				readyNamespaces = append(readyNamespaces, ns)
			} else {
				stageReady = false
				allReady = false
				// Prevent duplicate appends if ScaleTarget also failed
				found := false
				for _, bNs := range blockingNamespaces {
					if bNs == ns {
						found = true
						break
					}
				}
				if !found {
					blockingNamespaces = append(blockingNamespaces, ns)
				}
			}
		}

		if !stageReady {
			l.Info("Stage not ready, waiting before next stage", "stageIndex", i)
			break // Stop at this stage, wait for next reconcile
		}
	}

	if !allReady && len(blockingNamespaces) > 0 {
		stageNumber := 0
		for idx, stage := range stages {
			// Find which stage the first blocking namespace belongs to
			for _, sNs := range stage {
				if sNs == blockingNamespaces[0] {
					stageNumber = idx + 1
					break
				}
			}
			if stageNumber != 0 {
				break
			}
		}

		if timeoutPassed {
			msg := fmt.Sprintf("Timeout exceeded 1 min. Strict sequence is still active. Waiting on Stage %d: %s", stageNumber, strings.Join(blockingNamespaces, ", "))
			r.Recorder.Event(group, "Warning", "ScalingTimeout", msg)
		} else {
			msg := fmt.Sprintf("Executing Stage %d. Waiting for targets in: %s", stageNumber, strings.Join(blockingNamespaces, ", "))
			r.Recorder.Event(group, "Normal", "ScalingActive", msg)
		}
	}

	if namespacesReady > group.Status.NamespacesReady {
		r.Recorder.Eventf(group, "Normal", "ScalingProgress", "Progress updated: %d of %d targets reached target state.", namespacesReady, namespacesTotal)
	}

	// 5. Update Status
	group.Status.ManagedCount = managedCount
	group.Status.NamespacesReady = namespacesReady
	group.Status.NamespacesTotal = namespacesTotal
	group.Status.ReadyNamespaces = readyNamespaces

	newPhase := "ScaledUp"
	if allReady {
		if targetActive {
			newPhase = "ScaledUp"
		} else {
			newPhase = "ScaledDown"
		}
	} else {
		if targetActive {
			newPhase = "ScalingUp"
		} else {
			newPhase = "ScalingDown"
		}
	}

	if group.Status.Phase != newPhase {
		oldPhase := group.Status.Phase
		group.Status.Phase = newPhase
		group.Status.LastAction = metav1.Now()

		// Emit Event on Phase transition
		r.Recorder.Eventf(group, "Normal", "PhaseTransition", "Group phase transitioned from %s to %s", oldPhase, newPhase)
	} else if group.Status.LastAction.IsZero() {
		group.Status.LastAction = metav1.Now()
	}

	if err := r.Status().Update(ctx, group); err != nil {
		return ctrl.Result{}, err
	}

	// Requeue faster if scaling is in progress
	if !allReady {
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	return ctrl.Result{RequeueAfter: time.Minute}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ScalingGroupReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.Engine == nil {
		r.Engine = &scaling.Engine{Client: r.Client}
	}
	if r.Engine.Providers == nil {
		r.Engine.Providers = make(map[string]scaling.ExternalProvider)
	}

	// Initialize AWS Provider (this relies on the Pod having IRSA or environment variables set)
	awsProv, err := scaling.NewAWSProvider(context.Background())
	if err == nil {
		r.Engine.Providers[awsProv.Name()] = awsProv
		logf.Log.Info("Successfully initialized external provider", "provider", awsProv.Name())
	} else {
		logf.Log.Error(err, "Failed to initialize AWS Provider, external targets for AWS will not work")
	}

	r.Recorder = mgr.GetEventRecorderFor("scalinggroup-controller")

	return ctrl.NewControllerManagedBy(mgr).
		For(&finopsv1.ScalingGroup{}).
		Named("scalinggroup").
		Complete(r)
}
