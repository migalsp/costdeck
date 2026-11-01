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
	"slices"
	"sync"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/events"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/scaling"
)

var _ = Describe("ScalingGroup Controller", func() {
	Context("When reconciling a resource", func() {
		const resourceName = "test-resource"

		ctx := context.Background()

		typeNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: "default",
		}
		scalinggroup := &finopsv1.ScalingGroup{}

		BeforeEach(func() {
			By("creating the custom resource for the Kind ScalingGroup")
			err := k8sClient.Get(ctx, typeNamespacedName, scalinggroup)
			if err != nil && errors.IsNotFound(err) {
				resource := &finopsv1.ScalingGroup{
					Name:      resourceName,
					Namespace: "default",
					Spec: finopsv1.ScalingGroupSpec{
						Namespaces: []string{"default"},
					},
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			}
		})

		AfterEach(func() {

			resource := &finopsv1.ScalingGroup{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			Expect(err).NotTo(HaveOccurred())

			By("Cleanup the specific resource instance ScalingGroup")
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
		})
		It("should successfully reconcile the resource and update its phase", func() {
			By("Reconciling the created resource")
			controllerReconciler := &ScalingGroupReconciler{
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
				Engine:   &scaling.Engine{Client: k8sClient},
				Recorder: events.NewFakeRecorder(100),
			}

			// First Reconcile - initializes LastAction
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			// Add Target Replicas to bypass straight to complete state
			var scalinggroup finopsv1.ScalingGroup
			Expect(k8sClient.Get(ctx, typeNamespacedName, &scalinggroup)).To(Succeed())
			scalinggroup.Status.Phase = "ScalingUp"
			scalinggroup.Status.NamespacesReady = 0
			scalinggroup.Status.NamespacesTotal = 1
			Expect(k8sClient.Status().Update(ctx, &scalinggroup)).To(Succeed())

			// Second Reconcile - tests PhaseTransition and ScalingProgress
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

		})

		It("should explain in the status what drives the desired state", func() {
			controllerReconciler := &ScalingGroupReconciler{
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
				Engine:   &scaling.Engine{Client: k8sClient},
				Recorder: events.NewFakeRecorder(100),
			}

			By("reporting the fail-safe when there is no schedule")
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())
			var group finopsv1.ScalingGroup
			Expect(k8sClient.Get(ctx, typeNamespacedName, &group)).To(Succeed())
			Expect(group.Status.Mode).To(Equal(scaling.ModeAlwaysOn))
			Expect(group.Status.DesiredState).To(Equal(scaling.StateUp))
			Expect(meta.FindStatusCondition(group.Status.Conditions, ConditionReady)).NotTo(BeNil())

			By("reporting a manual override and when it ends")
			down := false
			until := metav1.NewTime(time.Now().Add(time.Hour).Truncate(time.Second))
			group.Spec.Active = &down
			group.Spec.ActiveUntil = &until
			Expect(k8sClient.Update(ctx, &group)).To(Succeed())

			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())
			Expect(k8sClient.Get(ctx, typeNamespacedName, &group)).To(Succeed())
			Expect(group.Status.Mode).To(Equal(scaling.ModeManualDown))
			Expect(group.Status.OverrideExpiresAt).NotTo(BeNil())
			Expect(group.Status.OverrideExpiresAt.Time.Equal(until.Time)).To(BeTrue())
			override := meta.FindStatusCondition(group.Status.Conditions, ConditionManualOverride)
			Expect(override).NotTo(BeNil())
			Expect(override.Status).To(Equal(metav1.ConditionTrue))
			Expect(override.Message).To(ContainSubstring("schedule is ignored"))
		})
	})
})

type cloudScaleCall struct {
	identifier string
	active     bool
}

type fakeCloud struct {
	mu        sync.Mutex
	calls     []cloudScaleCall
	ready     map[bool]bool
	failScale map[string]error
}

func newFakeCloud() *fakeCloud {
	return &fakeCloud{ready: map[bool]bool{}, failScale: map[string]error{}}
}

func (f *fakeCloud) Name() string { return "fakecloud" }

func (f *fakeCloud) Scale(_ context.Context, target finopsv1.ExternalTarget, active bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, cloudScaleCall{target.Identifier, active})
	return f.failScale[target.Identifier]
}

