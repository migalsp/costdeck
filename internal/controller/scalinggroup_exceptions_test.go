package controller

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/scaling"
)

var _ = Describe("Schedule exceptions and holiday calendars", func() {
	ctx := context.Background()
	groupKey := types.NamespacedName{Name: "exc-group", Namespace: "default"}
	configKey := types.NamespacedName{Name: "default", Namespace: "default"}
	today := time.Now().UTC().Format("2006-01-02")
	// Up around the clock, so whatever is down here was taken down by an exception.
	allWeek := []finopsv1.ScalingSchedule{{Days: []int{0, 1, 2, 3, 4, 5, 6}, StartTime: "00:00", EndTime: "23:59", Timezone: "UTC"}}

	var reconciler *ScalingGroupReconciler
	reconcileGroup := func() *finopsv1.ScalingGroup {
		GinkgoHelper()
		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: groupKey})
		Expect(err).NotTo(HaveOccurred())
		g := &finopsv1.ScalingGroup{}
		Expect(k8sClient.Get(ctx, groupKey, g)).To(Succeed())
		return g
	}

	BeforeEach(func() {
		GinkgoT().Setenv("POD_NAMESPACE", "default")
		reconciler = &ScalingGroupReconciler{
			Client: k8sClient, Scheme: k8sClient.Scheme(),
			Engine: &scaling.Engine{Client: k8sClient}, Recorder: events.NewFakeRecorder(100),
		}
	})

	AfterEach(func() {
		g := &finopsv1.ScalingGroup{}
		if err := k8sClient.Get(ctx, groupKey, g); err == nil {
			Expect(k8sClient.Delete(ctx, g)).To(Succeed())
		}
		c := &finopsv1.CostDeckConfig{}
		if err := k8sClient.Get(ctx, configKey, c); err == nil {
			Expect(k8sClient.Delete(ctx, c)).To(Succeed())
		}
	})

	It("keeps a group down for today's exception and says so in its status", func() {
		Expect(k8sClient.Create(ctx, &finopsv1.ScalingGroup{
			ObjectMeta: metav1.ObjectMeta{Name: groupKey.Name, Namespace: groupKey.Namespace},
			Spec: finopsv1.ScalingGroupSpec{
				Category: "Test", Namespaces: []string{"exc-test-ns"}, Schedules: allWeek,
				Exceptions: []finopsv1.ScheduleException{{Name: "Office move", Start: today, State: "Down"}},
			},
		})).To(Succeed())

		g := reconcileGroup()
		Expect(g.Status.Mode).To(Equal(scaling.ModeException))
		Expect(g.Status.DesiredState).To(Equal(scaling.StateDown))
		Expect(g.Status.Exception).To(Equal("Office move"))
		Expect(g.Status.ExceptionEndsAt).NotTo(BeNil())
		Expect(g.Status.ExceptionEndsAt.Time).To(BeTemporally("==", time.Now().UTC().Truncate(24*time.Hour).Add(24*time.Hour)))
		cond := meta.FindStatusCondition(g.Status.Conditions, ConditionManualOverride)
		Expect(cond).NotTo(BeNil())
		Expect(cond.Reason).To(Equal("Exception"))
	})

	It("reports a missing calendar, and applies it once it exists", func() {
		Expect(k8sClient.Create(ctx, &finopsv1.ScalingGroup{
			ObjectMeta: metav1.ObjectMeta{Name: groupKey.Name, Namespace: groupKey.Namespace},
			Spec:       finopsv1.ScalingGroupSpec{Category: "Test", Namespaces: []string{"exc-test-ns"}, Schedules: allWeek, Calendars: []string{"company"}},
		})).To(Succeed())

		By("reporting the calendar as missing and staying on the schedule")
		g := reconcileGroup()
		Expect(g.Status.Mode).To(Equal(scaling.ModeSchedule))
		cond := meta.FindStatusCondition(g.Status.Conditions, ConditionExceptionsApplied)
		Expect(cond).NotTo(BeNil())
		Expect(cond.Status).To(Equal(metav1.ConditionFalse))
		Expect(cond.Message).To(ContainSubstring(`calendar "company" does not exist`))

		By("mapping a change of the settings to the groups that use calendars")
		cfg := &finopsv1.CostDeckConfig{
			ObjectMeta: metav1.ObjectMeta{Name: configKey.Name, Namespace: configKey.Namespace},
			Spec:       finopsv1.CostDeckConfigSpec{Calendars: []finopsv1.HolidayCalendar{{Name: "company", Days: []finopsv1.CalendarDay{{Date: today, Name: "Founders day"}}}}},
		}
		Expect(k8sClient.Create(ctx, cfg)).To(Succeed())
		Expect(reconciler.groupsWithCalendars(ctx, cfg)).To(ContainElement(reconcile.Request{NamespacedName: groupKey}))

		By("applying the calendar day and dropping the condition")
		g = reconcileGroup()
		Expect(g.Status.Mode).To(Equal(scaling.ModeException))
		Expect(g.Status.Exception).To(Equal("Founders day"))
		Expect(meta.FindStatusCondition(g.Status.Conditions, ConditionExceptionsApplied)).To(BeNil())
	})
})

var _ = Describe("ScalingConfig exceptions", func() {
	ctx := context.Background()
	key := types.NamespacedName{Name: "exc-config", Namespace: "default"}
	today := time.Now().UTC().Format("2006-01-02")

	AfterEach(func() {
		c := &finopsv1.ScalingConfig{}
		if err := k8sClient.Get(ctx, key, c); err == nil {
			Expect(k8sClient.Delete(ctx, c)).To(Succeed())
		}
	})

	It("keeps a namespace up for an Up exception outside its schedule", func() {
		GinkgoT().Setenv("POD_NAMESPACE", "default")
		Expect(k8sClient.Create(ctx, &finopsv1.ScalingConfig{
			ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace},
			Spec: finopsv1.ScalingConfigSpec{
				TargetNamespace: "exc-config-ns",
				// A window that never covers today.
				Schedules:  []finopsv1.ScalingSchedule{{Days: []int{int(time.Now().UTC().Weekday()+3) % 7}, StartTime: "08:00", EndTime: "09:00", Timezone: "UTC"}},
				Exceptions: []finopsv1.ScheduleException{{Name: "Demo day", Start: today, State: "Up"}},
			},
		})).To(Succeed())
		r := &ScalingConfigReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), Engine: &scaling.Engine{Client: k8sClient}}
		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
		c := &finopsv1.ScalingConfig{}
		Expect(k8sClient.Get(ctx, key, c)).To(Succeed())
		Expect(c.Status.Mode).To(Equal(scaling.ModeException))
		Expect(c.Status.DesiredState).To(Equal(scaling.StateUp))
		Expect(c.Status.Exception).To(Equal("Demo day"))
	})
})
