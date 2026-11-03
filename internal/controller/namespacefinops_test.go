package controller

import (
	"context"
	"errors"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/metrics"
)

// fixedUsage is a metrics source that always reports the same usage.
type fixedUsage struct{}

func (fixedUsage) Name() string { return metrics.SourceMetricsServer }
func (fixedUsage) NamespaceUsage(context.Context, string) (metrics.Usage, error) {
	return metrics.Usage{CPU: resource.MustParse("100m"), Memory: resource.MustParse("64Mi")}, nil
}
func (fixedUsage) PodUsage(context.Context, string) (map[string]metrics.Usage, error) {
	return map[string]metrics.Usage{}, nil
}

var _ = Describe("NamespaceFinOps sampling", func() {
	ctx := context.Background()
	key := types.NamespacedName{Name: "batch-sampling", Namespace: "default"}

	It("samples every minute but writes the status in batches", func() {
		Expect(k8sClient.Create(ctx, &finopsv1.NamespaceFinOps{
			Name: key.Name, Namespace: key.Namespace,
			Spec: finopsv1.NamespaceFinOpsSpec{TargetNamespace: "batch-sampling-target"},
		})).To(Succeed())
		r := &NamespaceFinOpsReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), Metrics: metrics.NewProvider(k8sClient, fixedUsage{})}
		get := func() finopsv1.NamespaceFinOps {
			var o finopsv1.NamespaceFinOps
			Expect(k8sClient.Get(ctx, key, &o)).To(Succeed())
			return o
		}
		minuteLater := func() { r.sampledAt[key.Name] = time.Now().Add(-61 * time.Second) }

		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
		first := get()
		Expect(first.Status.History).To(HaveLen(1), "the first sample is written at once")

		// A reconcile within the minute takes no sample.
		res, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.RequeueAfter).To(BeNumerically(">", 50*time.Second))

		minuteLater()
		_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
		Expect(get().ResourceVersion).To(Equal(first.ResourceVersion), "a sample before the flush is only buffered")
		Expect(r.pending[key.Name]).To(HaveLen(1))

		minuteLater()
		r.FlushEvery = time.Second
		_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
		Expect(get().Status.History).To(HaveLen(3), "one write carries every buffered sample")
		Expect(r.pending[key.Name]).To(BeEmpty())

		Expect(k8sClient.Delete(ctx, &finopsv1.NamespaceFinOps{Name: key.Name, Namespace: key.Namespace})).To(Succeed())
	})
})

type switchableUsage struct{ failing atomic.Bool }

func (*switchableUsage) Name() string { return metrics.SourceMetricsServer }
func (s *switchableUsage) NamespaceUsage(context.Context, string) (metrics.Usage, error) {
	if s.failing.Load() {
		return metrics.Usage{}, errors.New("metrics.k8s.io is not served")
	}
	return metrics.Usage{CPU: resource.MustParse("100m"), Memory: resource.MustParse("64Mi")}, nil
}
func (*switchableUsage) PodUsage(context.Context, string) (map[string]metrics.Usage, error) {
	return map[string]metrics.Usage{}, nil
}

var _ = Describe("NamespaceFinOps lifecycle", func() {
	ctx := context.Background()
	key := types.NamespacedName{Name: "lifecycle-sampling", Namespace: "default"}
	create := func() {
		GinkgoHelper()
		Expect(k8sClient.Create(ctx, &finopsv1.NamespaceFinOps{
			Name: key.Name, Namespace: key.Namespace,
			Spec: finopsv1.NamespaceFinOpsSpec{TargetNamespace: "lifecycle-sampling-target"},
		})).To(Succeed())
	}
	get := func() *finopsv1.NamespaceFinOps {
		GinkgoHelper()
		o := &finopsv1.NamespaceFinOps{}
		Expect(k8sClient.Get(ctx, key, o)).To(Succeed())
		return o
	}
	remove := func() {
		GinkgoHelper()
		Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, &finopsv1.NamespaceFinOps{Name: key.Name, Namespace: key.Namespace}))).To(Succeed())
		Eventually(func() bool { return apierrors.IsNotFound(k8sClient.Get(ctx, key, &finopsv1.NamespaceFinOps{})) }).Should(BeTrue())
	}
	AfterEach(remove)

	It("forgets the samples of a deleted object, so one re-created under its name starts afresh", func() {
		source := &switchableUsage{}
		r := &NamespaceFinOpsReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), Metrics: metrics.NewProvider(k8sClient, source), FlushEvery: time.Hour}
		create()
		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
		r.sampledAt[key.Name] = time.Now().Add(-61 * time.Second)
		_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
		Expect(r.pending[key.Name]).To(HaveLen(1), "a sample waits for the next flush")

		remove()
		res, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
		Expect(res).To(Equal(reconcile.Result{}))
		Expect(r.pending).NotTo(HaveKey(key.Name))
		Expect(r.sampledAt).NotTo(HaveKey(key.Name))

		create()
		res, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.RequeueAfter).To(Equal(time.Minute))
		Expect(get().Status.History).To(HaveLen(1), "the new object gets its first sample at once and none of the old one's")
	})

	It("reports unavailable metrics once and recovers when they come back", func() {
		source := &switchableUsage{}
		source.failing.Store(true)
		r := &NamespaceFinOpsReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), Metrics: metrics.NewProvider(k8sClient, source)}
		create()

		res, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.RequeueAfter).To(Equal(time.Minute))
		failed := get()
		cond := meta.FindStatusCondition(failed.Status.Conditions, ConditionMetricsAvailable)
		Expect(cond).NotTo(BeNil())
		Expect(cond.Status).To(Equal(metav1.ConditionFalse))
		Expect(cond.Reason).To(Equal("Unavailable"))
		Expect(cond.Message).To(Equal("metrics.k8s.io is not served"))
		Expect(failed.Status.History).To(BeEmpty())

		_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
		Expect(get().ResourceVersion).To(Equal(failed.ResourceVersion), "an unchanged failure is not written again")

		source.failing.Store(false)
		_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
		recovered := get()
		Expect(meta.IsStatusConditionTrue(recovered.Status.Conditions, ConditionMetricsAvailable)).To(BeTrue())
		Expect(meta.FindStatusCondition(recovered.Status.Conditions, ConditionMetricsAvailable).Reason).To(Equal("MetricsServer"))
		Expect(recovered.Status.History).To(HaveLen(1))
	})
})