func (f *fakeCloud) IsReady(_ context.Context, _ finopsv1.ExternalTarget, active bool) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ready[active], nil
}

func (f *fakeCloud) Discover(context.Context, string, map[string]string) ([]finopsv1.ExternalTarget, error) {
	return nil, nil
}

func (f *fakeCloud) setReady(active, ready bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ready[active] = ready
}

func (f *fakeCloud) scaledTo(active bool) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var ids []string
	for _, c := range f.calls {
		if c.active == active {
			ids = append(ids, c.identifier)
		}
	}
	return ids
}

var _ = Describe("ScalingGroup external targets", func() {
	const appNamespace = "ext-app"
	ctx := context.Background()
	key := types.NamespacedName{Name: "ext-group", Namespace: "default"}
	deploymentKey := types.NamespacedName{Name: "api", Namespace: appNamespace}

	var (
		cloud      *fakeCloud
		recorder   *events.FakeRecorder
		reconciler *ScalingGroupReconciler
	)

	reconcileGroup := func() (reconcile.Result, *finopsv1.ScalingGroup) {
		GinkgoHelper()
		res, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
		g := &finopsv1.ScalingGroup{}
		Expect(k8sClient.Get(ctx, key, g)).To(Succeed())
		return res, g
	}
	readyMessage := func(g *finopsv1.ScalingGroup) string {
		GinkgoHelper()
		cond := meta.FindStatusCondition(g.Status.Conditions, ConditionReady)
		Expect(cond).NotTo(BeNil())
		return cond.Message
	}
	apiReplicas := func() int32 {
		GinkgoHelper()
		d := &appsv1.Deployment{}
		Expect(k8sClient.Get(ctx, deploymentKey, d)).To(Succeed())
		return ptr.Deref(d.Spec.Replicas, -1)
	}
	setAPIRunning := func(n int32) {
		GinkgoHelper()
		d := &appsv1.Deployment{}
		Expect(k8sClient.Get(ctx, deploymentKey, d)).To(Succeed())
		d.Status.Replicas, d.Status.ReadyReplicas, d.Status.AvailableReplicas = n, n, n
		Expect(k8sClient.Status().Update(ctx, d)).To(Succeed())
	}
	createGroup := func(sequence []string, targets []finopsv1.ExternalTarget) {
		GinkgoHelper()
		Expect(k8sClient.Create(ctx, &finopsv1.ScalingGroup{
			Name: key.Name, Namespace: key.Namespace,
			Spec: finopsv1.ScalingGroupSpec{Category: "Solution", Namespaces: []string{appNamespace}, Sequence: sequence, ExternalTargets: targets},
		})).To(Succeed())
	}

	BeforeEach(func() {
		Expect(client.IgnoreAlreadyExists(k8sClient.Create(ctx, &corev1.Namespace{Name: appNamespace}))).To(Succeed())
		labels := map[string]string{"app": "api"}
		Expect(k8sClient.Create(ctx, &appsv1.Deployment{
			Name: deploymentKey.Name, Namespace: deploymentKey.Namespace,
			Spec: appsv1.DeploymentSpec{
				Replicas: ptr.To[int32](0),
				Selector: &metav1.LabelSelector{MatchLabels: labels},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: labels},
					Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "api", Image: "busybox"}}},
				},
			},
		})).To(Succeed())
		cloud = newFakeCloud()
		recorder = events.NewFakeRecorder(100)
		reconciler = &ScalingGroupReconciler{
			Client: k8sClient, Scheme: k8sClient.Scheme(), Recorder: recorder,
			Engine: &scaling.Engine{Client: k8sClient, Providers: map[string]scaling.ExternalProvider{"fakecloud": cloud}},
		}
	})

	AfterEach(func() {
		Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, &finopsv1.ScalingGroup{Name: key.Name, Namespace: key.Namespace}))).To(Succeed())
		Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, &appsv1.Deployment{Name: deploymentKey.Name, Namespace: deploymentKey.Namespace}))).To(Succeed())
	})

	It("starts a cloud database before the apps and stops it only after them", func() {
		createGroup([]string{"ext:db1", appNamespace}, []finopsv1.ExternalTarget{{Provider: "fakecloud", Type: "aurora", Identifier: "db1", Region: "eu-west-1"}})

		By("starting the database first and holding the apps back until it is available")
		res, g := reconcileGroup()
		Expect(cloud.scaledTo(true)).To(Equal([]string{"db1"}))
		Expect(apiReplicas()).To(BeZero())
		Expect(g.Status.Phase).To(Equal(scaling.PhaseScalingUp))
		Expect(readyMessage(g)).To(Equal("Waiting for: ext:db1"))
		Expect(res.RequeueAfter).To(Equal(5 * time.Second))

		By("starting the apps once the database is available")
		cloud.setReady(true, true)
		_, g = reconcileGroup()
		Expect(apiReplicas()).To(Equal(int32(1)))
		Expect(readyMessage(g)).To(Equal("Waiting for: " + appNamespace))

		setAPIRunning(1)
		res, g = reconcileGroup()
		Expect(g.Status.Phase).To(Equal(scaling.PhaseScaledUp))
		Expect(meta.IsStatusConditionTrue(g.Status.Conditions, ConditionReady)).To(BeTrue())
		Expect(res.RequeueAfter).To(BeNumerically(">", 5*time.Second))

		By("stopping the apps first and keeping the database while they still run")
		g.Spec.Active = new(false)
		Expect(k8sClient.Update(ctx, g)).To(Succeed())
		_, g = reconcileGroup()
		Expect(apiReplicas()).To(BeZero())
		Expect(cloud.scaledTo(false)).To(BeEmpty())
		Expect(g.Status.Phase).To(Equal(scaling.PhaseScalingDown))
		Expect(readyMessage(g)).To(Equal("Waiting for: " + appNamespace))

		By("stopping the database once the apps are gone")
		setAPIRunning(0)
		_, g = reconcileGroup()
		Expect(cloud.scaledTo(false)).To(Equal([]string{"db1"}))
		Expect(readyMessage(g)).To(Equal("Waiting for: ext:db1"))

		cloud.setReady(false, true)
		_, g = reconcileGroup()
		Expect(g.Status.Phase).To(Equal(scaling.PhaseScaledDown))
		Expect(meta.IsStatusConditionTrue(g.Status.Conditions, ConditionReady)).To(BeTrue())
	})

	It("keeps waiting on cloud targets it cannot resolve or scale without failing the reconcile", func() {
		cloud.failScale["flaky"] = fmt.Errorf("throttled")
		cloud.setReady(true, true)
		createGroup([]string{"ext:ghost ext:orphan ext:flaky", appNamespace}, []finopsv1.ExternalTarget{
			{Provider: "nocloud", Type: "vm", Identifier: "orphan", Region: "eu-west-1"},
			{Provider: "fakecloud", Type: "vm", Identifier: "flaky", Region: "eu-west-1"},
		})

		res, g := reconcileGroup()
		Expect(readyMessage(g)).To(Equal("Waiting for: ext:ghost, ext:orphan, ext:flaky"))
		Expect(g.Status.Phase).To(Equal(scaling.PhaseScalingUp))
		Expect(res.RequeueAfter).To(Equal(5 * time.Second))
		Expect(cloud.scaledTo(true)).To(Equal([]string{"flaky"}))
		Expect(apiReplicas()).To(BeZero(), "the next stage waits for the cloud targets")
		Expect(recorder.Events).To(Receive(Equal("Normal ScalingActive Executing Stage 1. Waiting for targets in: ext:ghost, ext:orphan, ext:flaky")))
	})

	It("starts neither the apps nor the cloud targets when a dry-run group that never scaled them is deleted", func() {
		Expect(k8sClient.Create(ctx, &finopsv1.ScalingGroup{
			Name: key.Name, Namespace: key.Namespace,
			Spec: finopsv1.ScalingGroupSpec{
				Category: "Solution", Namespaces: []string{appNamespace}, Active: new(false), DryRun: true,
				ExternalTargets: []finopsv1.ExternalTarget{{Provider: "fakecloud", Type: "aurora", Identifier: "db1", Region: "eu-west-1"}},
			},
		})).To(Succeed())
		_, g := reconcileGroup()
		Expect(g.Status.Phase).To(Equal(scaling.PhaseDryRun))
		Expect(g.Finalizers).To(ContainElement(ReleaseFinalizer))

		Expect(rawClient.Delete(ctx, &finopsv1.ScalingGroup{Name: key.Name, Namespace: key.Namespace})).To(Succeed())
		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
		Eventually(func() error {
			return k8sClient.Get(ctx, key, &finopsv1.ScalingGroup{})
		}).Should(Satisfy(errors.IsNotFound))
		Expect(cloud.scaledTo(true)).To(BeEmpty(), "the dry run never stopped the database")
		Expect(apiReplicas()).To(BeZero(), "the dry run never stopped the app")
	})
})

func groupScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(s))
	utilruntime.Must(finopsv1.AddToScheme(s))
	return s
}

func TestRelatedGroupsAreDependenciesDependentsAndNamespaceSharers(t *testing.T) {
	selector := &metav1.LabelSelector{MatchLabels: map[string]string{"env": "preview"}}
	group := func(name, ns string, namespaces, dependsOn []string) *finopsv1.ScalingGroup {
		return &finopsv1.ScalingGroup{Name: name, Namespace: ns, Spec: finopsv1.ScalingGroupSpec{Namespaces: namespaces, DependsOn: dependsOn}}
	}
	selecting := group("selects-own", "costdeck", nil, nil)
	selecting.Spec.NamespaceSelector = selector
	selecting.Status.SelectedNamespaces = []string{"own"}
	staleSelection := group("selector-removed", "costdeck", []string{"elsewhere"}, nil)
	staleSelection.Status.SelectedNamespaces = []string{"own"}
	changed := group("app", "costdeck", []string{"shared", "own"}, []string{"platform"})

	c := fake.NewClientBuilder().WithScheme(groupScheme()).WithObjects(
		changed,
		group("platform", "costdeck", []string{"infra"}, nil),
		group("consumer", "costdeck", []string{"consumer"}, []string{"app"}),
		group("sharer", "costdeck", []string{"shared"}, nil),
		selecting,
		staleSelection,
		group("unrelated", "costdeck", []string{"other"}, nil),
		group("consumer-elsewhere", "team-b", []string{"shared"}, []string{"app"}),
	).Build()
	r := &ScalingGroupReconciler{Client: c}

	related := r.relatedGroups(context.Background(), changed)
	got := make([]string, 0, len(related))
	for _, req := range related {
		if req.Namespace != "costdeck" {
			t.Errorf("a group in another namespace was enqueued: %v", req)
		}
		got = append(got, req.Name)
	}
	slices.Sort(got)
	if want := []string{"consumer", "platform", "selects-own", "sharer"}; !slices.Equal(got, want) {
		t.Errorf("relatedGroups() = %v, want %v", got, want)
	}
	if reqs := r.relatedGroups(context.Background(), &finopsv1.ScalingConfig{Name: "x", Namespace: "costdeck"}); reqs != nil {
		t.Errorf("a non-group object mapped to %v", reqs)
	}
}

