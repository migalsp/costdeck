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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/scaling"
)

// CostDeckConfigReconciler reconciles a CostDeckConfig object
type CostDeckConfigReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=finops.costdeck.io,resources=costdeckconfigs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=finops.costdeck.io,resources=costdeckconfigs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=finops.costdeck.io,resources=costdeckconfigs/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch;delete

func (r *CostDeckConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	l := log.FromContext(ctx)

	var config finopsv1.CostDeckConfig
	if err := r.Get(ctx, req.NamespacedName, &config); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	l.Info("Reconciling CostDeckConfig", "name", config.Name)

	// Validate and test AWS provider if configured
	if config.Spec.Providers.AWS != nil && config.Spec.Providers.AWS.Enabled {
		awsStatus := r.reconcileAWSProvider(ctx, &config)
		config.Status.AWS = awsStatus
	} else {
		config.Status.AWS = nil
	}

	// Azure and GCP are stubs — just report not connected
	if config.Spec.Providers.Azure != nil && config.Spec.Providers.Azure.Enabled {
		config.Status.Azure = &finopsv1.ProviderStatus{
			Connected:   false,
			LastChecked: metav1.Now(),
			Error:       "Azure provider is not yet implemented",
		}
	} else {
		config.Status.Azure = nil
	}

	if config.Spec.Providers.GCP != nil && config.Spec.Providers.GCP.Enabled {
		config.Status.GCP = &finopsv1.ProviderStatus{
			Connected:   false,
			LastChecked: metav1.Now(),
			Error:       "GCP provider is not yet implemented",
		}
	} else {
		config.Status.GCP = nil
	}

	// Update status
	if err := r.Status().Update(ctx, &config); err != nil {
		l.Error(err, "Failed to update CostDeckConfig status")
		return ctrl.Result{}, err
	}

	// Requeue every 5 minutes to refresh provider connectivity
	return ctrl.Result{RequeueAfter: 300_000_000_000}, nil // 5 minutes
}

func (r *CostDeckConfigReconciler) reconcileAWSProvider(ctx context.Context, config *finopsv1.CostDeckConfig) *finopsv1.ProviderStatus {
	l := log.FromContext(ctx)
	awsCfg := config.Spec.Providers.AWS
	status := &finopsv1.ProviderStatus{
		LastChecked: metav1.Now(),
	}

	if awsCfg.SecretRef == "" {
		status.Error = "No credentials configured (secretRef is empty)"
		return status
	}

	// Verify the referenced secret exists
	secret := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{
		Name:      awsCfg.SecretRef,
		Namespace: config.Namespace,
	}, secret); err != nil {
		if errors.IsNotFound(err) {
			status.Error = fmt.Sprintf("Referenced secret %q not found", awsCfg.SecretRef)
		} else {
			status.Error = fmt.Sprintf("Failed to read secret: %v", err)
		}
		return status
	}

	// Ensure owner reference is set on the secret
	if err := r.ensureSecretOwnership(ctx, config, secret); err != nil {
		l.Error(err, "Failed to set owner reference on secret")
	}

	// Try to initialize the provider and validate connectivity
	provider, err := scaling.NewAWSProviderFromSecret(ctx, r.Client, awsCfg.SecretRef, config.Namespace, awsCfg.Region)
	if err != nil {
		status.Error = fmt.Sprintf("Failed to initialize AWS provider: %v", err)
		return status
	}

	// Discover resources to test connectivity and count resources
	totalDiscovered := 0
	resourceTypes := awsCfg.ResourceTypes
	if len(resourceTypes) == 0 {
		resourceTypes = []string{"aurora"}
	}

	for _, rt := range resourceTypes {
		targets, err := provider.Discover(ctx, rt, awsCfg.DiscoveryTags)
		if err != nil {
			status.Error = fmt.Sprintf("Discovery failed for %s: %v", rt, err)
			return status
		}
		totalDiscovered += len(targets)
	}

	status.Connected = true
	status.DiscoveredResources = totalDiscovered
	l.Info("AWS provider validated successfully", "discoveredResources", totalDiscovered)
	return status
}

// ensureSecretOwnership sets the CostDeckConfig as the owner of the secret
// so it gets garbage-collected when the config is deleted.
func (r *CostDeckConfigReconciler) ensureSecretOwnership(ctx context.Context, config *finopsv1.CostDeckConfig, secret *corev1.Secret) error {
	if metav1.IsControlledBy(secret, config) {
		return nil
	}

	if err := controllerutil.SetControllerReference(config, secret, r.Scheme); err != nil {
		return err
	}

	return r.Update(ctx, secret)
}

// SetupWithManager sets up the controller with the Manager.
func (r *CostDeckConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&finopsv1.CostDeckConfig{}).
		Owns(&corev1.Secret{}).
		Named("costdeckconfig").
		Complete(r)
}
