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
	"time"

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
	"github.com/migalsp/costdeck-operator/internal/metrics"
	"github.com/migalsp/costdeck-operator/internal/scaling"
	"github.com/migalsp/costdeck-operator/internal/webex"
)

// Label that marks the credentials Secrets CostDeck created from the settings UI.
const (
	managedByLabel = "app.kubernetes.io/managed-by"
	managedByValue = "costdeck-operator"
)

// CostDeckConfigReconciler reconciles a CostDeckConfig object
type CostDeckConfigReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=finops.costdeck.io,namespace=costdeck,resources=costdeckconfigs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=finops.costdeck.io,namespace=costdeck,resources=costdeckconfigs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=finops.costdeck.io,namespace=costdeck,resources=costdeckconfigs/finalizers,verbs=update

// Credentials Secrets and the AI report ConfigMap are only ever read by name in the
// operator namespace, and both kinds bypass the informer cache (see cmd/main.go). That is
// why neither list nor watch is requested, and why the grant is a Role rather than a
// ClusterRole: CostDeck has no business enumerating Secrets anywhere.
// +kubebuilder:rbac:groups="",namespace=costdeck,resources=secrets,verbs=get;create;update;patch;delete
// +kubebuilder:rbac:groups="",namespace=costdeck,resources=configmaps,verbs=get;create;update;patch

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

	// Every enabled cloud gets a live connectivity check and a resource count.
	for _, name := range scaling.CloudProviders {
		var st *finopsv1.ProviderStatus
		if scaling.CloudSettingsFor(&config, name).Enabled {
			st = r.reconcileCloudProvider(ctx, &config, name)
		}
		switch name {
		case scaling.ProviderAWS:
			config.Status.AWS = st
		case scaling.ProviderAzure:
			config.Status.Azure = st
		case scaling.ProviderGCP:
			config.Status.GCP = st
		}
	}

	config.Status.VictoriaMetrics = r.reconcileVictoriaMetrics(ctx, &config)
	config.Status.Webex = r.reconcileWebex(ctx)

	// Update status
	if err := r.Status().Update(ctx, &config); err != nil {
		l.Error(err, "Failed to update CostDeckConfig status")
		return ctrl.Result{}, err
	}

	// Requeue every 5 minutes to refresh provider connectivity
	return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
}

// reconcileCloudProvider checks a cloud provider's credentials and counts the resources
// it can discover. A provider without a Secret uses the pod identity (IRSA, AKS or GKE
// workload identity), which is a valid setup rather than a missing credential.
func (r *CostDeckConfigReconciler) reconcileCloudProvider(ctx context.Context, config *finopsv1.CostDeckConfig, name string) *finopsv1.ProviderStatus {
	l := log.FromContext(ctx).WithValues("provider", name)
	settings := scaling.CloudSettingsFor(config, name)
	status := &finopsv1.ProviderStatus{LastChecked: metav1.Now()}

	if settings.SecretRef != "" {
		secret := &corev1.Secret{}
		if err := r.Get(ctx, types.NamespacedName{Name: settings.SecretRef, Namespace: config.Namespace}, secret); err != nil {
			if errors.IsNotFound(err) {
				status.Error = fmt.Sprintf("Referenced secret %q not found", settings.SecretRef)
			} else {
				status.Error = fmt.Sprintf("Failed to read secret: %v", err)
			}
			return status
		}
		if err := r.ensureSecretOwnership(ctx, config, secret); err != nil {
			l.Error(err, "Failed to set owner reference on secret")
		}
	}

	provider, err := scaling.BuildProvider(ctx, r.Client, config, name, nil)
	if err != nil {
		status.Error = err.Error()
		return status
	}
	if err := provider.ValidateConnectivity(ctx); err != nil {
		status.Error = err.Error()
		return status
	}
	total := 0
	for _, rt := range settings.ResourceTypes {
		targets, err := provider.Discover(ctx, rt, settings.Filter)
		if err != nil {
			status.Error = fmt.Sprintf("Discovery failed for %s: %v", rt, err)
			return status
		}
		total += len(targets)
	}
	status.Connected = true
	status.DiscoveredResources = total
	status.Message = fmt.Sprintf("%d resources discoverable", total)
	l.Info("Validated cloud provider", "discoveredResources", total)
	return status
}

// reconcileVictoriaMetrics validates the metrics endpoint so a misconfiguration shows up
// in the CostDeckConfig status and the settings page instead of only in the operator log.
func (r *CostDeckConfigReconciler) reconcileVictoriaMetrics(ctx context.Context, config *finopsv1.CostDeckConfig) *finopsv1.ProviderStatus {
	vm := config.Spec.Integrations.VictoriaMetrics
	if vm == nil || !vm.Enabled {
		return nil
	}
	status := &finopsv1.ProviderStatus{LastChecked: metav1.Now()}
	vmClient, err := metrics.BuildVMClient(ctx, r.Client, vm, nil)
	if err == nil {
		err = vmClient.Validate(ctx)
	}
	if err != nil {
		status.Error = err.Error()
		log.FromContext(ctx).Info("VictoriaMetrics validation failed", "error", err.Error())
		return status
	}
	status.Connected = true
	return status
}

// reconcileWebex checks the bot token and, when a room is configured, that the bot can see
// it. "The bot receives messages but never answers" is almost always one of the two.
func (r *CostDeckConfigReconciler) reconcileWebex(ctx context.Context) *finopsv1.ProviderStatus {
	settings, err := webex.LoadSettings(ctx, r.Client)
	if settings == nil && err == nil {
		return nil
	}
	status := &finopsv1.ProviderStatus{LastChecked: metav1.Now()}
	if err != nil {
		status.Error = err.Error()
		return status
	}
	msg, err := webex.Check(ctx, webex.NewClient(settings.Token), settings)
	if err != nil {
		status.Error = err.Error()
		return status
	}
	status.Connected, status.Message = true, msg
	return status
}

// ensureSecretOwnership sets the CostDeckConfig as the owner of a credentials Secret that
// CostDeck itself created, so it is garbage-collected together with the config. Secrets
// the user brought (for example from external-secrets or sealed-secrets) are left alone:
// adopting them would delete the user's Secret when the config goes away, and would fight
// the controller that already owns them.
func (r *CostDeckConfigReconciler) ensureSecretOwnership(ctx context.Context, config *finopsv1.CostDeckConfig, secret *corev1.Secret) error {
	if secret.Labels[managedByLabel] != managedByValue || metav1.IsControlledBy(secret, config) {
		return nil
	}

	if err := controllerutil.SetControllerReference(config, secret, r.Scheme); err != nil {
		return err
	}

	return r.Update(ctx, secret)
}

// SetupWithManager sets up the controller with the Manager.
func (r *CostDeckConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Secrets are deliberately not watched: that would need list/watch on Secrets.
	// Provider connectivity is re-validated on every periodic reconcile instead.
	return ctrl.NewControllerManagedBy(mgr).
		For(&finopsv1.CostDeckConfig{}).
		Named("costdeckconfig").
		Complete(r)
}