var _ = Describe("ScalingGroup letting go of namespaces", func() {
	ctx := context.Background()
	key := types.NamespacedName{Name: "letting-go", Namespace: "default"}
	namespaces := []string{"letgo-a", "letgo-b"}

	var reconciler *ScalingGroupReconciler
	replicasIn := func(ns string) int32 {
		GinkgoHelper()
		d := &appsv1.Deployment{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "app", Namespace: ns}, d)).To(Succeed())
		return ptr.Deref(d.Spec.Replicas, -1)
	}
	reconcileGroup := func() {
		GinkgoHelper()
		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
	}
	cronJobSuspended := func(ns string) bool {
		GinkgoHelper()
		job := &batchv1.CronJob{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "report", Namespace: ns}, job)).To(Succeed())
		return ptr.Deref(job.Spec.Suspend, false)
	}

	BeforeEach(func() {
		reconciler = &ScalingGroupReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), Engine: &scaling.Engine{Client: k8sClient}, Recorder: events.NewFakeRecorder(100)}
		for _, ns := range namespaces {
			Expect(client.IgnoreAlreadyExists(k8sClient.Create(ctx, &corev1.Namespace{Name: ns}))).To(Succeed())
			Expect(k8sClient.Create(ctx, &batchv1.CronJob{
				Name: "report", Namespace: ns,
				Spec: batchv1.CronJobSpec{Schedule: "0 * * * *", JobTemplate: batchv1.JobTemplateSpec{Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{RestartPolicy: corev1.RestartPolicyNever, Containers: []corev1.Container{{Name: "report", Image: "busybox"}}},
				}}}},
			})).To(Succeed())
			labels := map[string]string{"app": "app"}
			Expect(k8sClient.Create(ctx, &appsv1.Deployment{
				Name: "app", Namespace: ns,
				Spec: appsv1.DeploymentSpec{
					Replicas: ptr.To[int32](3), Selector: &metav1.LabelSelector{MatchLabels: labels},
					Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "busybox"}}}},
				},
			})).To(Succeed())
		}
		Expect(k8sClient.Create(ctx, &finopsv1.ScalingGroup{
			Name: key.Name, Namespace: key.Namespace,
			Spec: finopsv1.ScalingGroupSpec{Category: "Team", Namespaces: namespaces, Active: new(false)},
		})).To(Succeed())
		reconcileGroup()
		for _, ns := range namespaces {
			Expect(replicasIn(ns)).To(BeZero())
			Expect(cronJobSuspended(ns)).To(BeTrue())
		}
	})

	AfterEach(func() {
		Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, &finopsv1.ScalingGroup{Name: key.Name, Namespace: key.Namespace}))).To(Succeed())
		for _, ns := range namespaces {
			Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, &appsv1.Deployment{Name: "app", Namespace: ns}))).To(Succeed())
			Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, &batchv1.CronJob{Name: "report", Namespace: ns}))).To(Succeed())
		}
	})

	It("restores a namespace removed from spec.namespaces, as it does for one its selector stops matching", func() {
		g := &finopsv1.ScalingGroup{}
		Expect(k8sClient.Get(ctx, key, g)).To(Succeed())
		g.Spec.Namespaces = []string{"letgo-a"}
		Expect(k8sClient.Update(ctx, g)).To(Succeed())
		reconcileGroup()
		Expect(replicasIn("letgo-a")).To(BeZero())
		Expect(replicasIn("letgo-b")).To(Equal(int32(3)))
		Expect(cronJobSuspended("letgo-a")).To(BeTrue())
		Expect(cronJobSuspended("letgo-b")).To(BeFalse())
		g = &finopsv1.ScalingGroup{}
		Expect(k8sClient.Get(ctx, key, g)).To(Succeed())
		Expect(g.Status.OriginalReplicas).NotTo(HaveKey(HavePrefix("letgo-b/")))
	})

	It("restores what it scaled down when the group is deleted", func() {
		Expect(rawClient.Delete(ctx, &finopsv1.ScalingGroup{Name: key.Name, Namespace: key.Namespace})).To(Succeed())
		Expect(k8sClient.Get(ctx, key, &finopsv1.ScalingGroup{})).To(Succeed())
		reconcileGroup()
		Eventually(func() error {
			return k8sClient.Get(ctx, key, &finopsv1.ScalingGroup{})
		}).Should(Satisfy(errors.IsNotFound))
		for _, ns := range namespaces {
			Expect(replicasIn(ns)).To(Equal(int32(3)))
			Expect(cronJobSuspended(ns)).To(BeFalse())
		}
	})

	It("adds its finalizer so a deletion waits for the release", func() {
		g := &finopsv1.ScalingGroup{}
		Expect(k8sClient.Get(ctx, key, g)).To(Succeed())
		Expect(g.Finalizers).To(ContainElement(ReleaseFinalizer))
	})

	It("does not start workloads someone stopped by hand when an up group is deleted", func() {
		g := &finopsv1.ScalingGroup{}
		Expect(k8sClient.Get(ctx, key, g)).To(Succeed())
		g.Spec.Active = new(true)
		Expect(k8sClient.Update(ctx, g)).To(Succeed())
		reconcileGroup()
		for _, ns := range namespaces {
			d := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "app", Namespace: ns}, d)).To(Succeed())
			Expect(*d.Spec.Replicas).To(Equal(int32(3)))
			d.Status.Replicas, d.Status.ReadyReplicas, d.Status.AvailableReplicas = 3, 3, 3
			Expect(k8sClient.Status().Update(ctx, d)).To(Succeed())
		}
		Eventually(func() string {
			reconcileGroup()
			Expect(k8sClient.Get(ctx, key, g)).To(Succeed())
			return g.Status.Phase
		}).Should(Equal(scaling.PhaseScaledUp))
		d := &appsv1.Deployment{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "app", Namespace: "letgo-b"}, d)).To(Succeed())
		d.Spec.Replicas = ptr.To[int32](0)
		Expect(k8sClient.Update(ctx, d)).To(Succeed())

		Expect(rawClient.Delete(ctx, &finopsv1.ScalingGroup{Name: key.Name, Namespace: key.Namespace})).To(Succeed())
		reconcileGroup()
		Eventually(func() error {
			return k8sClient.Get(ctx, key, &finopsv1.ScalingGroup{})
		}).Should(Satisfy(errors.IsNotFound))
		Expect(replicasIn("letgo-b")).To(BeZero())
		Expect(replicasIn("letgo-a")).To(Equal(int32(3)))
	})
})

