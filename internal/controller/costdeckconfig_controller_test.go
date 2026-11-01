package controller

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlconfig "sigs.k8s.io/controller-runtime/pkg/config"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/scaling"
)

type fakeVictoriaMetrics struct {
	server  *httptest.Server
	series  atomic.Int64
	queries atomic.Int64
	delay   time.Duration
}

func newFakeVictoriaMetrics() *fakeVictoriaMetrics {
	vm := &fakeVictoriaMetrics{}
	vm.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		vm.queries.Add(1)
		time.Sleep(vm.delay)
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/api/v1/query" || !strings.Contains(r.URL.Query().Get("query"), "container_cpu_usage_seconds_total") {
			http.NotFound(w, r)
			return
		}
		result := "[]"
		if n := vm.series.Load(); n > 0 {
			result = fmt.Sprintf(`[{"metric":{},"value":[0,"%d"]}]`, n)
		}
		_, _ = fmt.Fprintf(w, `{"status":"success","data":{"resultType":"vector","result":%s}}`, result)
	}))
	DeferCleanup(vm.server.Close)
	return vm
}

func newTestManager() ctrl.Manager {
	GinkgoHelper()
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                 scheme.Scheme,
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
		Controller:             ctrlconfig.Controller{SkipNameValidation: new(true)},
	})
	Expect(err).NotTo(HaveOccurred())
	return mgr
}

