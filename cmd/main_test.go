package main

import (
	"context"
	"crypto/tls"
	"flag"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"sync"
	"testing"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
)

type parsedFlags struct {
	flags        map[string]string
	leaderElect  bool
	probeAddr    string
	secure       bool
	loggerConfig zap.Options
}

func parseArgs(t *testing.T, args ...string) parsedFlags {
	t.Helper()
	savedFlags, savedArgs := flag.CommandLine, os.Args
	t.Cleanup(func() { flag.CommandLine, os.Args = savedFlags, savedArgs })
	flag.CommandLine = flag.NewFlagSet("manager", flag.ContinueOnError)
	os.Args = append([]string{"manager"}, args...)
	var p parsedFlags
	p.flags, p.leaderElect, p.probeAddr, p.secure, p.loggerConfig = parseFlags()
	return p
}

func TestParseFlagsDefaults(t *testing.T) {
	p := parseArgs(t)
	want := map[string]string{
		"metricsAddr": "0", "probeAddr": ":8081",
		"webhookCertPath": "", "webhookCertName": "tls.crt", "webhookCertKey": "tls.key",
		"metricsCertPath": "", "metricsCertName": "tls.crt", "metricsCertKey": "tls.key",
	}
	if !maps.Equal(p.flags, want) || p.leaderElect || p.probeAddr != ":8081" || !p.secure || !p.loggerConfig.Development {
		t.Errorf("defaults = %+v; want metrics off, probes on :8081, no leader election, "+
			"secure metrics and development logs", p)
	}
}

func TestParseFlagsAsTheChartPassesThem(t *testing.T) {
	p := parseArgs(t, "--leader-elect", "--metrics-bind-address=:8443", "--metrics-secure=false",
		"--health-probe-bind-address=:9440",
		"--metrics-cert-path=/certs/metrics", "--metrics-cert-name=m.crt", "--metrics-cert-key=m.key",
		"--webhook-cert-path=/certs/webhook", "--webhook-cert-name=w.crt", "--webhook-cert-key=w.key", "--zap-devel=false")
	want := map[string]string{
		"metricsAddr": ":8443", "probeAddr": ":9440",
		"webhookCertPath": "/certs/webhook", "webhookCertName": "w.crt", "webhookCertKey": "w.key",
		"metricsCertPath": "/certs/metrics", "metricsCertName": "m.crt", "metricsCertKey": "m.key",
	}
	if !maps.Equal(p.flags, want) || !p.leaderElect || p.probeAddr != ":9440" || p.secure || p.loggerConfig.Development {
		t.Errorf("parsed = %+v", p)
	}
}

func kindsOf(t *testing.T, objs []client.Object) []string {
	t.Helper()
	out := make([]string, 0, len(objs))
	for _, o := range objs {
		gvk, err := apiutil.GVKForObject(o, scheme)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, gvk.GroupKind().String())
	}
	slices.Sort(out)
	return out
}

func TestCacheOptionsConfineOwnKindsToTheOperatorNamespace(t *testing.T) {
	opts := cacheOptions("costdeck")
	objs := make([]client.Object, 0, len(opts.ByObject))
	for obj, by := range opts.ByObject {
		objs = append(objs, obj)
		if len(by.Namespaces) != 1 || by.Namespaces["costdeck"].LabelSelector != nil {
			t.Errorf("%T is cached in %v, want the operator namespace only", obj, by.Namespaces)
		}
	}
	want := []string{"CostDeckConfig.finops.costdeck.io", "Event", "NamespaceFinOps.finops.costdeck.io",
		"NamespaceOptimization.finops.costdeck.io", "ScalingConfig.finops.costdeck.io", "ScalingGroup.finops.costdeck.io"}
	if got := kindsOf(t, objs); !slices.Equal(got, want) {
		t.Errorf("kinds confined to the operator namespace = %v, want %v", got, want)
	}
	if len(opts.DefaultNamespaces) != 0 {
		t.Errorf("default namespaces = %v; pods, nodes and namespaces must stay cluster-wide", opts.DefaultNamespaces)
	}

	pod := &corev1.Pod{Name: "api", ManagedFields: []metav1.ManagedFieldsEntry{{Manager: "kubectl"}}}
	stripped, err := opts.DefaultTransform(pod)
	if err != nil || stripped.(*corev1.Pod).ManagedFields != nil || stripped.(*corev1.Pod).Name != "api" {
		t.Errorf("cached objects keep their managedFields: %+v, %v", stripped, err)
	}
}