func runningPod(phase corev1.PodPhase, containers ...corev1.Container) corev1.Pod {
	return corev1.Pod{Spec: corev1.PodSpec{Containers: containers}, Status: corev1.PodStatus{Phase: phase}}
}

func container(requests, limits corev1.ResourceList) corev1.Container {
	return corev1.Container{Name: "c", Resources: corev1.ResourceRequirements{Requests: requests, Limits: limits}}
}

func cpuMem(cpu, mem string) corev1.ResourceList {
	return corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(cpu), corev1.ResourceMemory: resource.MustParse(mem)}
}

func TestSumPodsCountsOnlyRunningPods(t *testing.T) {
	totals := sumPods([]corev1.Pod{
		runningPod(corev1.PodRunning, container(cpuMem("250m", "256Mi"), cpuMem("500m", "512Mi")), container(cpuMem("250m", "256Mi"), cpuMem("1", "1Gi"))),
		runningPod(corev1.PodPending, container(cpuMem("4", "8Gi"), nil)),
		runningPod(corev1.PodSucceeded, container(nil, nil)),
	})
	if got := totals.requests.cpu.String(); got != "500m" {
		t.Errorf("CPU requests = %s, want 500m", got)
	}
	if got := totals.requests.mem.String(); got != "512Mi" {
		t.Errorf("memory requests = %s, want 512Mi", got)
	}
	if got := totals.limits.cpu.String(); got != "1500m" {
		t.Errorf("CPU limits = %s, want 1500m", got)
	}
	if totals.missingRequests || totals.missingLimits {
		t.Errorf("pods that are not running must not count: %+v", totals)
	}

	partial := sumPods([]corev1.Pod{runningPod(corev1.PodRunning,
		container(corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m")}, nil))})
	if !partial.missingRequests || !partial.missingLimits {
		t.Errorf("a container without a memory request and limits must be flagged: %+v", partial)
	}
}

func TestNamespaceInsights(t *testing.T) {
	sized := podTotals{requests: resourcePair{cpu: resource.MustParse("1"), mem: resource.MustParse("1Gi")}}
	for name, c := range map[string]struct {
		totals  podTotals
		cpu     string
		mem     string
		hasPods bool
		want    []string
	}{
		"well sized":              {sized, "800m", "900Mi", true, []string{"Optimized"}},
		"usage under 30% of both": {sized, "100m", "100Mi", true, []string{"Overprovisioned CPU", "Overprovisioned RAM"}},
		"usage at 30% is fine":    {sized, "300m", "900Mi", true, []string{"Optimized"}},
		"missing requests and limits": {podTotals{missingRequests: true, missingLimits: true}, "0", "0", true,
			[]string{"Missing Requests", "Uncapped"}},
		"no pods": {podTotals{}, "0", "0", false, nil},
	} {
		got := namespaceInsights(c.totals, resource.MustParse(c.cpu), resource.MustParse(c.mem), c.hasPods)
		if !slices.Equal(got, c.want) {
			t.Errorf("%s: namespaceInsights() = %v, want %v", name, got, c.want)
		}
	}
}

func TestMetricsConditionNamesTheSourceThatAnswered(t *testing.T) {
	for name, c := range map[string]struct {
		source  metrics.Result
		reason  string
		message string
	}{
		"VictoriaMetrics": {metrics.Result{Source: metrics.SourceVictoriaMetrics}, "VictoriaMetrics", "Usage collected from VictoriaMetrics"},
		"fallback":        {metrics.Result{Source: metrics.SourceMetricsServer, Degraded: errors.New("401 Unauthorized")}, "MetricsServerFallback", "VictoriaMetrics is enabled but failed, using metrics-server: 401 Unauthorized"},
		"metrics-server":  {metrics.Result{Source: metrics.SourceMetricsServer}, "MetricsServer", "Usage collected from metrics-server"},
	} {
		cond := metricsCondition(c.source, 7)
		if cond.Type != ConditionMetricsAvailable || cond.Status != metav1.ConditionTrue || cond.Reason != c.reason || cond.Message != c.message || cond.ObservedGeneration != 7 {
			t.Errorf("%s: metricsCondition() = %+v", name, cond)
		}
	}
}
