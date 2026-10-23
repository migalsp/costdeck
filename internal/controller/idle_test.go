package controller

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/metrics"
	"github.com/migalsp/costdeck-operator/internal/scaling"
)

// fakeRequests answers every namespace with the same traffic and GPU use.
type fakeRequests struct {
	requests float64
	known    bool
	gpuPeak  float64
	gpuKnown bool
	err      error
	asked    []string
}

func (f *fakeRequests) NamespaceRequests(_ context.Context, ns string, _ time.Duration) (float64, bool, error) {
	f.asked = append(f.asked, ns)
	return f.requests, f.known, f.err
}

func (f *fakeRequests) NamespaceGPUPeak(_ context.Context, _ string, _ time.Duration) (float64, bool, error) {
	return f.gpuPeak, f.gpuKnown, f.err
}

var _ = Describe("Idle auto-sleep", func() {
	ctx := context.Background()
	open := []finopsv1.ScalingSchedule{{Days: []int{0, 1, 2, 3, 4, 5, 6}, StartTime: "00:00", EndTime: "23:59", Timezone: "UTC"}}
	minute := &metav1.Duration{Duration: time.Minute}

	newNamespace := func(name string) {
		GinkgoHelper()
		Expect(client.IgnoreAlreadyExists(k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}))).To(Succeed())
	}
	newGroup := func(name string, spec finopsv1.ScalingGroupSpec) types.NamespacedName {
		GinkgoHelper()
		g := &finopsv1.ScalingGroup{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"}, Spec: spec}
		Expect(k8sClient.Create(ctx, g)).To(Succeed())
		DeferCleanup(func() { Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, g))).To(Succeed()) })
		return client.ObjectKeyFromObject(g)
	}
	reconcileGroup := func(key types.NamespacedName, requests RequestCounter) *finopsv1.ScalingGroup {
		GinkgoHelper()
		// A fresh reconciler each time, so no measurement is held back for a minute.
		r := &ScalingGroupReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), Engine: &scaling.Engine{Client: k8sClient}, Recorder: events.NewFakeRecorder(100), Requests: requests}
		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
		out := &finopsv1.ScalingGroup{}
		Expect(k8sClient.Get(ctx, key, out)).To(Succeed())
		return out
	}
	// upFor makes the group look up for longer than its idle period.
	upFor := func(g *finopsv1.ScalingGroup, d time.Duration) {
		GinkgoHelper()
		Expect(g.Status.Phase).To(Equal(scaling.PhaseScaledUp))
		g.Status.LastAction = metav1.NewTime(time.Now().Add(-d))
		Expect(k8sClient.Status().Update(ctx, g)).To(Succeed())
	}
	idle := func(conds []metav1.Condition) *metav1.Condition {
		return meta.FindStatusCondition(conds, ConditionIdle)
	}

	BeforeEach(func() { GinkgoT().Setenv("POD_NAMESPACE", "default") })

	It("puts an unused group to sleep until a manual start", func() {
		newNamespace("idle-shop")
		key := newGroup("idle-shop", finopsv1.ScalingGroupSpec{Category: "Test", Namespaces: []string{"idle-shop"}, Schedules: open, IdleAfter: minute})
		quiet := &fakeRequests{known: true}

		By("not counting before the namespaces have been up for the idle period")
		g := reconcileGroup(key, quiet)
		Expect(g.Status.Mode).To(Equal(scaling.ModeSchedule))
		Expect(idle(g.Status.Conditions).Reason).To(Equal("Watching"))
		Expect(quiet.asked).To(BeEmpty())

		By("sleeping once nobody used them for that long")
		upFor(g, 2*time.Minute)
		g = reconcileGroup(key, quiet)
		Expect(quiet.asked).To(Equal([]string{"idle-shop"}))
		Expect(g.Status.Mode).To(Equal(scaling.ModeIdle))
		Expect(g.Status.DesiredState).To(Equal(scaling.StateDown))
		Expect(g.Status.IdleUntil).NotTo(BeNil())
		Expect(g.Status.IdleUntil.Time).To(BeTemporally("~", time.Now().Add(idleSleepLongest), time.Minute),
			"an always-open schedule never changes, so the sleep lasts at most a week")
		Expect(g.Status.Phase).To(Equal(scaling.PhaseScaledDown))
		cond := idle(g.Status.Conditions)
		Expect(cond.Status).To(Equal(metav1.ConditionTrue))
		Expect(cond.Reason).To(Equal("Unused"))
		Expect(meta.FindStatusCondition(g.Status.Conditions, ConditionManualOverride).Reason).To(Equal("Idle"))

		By("staying asleep without counting again")
		g = reconcileGroup(key, quiet)
		Expect(g.Status.Mode).To(Equal(scaling.ModeIdle))
		Expect(quiet.asked).To(HaveLen(1))

		By("waking with a manual start, which ends the sleep for good")
		g.Spec.Active = new(true)
		Expect(k8sClient.Update(ctx, g)).To(Succeed())
		g = reconcileGroup(key, quiet)
		Expect(g.Status.Mode).To(Equal(scaling.ModeManualUp))
		Expect(g.Status.IdleUntil).To(BeNil())
		Expect(idle(g.Status.Conditions).Reason).To(Equal("Watching"))

		By("dropping it all when idleAfter is removed")
		g.Spec.Active, g.Spec.IdleAfter = nil, nil
		Expect(k8sClient.Update(ctx, g)).To(Succeed())
		g = reconcileGroup(key, quiet)
		Expect(idle(g.Status.Conditions)).To(BeNil())
		Expect(g.Status.Mode).To(Equal(scaling.ModeSchedule))
	})

	It("keeps a group up while it is used, without traffic data, or without VictoriaMetrics", func() {
		newNamespace("idle-busy")
		key := newGroup("idle-busy", finopsv1.ScalingGroupSpec{Category: "Test", Namespaces: []string{"idle-busy"}, Schedules: open, IdleAfter: minute})
		g := reconcileGroup(key, nil)
		upFor(g, 2*time.Minute)

		for _, c := range []struct {
			requests *fakeRequests
			reason   string
		}{
			{&fakeRequests{requests: 3, known: true}, "InUse"},
			{&fakeRequests{known: false}, "NoTrafficData"},
			{&fakeRequests{err: metrics.ErrNoHistory}, "NoVictoriaMetrics"},
		} {
			g = reconcileGroup(key, c.requests)
			Expect(g.Status.Mode).To(Equal(scaling.ModeSchedule), c.reason)
			Expect(g.Status.IdleUntil).To(BeNil(), c.reason)
			Expect(idle(g.Status.Conditions).Reason).To(Equal(c.reason))
		}
	})

	It("puts a namespace whose GPUs sit idle to sleep, and keeps one with busy GPUs up", func() {
		newNamespace("idle-train")
		key := newGroup("idle-train", finopsv1.ScalingGroupSpec{Category: "Test", Namespaces: []string{"idle-train"}, Schedules: open, IdleAfter: minute})
		g := reconcileGroup(key, nil)
		upFor(g, 2*time.Minute)

		By("staying up while a GPU works, even without requests")
		g = reconcileGroup(key, &fakeRequests{known: true, gpuPeak: 40, gpuKnown: true})
		Expect(g.Status.Mode).To(Equal(scaling.ModeSchedule))
		Expect(idle(g.Status.Conditions).Message).To(Equal("Their GPUs were busy in the last 1m0s."))

		By("sleeping once its GPUs stay idle, with no request metrics at all")
		g = reconcileGroup(key, &fakeRequests{gpuPeak: 2, gpuKnown: true})
		Expect(g.Status.Mode).To(Equal(scaling.ModeIdle))
		Expect(idle(g.Status.Conditions).Message).To(HavePrefix("The GPUs of idle-train stayed idle for 1m0s"))
	})

	It("keeps a group up while groups that depend on it need it", func() {
		newNamespace("idle-db")
		newNamespace("idle-app")
		db := newGroup("idle-db", finopsv1.ScalingGroupSpec{Category: "Test", Namespaces: []string{"idle-db"}, Schedules: open, IdleAfter: minute})
		newGroup("idle-app", finopsv1.ScalingGroupSpec{Category: "Test", Namespaces: []string{"idle-app"}, Schedules: open, DependsOn: []string{"idle-db"}})
		quiet := &fakeRequests{known: true}
		g := reconcileGroup(db, quiet)
		upFor(g, 2*time.Minute)
		g = reconcileGroup(db, quiet)
		Expect(g.Status.RequiredBy).To(Equal([]string{"idle-app"}))
		Expect(g.Status.Mode).To(Equal(scaling.ModeSchedule))
		Expect(quiet.asked).To(BeEmpty(), "a needed group is not measured")
	})

	It("puts an unused ScalingConfig namespace to sleep", func() {
		newNamespace("idle-lab")
		c := &finopsv1.ScalingConfig{ObjectMeta: metav1.ObjectMeta{Name: "idle-lab", Namespace: "default"},
			Spec: finopsv1.ScalingConfigSpec{TargetNamespace: "idle-lab", Schedules: open, IdleAfter: minute}}
		Expect(k8sClient.Create(ctx, c)).To(Succeed())
		DeferCleanup(func() { Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, c))).To(Succeed()) })
		key := client.ObjectKeyFromObject(c)
		quiet := &fakeRequests{known: true}
		reconcileConfig := func() *finopsv1.ScalingConfig {
			GinkgoHelper()
			r := &ScalingConfigReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), Engine: &scaling.Engine{Client: k8sClient}, Requests: quiet}
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
			out := &finopsv1.ScalingConfig{}
			Expect(k8sClient.Get(ctx, key, out)).To(Succeed())
			return out
		}
		Eventually(func() string { return reconcileConfig().Status.Phase }).Should(Equal(scaling.PhaseScaledUp))
		c = reconcileConfig()
		c.Status.LastAction = metav1.NewTime(time.Now().Add(-2 * time.Minute))
		Expect(k8sClient.Status().Update(ctx, c)).To(Succeed())

		c = reconcileConfig()
		Expect(c.Status.Mode).To(Equal(scaling.ModeIdle))
		Expect(c.Status.IdleUntil).NotTo(BeNil())
		Expect(idle(c.Status.Conditions).Reason).To(Equal("Unused"))
		c = reconcileConfig()
		Expect(c.Status.Mode).To(Equal(scaling.ModeIdle), "the sleep holds on the next reconcile")
		Expect(c.Status.Phase).To(Equal(scaling.PhaseScaledDown))
	})
})