func TestClientOptionsReadSecretsConfigMapsHPAsAndPoliciesLive(t *testing.T) {
	want := []string{"ConfigMap", "HorizontalPodAutoscaler.autoscaling", "RightsizingPolicy.finops.costdeck.io", "Secret"}
	if got := kindsOf(t, clientOptions().Cache.DisableFor); !slices.Equal(got, want) {
		t.Fatalf("kinds that bypass the cache = %v, want %v", got, want)
	}

	var mu sync.Mutex
	var live []string
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		live = append(live, r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"NotFound","code":404}`))
	}))
	defer apiServer.Close()

	mapper := meta.NewDefaultRESTMapper(nil)
	for _, gvk := range []schema.GroupVersionKind{
		corev1.SchemeGroupVersion.WithKind("Secret"),
		corev1.SchemeGroupVersion.WithKind("ConfigMap"),
		corev1.SchemeGroupVersion.WithKind("Pod"),
		autoscalingv2.SchemeGroupVersion.WithKind("HorizontalPodAutoscaler"),
		finopsv1.GroupVersion.WithKind("RightsizingPolicy"),
	} {
		mapper.Add(gvk, meta.RESTScopeNamespace)
	}
	cached := fake.NewClientBuilder().WithScheme(scheme).WithObjects(&corev1.Pod{Name: "api", Namespace: "shop"}).Build()
	opts := clientOptions()
	opts.Scheme, opts.Mapper, opts.Cache.Reader = scheme, mapper, cached
	c, err := client.New(&rest.Config{Host: apiServer.URL}, opts)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	key := client.ObjectKey{Namespace: "costdeck", Name: "x"}
	uncached := []client.Object{
		&corev1.Secret{}, &corev1.ConfigMap{}, &autoscalingv2.HorizontalPodAutoscaler{}, &finopsv1.RightsizingPolicy{},
	}
	for _, obj := range uncached {
		_ = c.Get(ctx, key, obj)
	}
	if err := c.Get(ctx, client.ObjectKey{Namespace: "shop", Name: "api"}, &corev1.Pod{}); err != nil {
		t.Errorf("a pod is not read from the cache: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	wantLive := []string{
		"/api/v1/namespaces/costdeck/secrets/x",
		"/api/v1/namespaces/costdeck/configmaps/x",
		"/apis/autoscaling/v2/namespaces/costdeck/horizontalpodautoscalers/x",
		"/apis/finops.costdeck.io/v1/namespaces/costdeck/rightsizingpolicies/x",
	}
	if !slices.Equal(live, wantLive) {
		t.Errorf("requests to the API server = %v, want exactly the uncached kinds %v", live, wantLive)
	}
}

func nextProtos(t *testing.T, opts []func(*tls.Config)) []string {
	t.Helper()
	cfg := &tls.Config{NextProtos: []string{"h2", "http/1.1"}}
	for _, o := range opts {
		o(cfg)
	}
	return cfg.NextProtos
}

func TestSetupWebhookServer(t *testing.T) {
	plain, ok := setupWebhookServer(map[string]string{"webhookCertName": "ignored.crt"}).(*webhook.DefaultServer)
	if !ok {
		t.Fatal("not the default webhook server")
	}
	if o := plain.Options; o.CertDir != "" || o.CertName != "" || o.KeyName != "" {
		t.Errorf("without a cert path the server takes certificate settings: %+v", o)
	}
	if got := nextProtos(t, plain.Options.TLSOpts); !slices.Equal(got, []string{"http/1.1"}) {
		t.Errorf("webhook protocols = %v, want HTTP/2 off", got)
	}

	certFlags := map[string]string{"webhookCertPath": "/certs", "webhookCertName": "w.crt", "webhookCertKey": "w.key"}
	withCerts := setupWebhookServer(certFlags).(*webhook.DefaultServer)
	if o := withCerts.Options; o.CertDir != "/certs" || o.CertName != "w.crt" || o.KeyName != "w.key" {
		t.Errorf("webhook certificates = %q %q %q", o.CertDir, o.CertName, o.KeyName)
	}
}

func TestSetupMetricsOptions(t *testing.T) {
	secure := setupMetricsOptions(map[string]string{"metricsAddr": ":8443"}, true)
	if secure.BindAddress != ":8443" || !secure.SecureServing || secure.FilterProvider == nil || secure.CertDir != "" {
		t.Errorf("secure metrics = %+v; want HTTPS with authentication and authorization", secure)
	}
	if got := nextProtos(t, secure.TLSOpts); !slices.Equal(got, []string{"http/1.1"}) {
		t.Errorf("metrics protocols = %v, want HTTP/2 off", got)
	}

	certFlags := map[string]string{
		"metricsAddr": ":8080", "metricsCertPath": "/certs", "metricsCertName": "m.crt", "metricsCertKey": "m.key",
	}
	open := setupMetricsOptions(certFlags, false)
	if open.SecureServing || open.FilterProvider != nil {
		t.Errorf("plain metrics = %+v; want no authentication filter", open)
	}
	if open.CertDir != "/certs" || open.CertName != "m.crt" || open.KeyName != "m.key" {
		t.Errorf("metrics certificates = %q %q %q", open.CertDir, open.CertName, open.KeyName)
	}
}
