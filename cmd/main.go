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

package main

import (
	"crypto/tls"
	"flag"
	"os"

	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	"k8s.io/client-go/kubernetes"
	metricsv "k8s.io/metrics/pkg/client/clientset/versioned"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/api"
	"github.com/migalsp/costdeck-operator/internal/controller"
	// +kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	utilruntime.Must(finopsv1.AddToScheme(scheme))
	// +kubebuilder:scaffold:scheme
}

func parseFlags() (map[string]string, bool, string, bool, zap.Options) {
	var metricsAddr, probeAddr string
	var metricsCertPath, metricsCertName, metricsCertKey string
	var webhookCertPath, webhookCertName, webhookCertKey string
	var enableLeaderElection, secureMetrics, enableHTTP2 bool

	flag.StringVar(&metricsAddr, "metrics-bind-address", "0", "The address the metrics endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false, "Enable leader election.")
	flag.BoolVar(&secureMetrics, "metrics-secure", true, "Serve metrics securely.")
	flag.StringVar(&webhookCertPath, "webhook-cert-path", "", "Webhook cert directory.")
	flag.StringVar(&webhookCertName, "webhook-cert-name", "tls.crt", "Webhook cert filename.")
	flag.StringVar(&webhookCertKey, "webhook-cert-key", "tls.key", "Webhook key filename.")
	flag.StringVar(&metricsCertPath, "metrics-cert-path", "", "Metrics cert directory.")
	flag.StringVar(&metricsCertName, "metrics-cert-name", "tls.crt", "Metrics cert filename.")
	flag.StringVar(&metricsCertKey, "metrics-cert-key", "tls.key", "Metrics key filename.")
	flag.BoolVar(&enableHTTP2, "enable-http2", false, "Enable HTTP/2.")

	opts := zap.Options{Development: true}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	flags := map[string]string{
		"metricsAddr":     metricsAddr,
		"probeAddr":       probeAddr,
		"webhookCertPath": webhookCertPath, "webhookCertName": webhookCertName, "webhookCertKey": webhookCertKey,
		"metricsCertPath": metricsCertPath, "metricsCertName": metricsCertName, "metricsCertKey": metricsCertKey,
	}
	return flags, enableLeaderElection, probeAddr, secureMetrics, opts
}

func main() {
	flags, enableLeaderElection, probeAddr, secureMetrics, zapOpts := parseFlags()
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zapOpts)))

	webhookServer := setupWebhookServer(flags)
	metricsServerOptions := setupMetricsOptions(flags, secureMetrics)

	config := ctrl.GetConfigOrDie()
	mgr, err := ctrl.NewManager(config, ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsServerOptions,
		WebhookServer:          webhookServer,
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "fdcd422b.costdeck.io",
	})
	if err != nil {
		setupLog.Error(err, "Failed to start manager")
		os.Exit(1)
	}

	metricsClient, err := metricsv.NewForConfig(config)
	if err != nil {
		setupLog.Error(err, "Failed to create metrics client")
		os.Exit(1)
	}

	k8sClient, err := kubernetes.NewForConfig(config)
	if err != nil {
		setupLog.Error(err, "Failed to create k8s client")
		os.Exit(1)
	}

	apiServer := &api.Server{
		Client:        mgr.GetClient(),
		K8sClient:     k8sClient,
		MetricsClient: metricsClient,
		Port:          "8082",
	}
	if err := mgr.Add(apiServer); err != nil {
		setupLog.Error(err, "Failed to add API server to manager")
		os.Exit(1)
	}

	if err := (&controller.NamespaceFinOpsReconciler{
		Client:        mgr.GetClient(),
		Scheme:        mgr.GetScheme(),
		MetricsClient: metricsClient,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create controller", "controller", "NamespaceFinOps")
		os.Exit(1)
	}

	if err := (&controller.NamespaceDiscoveryReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create controller", "controller", "NamespaceDiscovery")
		os.Exit(1)
	}
	if err := (&controller.ScalingConfigReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create controller", "controller", "ScalingConfig")
		os.Exit(1)
	}
	if err := (&controller.ScalingGroupReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create controller", "controller", "ScalingGroup")
		os.Exit(1)
	}
	// +kubebuilder:scaffold:builder

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "Failed to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "Failed to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("Starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "Failed to run manager")
		os.Exit(1)
	}
}

func setupWebhookServer(flags map[string]string) webhook.Server {
	opts := webhook.Options{
		TLSOpts: []func(*tls.Config){func(c *tls.Config) {
			c.NextProtos = []string{"http/1.1"}
		}},
	}
	if p := flags["webhookCertPath"]; len(p) > 0 {
		opts.CertDir = p
		opts.CertName = flags["webhookCertName"]
		opts.KeyName = flags["webhookCertKey"]
	}
	return webhook.NewServer(opts)
}

func setupMetricsOptions(flags map[string]string, secure bool) metricsserver.Options {
	opts := metricsserver.Options{
		BindAddress:   flags["metricsAddr"],
		SecureServing: secure,
		TLSOpts: []func(*tls.Config){func(c *tls.Config) {
			c.NextProtos = []string{"http/1.1"}
		}},
	}
	if secure {
		opts.FilterProvider = filters.WithAuthenticationAndAuthorization
	}
	if p := flags["metricsCertPath"]; len(p) > 0 {
		opts.CertDir = p
		opts.CertName = flags["metricsCertName"]
		opts.KeyName = flags["metricsCertKey"]
	}
	return opts
}