var _ = Describe("ScalingGroup controller setup", func() {
	It("resolves cloud providers from the live settings unless providers are given", func() {
		r := &ScalingGroupReconciler{Client: rawClient, Scheme: rawClient.Scheme()}
		Expect(r.SetupWithManager(newTestManager())).To(Succeed())
		Expect(r.Engine).NotTo(BeNil())
		Expect(r.Engine.Client).To(BeIdenticalTo(r.Client))
		Expect(r.Engine.Providers).NotTo(BeNil())
		Expect(r.Engine.Resolver).To(BeAssignableToTypeOf(&scaling.ConfigProviderResolver{}))
		Expect(r.Recorder).NotTo(BeNil())

		given := newFakeCloud()
		kept := &ScalingGroupReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(),
			Engine: &scaling.Engine{Client: k8sClient, Providers: map[string]scaling.ExternalProvider{"fakecloud": given}}}
		Expect(kept.SetupWithManager(newTestManager())).To(Succeed())
		p, err := kept.Engine.Provider(context.Background(), "fakecloud")
		Expect(err).NotTo(HaveOccurred())
		Expect(p).To(BeIdenticalTo(given))
	})
})

var _ = Describe("Two ScalingGroups claiming the same namespace", func() {
	ctx := context.Background()
	older := types.NamespacedName{Name: "conflict-a", Namespace: "default"}
	newer := types.NamespacedName{Name: "conflict-b", Namespace: "default"}
	namespaces := []string{"conflict-shared", "conflict-own"}

	var reconciler *ScalingGroupReconciler
	replicasIn := func(ns string) int32 {
		GinkgoHelper()
		d := &appsv1.Deployment{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "app", Namespace: ns}, d)).To(Succeed())
		return ptr.Deref(d.Spec.Replicas, -1)
	}
	reconcileNewer := func() *finopsv1.ScalingGroup {
		GinkgoHelper()
		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: newer})
		Expect(err).NotTo(HaveOccurred())
		g := &finopsv1.ScalingGroup{}
		Expect(k8sClient.Get(ctx, newer, g)).To(Succeed())
		return g
	}

	BeforeEach(func() {
		reconciler = &ScalingGroupReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), Engine: &scaling.Engine{Client: k8sClient}, Recorder: events.NewFakeRecorder(100)}
		for _, ns := range namespaces {
			Expect(client.IgnoreAlreadyExists(k8sClient.Create(ctx, &corev1.Namespace{Name: ns}))).To(Succeed())
			labels := map[string]string{"app": "app"}
			Expect(k8sClient.Create(ctx, &appsv1.Deployment{
				Name: "app", Namespace: ns,
				Spec: appsv1.DeploymentSpec{
					Replicas: ptr.To[int32](3), Selector: &metav1.LabelSelector{MatchLabels: labels},
					Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "busybox"}}}},
				},
			})).To(Succeed())
		}
		Expect(k8sClient.Create(ctx, &finopsv1.ScalingGroup{
			Name: older.Name, Namespace: older.Namespace,
			Spec: finopsv1.ScalingGroupSpec{Category: "Team", Namespaces: []string{"conflict-shared"}, Active: new(true)},
		})).To(Succeed())
		Expect(k8sClient.Create(ctx, &finopsv1.ScalingGroup{
			Name: newer.Name, Namespace: newer.Namespace,
			Spec: finopsv1.ScalingGroupSpec{Category: "Team", Namespaces: namespaces, Active: new(false)},
		})).To(Succeed())
	})

	AfterEach(func() {
		for _, key := range []types.NamespacedName{older, newer} {
			Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, &finopsv1.ScalingGroup{Name: key.Name, Namespace: key.Namespace}))).To(Succeed())
		}
		for _, ns := range namespaces {
			Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, &appsv1.Deployment{Name: "app", Namespace: ns}))).To(Succeed())
		}
	})

	It("leaves the shared namespace to the older group, says so, and takes it over once that group is gone", func() {
		g := reconcileNewer()
		Expect(replicasIn("conflict-own")).To(BeZero())
		Expect(replicasIn("conflict-shared")).To(Equal(int32(3)), "the newer group must not scale a namespace the older one owns")
		Expect(g.Status.ConflictingNamespaces).To(Equal([]string{"conflict-shared"}))
		Expect(g.Status.NamespacesTotal).To(Equal(1))
		Expect(g.Status.Phase).To(Equal(scaling.PhaseScaledDown))
		conflict := meta.FindStatusCondition(g.Status.Conditions, ConditionNamespaceConflict)
		Expect(conflict).NotTo(BeNil())
		Expect(conflict.Status).To(Equal(metav1.ConditionTrue))
		Expect(conflict.Reason).To(Equal("OwnedByOlderGroup"))
		Expect(conflict.Message).To(ContainSubstring("conflict-shared (owned by conflict-a)"))

		Expect(k8sClient.Delete(ctx, &finopsv1.ScalingGroup{Name: older.Name, Namespace: older.Namespace})).To(Succeed())
		g = reconcileNewer()
		Expect(replicasIn("conflict-shared")).To(BeZero())
		Expect(g.Status.ConflictingNamespaces).To(BeEmpty())
		Expect(meta.FindStatusCondition(g.Status.Conditions, ConditionNamespaceConflict)).To(BeNil())
	})
})

