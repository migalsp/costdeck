package controller

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
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
			ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace},
			Spec:       finopsv1.NamespaceFinOpsSpec{TargetNamespace: "batch-sampling-target"},
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

		Expect(k8sClient.Delete(ctx, &finopsv1.NamespaceFinOps{ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace}})).To(Succeed())
	})
})