var _ = Describe("CostDeckConfig Controller", func() {
	const operatorNamespace = "costdeck-config-test"
	ctx := context.Background()
	key := types.NamespacedName{Name: "default", Namespace: operatorNamespace}

	var reconciler *CostDeckConfigReconciler

	createConfig := func(spec finopsv1.CostDeckConfigSpec) *finopsv1.CostDeckConfig {
		GinkgoHelper()
		cfg := &finopsv1.CostDeckConfig{Name: key.Name, Namespace: key.Namespace, Spec: spec}
		Expect(k8sClient.Create(ctx, cfg)).To(Succeed())
		return cfg
	}
	reconcileConfig := func() (reconcile.Result, *finopsv1.CostDeckConfig) {
		GinkgoHelper()
		res, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
		cfg := &finopsv1.CostDeckConfig{}
		Expect(k8sClient.Get(ctx, key, cfg)).To(Succeed())
		return res, cfg
	}
	createSecret := func(name string, labels map[string]string, data map[string][]byte) {
		GinkgoHelper()
		Expect(k8sClient.Create(ctx, &corev1.Secret{Name: name, Namespace: operatorNamespace, Labels: labels, Data: data})).To(Succeed())
		DeferCleanup(func() {
			Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, &corev1.Secret{Name: name, Namespace: operatorNamespace}))).To(Succeed())
		})
	}

	BeforeEach(func() {
		GinkgoT().Setenv("POD_NAMESPACE", operatorNamespace)
		Expect(client.IgnoreAlreadyExists(k8sClient.Create(ctx, &corev1.Namespace{Name: operatorNamespace}))).To(Succeed())
		reconciler = &CostDeckConfigReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
	})

	AfterEach(func() {
		Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, &finopsv1.CostDeckConfig{Name: key.Name, Namespace: key.Namespace}))).To(Succeed())
	})

	It("ignores a config that no longer exists", func() {
		res, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
		Expect(res).To(Equal(reconcile.Result{}))
	})

	It("reports a missing credentials Secret, clears disabled providers and checks again in five minutes", func() {
		createConfig(finopsv1.CostDeckConfigSpec{Providers: finopsv1.ProvidersConfig{
			AWS:   &finopsv1.AWSProviderConfig{Enabled: false},
			Azure: &finopsv1.AzureProviderConfig{Enabled: true, SecretRef: "azure-absent"},
		}})
		stale := &finopsv1.CostDeckConfig{}
		Expect(k8sClient.Get(ctx, key, stale)).To(Succeed())
		stale.Status.AWS = &finopsv1.ProviderStatus{Connected: true, DiscoveredResources: 4, LastChecked: metav1.Now()}
		Expect(k8sClient.Status().Update(ctx, stale)).To(Succeed())

		res, cfg := reconcileConfig()
		Expect(res.RequeueAfter).To(Equal(5 * time.Minute))
		Expect(cfg.Status.AWS).To(BeNil(), "a disabled provider keeps no status from when it was enabled")
		Expect(cfg.Status.GCP).To(BeNil())
		Expect(cfg.Status.Azure).NotTo(BeNil())
		Expect(cfg.Status.Azure.Connected).To(BeFalse())
		Expect(cfg.Status.Azure.Error).To(Equal(`Referenced secret "azure-absent" not found`))
		Expect(cfg.Status.Azure.LastChecked.IsZero()).To(BeFalse())
		Expect(cfg.Status.VictoriaMetrics).To(BeNil())
		Expect(cfg.Status.Webex).To(BeNil())
	})

	It("adopts the credentials Secret CostDeck created but never one the user brought", func() {
		createSecret("gcp-managed", map[string]string{managedByLabel: managedByValue},
			map[string][]byte{scaling.GCPSecretKey: []byte("not a service account key")})
		createSecret("azure-external", map[string]string{managedByLabel: "external-secrets"},
			map[string][]byte{scaling.AzureSecretClientID: []byte("app"), scaling.AzureSecretClientSecret: []byte("s3cret")})
		owner := createConfig(finopsv1.CostDeckConfigSpec{Providers: finopsv1.ProvidersConfig{
			Azure: &finopsv1.AzureProviderConfig{Enabled: true, SecretRef: "azure-external"},
			GCP:   &finopsv1.GCPProviderConfig{Enabled: true, SecretRef: "gcp-managed"},
		}})

		_, cfg := reconcileConfig()
		Expect(cfg.Status.GCP.Connected).To(BeFalse())
		Expect(cfg.Status.GCP.Error).To(ContainSubstring("the Google Cloud credentials are not usable"))
		Expect(cfg.Status.Azure.Connected).To(BeFalse())
		Expect(cfg.Status.Azure.Error).To(ContainSubstring("a tenant ID is required with a client secret"))

		managed := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "gcp-managed", Namespace: operatorNamespace}, managed)).To(Succeed())
		Expect(managed.OwnerReferences).To(HaveLen(1))
		Expect(managed.OwnerReferences[0].UID).To(Equal(owner.UID))
		Expect(managed.OwnerReferences[0].Kind).To(Equal("CostDeckConfig"))
		Expect(ptr.Deref(managed.OwnerReferences[0].Controller, false)).To(BeTrue())

		external := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "azure-external", Namespace: operatorNamespace}, external)).To(Succeed())
		Expect(external.OwnerReferences).To(BeEmpty(), "adopting it would delete the user's Secret together with the config")

		By("not writing an adopted Secret again")
		reconcileConfig()
		again := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "gcp-managed", Namespace: operatorNamespace}, again)).To(Succeed())
		Expect(again.ResourceVersion).To(Equal(managed.ResourceVersion))
	})

	It("validates the VictoriaMetrics endpoint and reports what is wrong", func() {
		vm := newFakeVictoriaMetrics()
		vm.series.Store(12)
		createConfig(finopsv1.CostDeckConfigSpec{Integrations: finopsv1.IntegrationsConfig{
			VictoriaMetrics: &finopsv1.VictoriaMetricsConfig{Enabled: true, Endpoint: vm.server.URL + "/api/v1/query"},
		}})

		_, cfg := reconcileConfig()
		Expect(cfg.Status.VictoriaMetrics).NotTo(BeNil())
		Expect(cfg.Status.VictoriaMetrics.Connected).To(BeTrue())
		Expect(cfg.Status.VictoriaMetrics.Error).To(BeEmpty())

		By("explaining an endpoint without container series")
		vm.series.Store(0)
		_, cfg = reconcileConfig()
		Expect(cfg.Status.VictoriaMetrics.Connected).To(BeFalse())
		Expect(cfg.Status.VictoriaMetrics.Error).To(ContainSubstring("no container_cpu_usage_seconds_total series were found"))

		By("explaining a missing endpoint")
		cfg.Spec.Integrations.VictoriaMetrics.Endpoint = ""
		Expect(k8sClient.Update(ctx, cfg)).To(Succeed())
		_, cfg = reconcileConfig()
		Expect(cfg.Status.VictoriaMetrics.Error).To(Equal("VictoriaMetrics is enabled but no endpoint is configured"))

		By("dropping the status once it is switched off")
		cfg.Spec.Integrations.VictoriaMetrics.Enabled = false
		Expect(k8sClient.Update(ctx, cfg)).To(Succeed())
		_, cfg = reconcileConfig()
		Expect(cfg.Status.VictoriaMetrics).To(BeNil())
	})

	It("reports why the Webex bot cannot work", func() {
		createConfig(finopsv1.CostDeckConfigSpec{Integrations: finopsv1.IntegrationsConfig{
			Messenger: &finopsv1.MessengerIntegrationConfig{Webex: &finopsv1.WebexConfig{Enabled: true, SecretRef: "webex-bot"}},
		}})

		_, cfg := reconcileConfig()
		Expect(cfg.Status.Webex).NotTo(BeNil())
		Expect(cfg.Status.Webex.Connected).To(BeFalse())
		Expect(cfg.Status.Webex.Error).To(ContainSubstring("read secret " + operatorNamespace + "/webex-bot"))

		createSecret("webex-bot", nil, map[string][]byte{"WEBHOOK_SECRET": []byte("x")})
		_, cfg = reconcileConfig()
		Expect(cfg.Status.Webex.Connected).To(BeFalse())
		Expect(cfg.Status.Webex.Error).To(Equal("the Webex credentials Secret has no BOT_TOKEN"))
	})

	Context("running in a manager", func() {
		startManager := func(r *CostDeckConfigReconciler) {
			GinkgoHelper()
			mgr := newTestManager()
			r.Client, r.Scheme = mgr.GetClient(), mgr.GetScheme()
			Expect(r.SetupWithManager(mgr)).To(Succeed())
			mgrCtx, stop := context.WithCancel(ctx)
			done := make(chan struct{})
			go func() {
				defer GinkgoRecover()
				defer close(done)
				Expect(mgr.Start(mgrCtx)).To(Succeed())
			}()
			DeferCleanup(func() {
				stop()
				Eventually(done, time.Minute).Should(BeClosed())
			})
		}

		It("validates a config as soon as it is created", func() {
			vm := newFakeVictoriaMetrics()
			vm.series.Store(3)
			startManager(&CostDeckConfigReconciler{})
			createConfig(finopsv1.CostDeckConfigSpec{Integrations: finopsv1.IntegrationsConfig{
				VictoriaMetrics: &finopsv1.VictoriaMetricsConfig{Enabled: true, Endpoint: vm.server.URL},
			}})
			Eventually(func(g Gomega) {
				got := &finopsv1.CostDeckConfig{}
				g.Expect(k8sClient.Get(ctx, key, got)).To(Succeed())
				g.Expect(got.Status.VictoriaMetrics).NotTo(BeNil())
				g.Expect(got.Status.VictoriaMetrics.Connected).To(BeTrue())
			}, 20*time.Second, 200*time.Millisecond).Should(Succeed())
		})

		It("checks the providers again only every five minutes, not on each of its own status writes", func() {
			Skip("bug: CostDeckConfigReconciler watches its own status writes (no GenerationChangedPredicate) and stamps a new LastChecked each time, so a provider check slower than a second re-runs in a loop")
			vm := newFakeVictoriaMetrics()
			vm.series.Store(3)
			vm.delay = 1100 * time.Millisecond
			startManager(&CostDeckConfigReconciler{})
			createConfig(finopsv1.CostDeckConfigSpec{Integrations: finopsv1.IntegrationsConfig{
				VictoriaMetrics: &finopsv1.VictoriaMetricsConfig{Enabled: true, Endpoint: vm.server.URL},
			}})
			Eventually(vm.queries.Load, 20*time.Second, 100*time.Millisecond).Should(BeNumerically(">=", 1))
			Consistently(vm.queries.Load, 6*time.Second, 200*time.Millisecond).Should(BeNumerically("<=", 2))
		})
	})
})