func TestSetDependencyConditions(t *testing.T) {
	for name, c := range map[string]struct {
		dependsOn []string
		plan      scaling.GroupPlan
		status    metav1.ConditionStatus
		reason    string
		message   string
	}{
		"cycle":              {[]string{"b"}, scaling.GroupPlan{InCycle: true, WaitingFor: []string{"b"}}, metav1.ConditionFalse, "Cycle", "dependsOn forms a cycle and is ignored: b"},
		"missing dependency": {[]string{"ghost"}, scaling.GroupPlan{MissingDependencies: []string{"ghost"}}, metav1.ConditionFalse, "NotFound", "No such ScalingGroup: ghost"},
		"dependency down":    {[]string{"db", "mq"}, scaling.GroupPlan{WaitingFor: []string{"mq"}}, metav1.ConditionFalse, "NotScaledUp", "Not ScaledUp yet: mq"},
		"all up":             {[]string{"db", "mq"}, scaling.GroupPlan{}, metav1.ConditionTrue, "ScaledUp", "All dependencies are ScaledUp: db, mq"},
	} {
		g := &finopsv1.ScalingGroup{Spec: finopsv1.ScalingGroupSpec{DependsOn: c.dependsOn}}
		g.Generation = 4
		setDependencyConditions(g, c.plan)
		cond := meta.FindStatusCondition(g.Status.Conditions, ConditionDependenciesReady)
		if cond == nil || cond.Status != c.status || cond.Reason != c.reason || cond.Message != c.message || cond.ObservedGeneration != 4 {
			t.Errorf("%s: condition = %+v", name, cond)
		}
		if meta.FindStatusCondition(g.Status.Conditions, ConditionNamespaceConflict) != nil {
			t.Errorf("%s: a namespace conflict was reported without one", name)
		}
	}

	g := &finopsv1.ScalingGroup{}
	g.Status.Conditions = []metav1.Condition{
		{Type: ConditionDependenciesReady, Status: metav1.ConditionFalse, Reason: "NotFound"},
		{Type: ConditionNamespaceConflict, Status: metav1.ConditionTrue, Reason: "OwnedByOlderGroup"},
	}
	setDependencyConditions(g, scaling.GroupPlan{})
	if len(g.Status.Conditions) != 0 {
		t.Errorf("conditions left after dependencies and conflicts were resolved: %+v", g.Status.Conditions)
	}
}
