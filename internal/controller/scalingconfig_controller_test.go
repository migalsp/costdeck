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
	"slices"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/events"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/scaling"
)

var _ = Describe("ScalingConfig Controller", func() {
	Context("When reconciling a resource", func() {
		const resourceName = "test-resource"

		ctx := context.Background()

		typeNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: "default",
		}
		scalingconfig := &finopsv1.ScalingConfig{}

		BeforeEach(func() {
			By("creating the custom resource for the Kind ScalingConfig")
			err := k8sClient.Get(ctx, typeNamespacedName, scalingconfig)
			if err != nil && errors.IsNotFound(err) {
				resource := &finopsv1.ScalingConfig{
					Name:      resourceName,
					Namespace: "default",
					Spec: finopsv1.ScalingConfigSpec{
						TargetNamespace: "default",
					},
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			}
		})

		AfterEach(func() {
			By("Cleanup the specific resource instance ScalingConfig")
			resource := &finopsv1.ScalingConfig{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			Expect(err).NotTo(HaveOccurred())
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
		})
		It("should successfully reconcile the resource", func() {
			By("Reconciling the created resource")
			controllerReconciler := &ScalingConfigReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
				Engine: &scaling.Engine{Client: k8sClient},
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())
		})
	})
})

var _ = Describe("ScalingConfig and ScalingGroup claiming the same namespace", func() {
	const claimed = "claimed-ns"
	ctx := context.Background()
	configKey := types.NamespacedName{Name: "claimed-config", Namespace: "default"}
	groupKey := types.NamespacedName{Name: "claimer", Namespace: "default"}
	webKey := types.NamespacedName{Name: "web", Namespace: claimed}

	var (
		configReconciler *ScalingConfigReconciler
		groupReconciler  *ScalingGroupReconciler
	)

	reconcileConfig := func() (reconcile.Result, *finopsv1.ScalingConfig) {
		GinkgoHelper()
		res, err := configReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: configKey})
		Expect(err).NotTo(HaveOccurred())
		c := &finopsv1.ScalingConfig{}
		Expect(k8sClient.Get(ctx, configKey, c)).To(Succeed())
		return res, c
	}
	reconcileGroup := func() {
		GinkgoHelper()
		_, err := groupReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: groupKey})
		Expect(err).NotTo(HaveOccurred())
	}
	webReplicas := func() int32 {
		GinkgoHelper()
		d := &appsv1.Deployment{}
		Expect(k8sClient.Get(ctx, webKey, d)).To(Succeed())
		return ptr.Deref(d.Spec.Replicas, -1)
	}

	BeforeEach(func() {
		GinkgoT().Setenv("POD_NAMESPACE", "default")
		Expect(client.IgnoreAlreadyExists(k8sClient.Create(ctx, &corev1.Namespace{Name: claimed}))).To(Succeed())
		labels := map[string]string{"app": "web"}
		Expect(k8sClient.Create(ctx, &appsv1.Deployment{
			Name: webKey.Name, Namespace: webKey.Namespace,
			Spec: appsv1.DeploymentSpec{
				Replicas: ptr.To[int32](2),
				Selector: &metav1.LabelSelector{MatchLabels: labels},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: labels},
					Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "web", Image: "busybox"}}},
				},
			},
		})).To(Succeed())
		Expect(k8sClient.Create(ctx, &finopsv1.ScalingConfig{
			Name: configKey.Name, Namespace: configKey.Namespace,
			Spec: finopsv1.ScalingConfigSpec{TargetNamespace: claimed, Active: new(false)},
		})).To(Succeed())
		configReconciler = &ScalingConfigReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), Engine: &scaling.Engine{Client: k8sClient}}
		groupReconciler = &ScalingGroupReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), Engine: &scaling.Engine{Client: k8sClient}, Recorder: events.NewFakeRecorder(100)}
	})

	AfterEach(func() {
		Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, &finopsv1.ScalingGroup{Name: groupKey.Name, Namespace: groupKey.Namespace}))).To(Succeed())
		Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, &finopsv1.ScalingConfig{Name: configKey.Name, Namespace: configKey.Namespace}))).To(Succeed())
		Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, &appsv1.Deployment{Name: webKey.Name, Namespace: webKey.Namespace}))).To(Succeed())
	})

	It("lets the group win, keeps the config inert while it does, and hands the namespace back afterwards", func() {
		By("scaling the namespace down on its own while no group lists it")
		_, c := reconcileConfig()
		Expect(webReplicas()).To(BeZero())
		Expect(c.Status.Mode).To(Equal(scaling.ModeManualDown))

		By("standing back once a group lists the namespace")
		Expect(k8sClient.Create(ctx, &finopsv1.ScalingGroup{
			Name: groupKey.Name, Namespace: groupKey.Namespace,
			Spec: finopsv1.ScalingGroupSpec{Category: "Team", Namespaces: []string{claimed}},
		})).To(Succeed())
		res, c := reconcileConfig()
		Expect(res.RequeueAfter).To(Equal(5 * time.Minute))
		Expect(c.Status.Phase).To(Equal(PhaseOverriddenByGroup))
		Expect(c.Status.ScheduleStatus).To(Equal(finopsv1.ScheduleStatus{}), "the config no longer explains a desired state it does not drive")
		ready := meta.FindStatusCondition(c.Status.Conditions, ConditionReady)
		Expect(ready).NotTo(BeNil())
		Expect(ready.Status).To(Equal(metav1.ConditionFalse))
		Expect(ready.Reason).To(Equal(PhaseOverriddenByGroup))
		Expect(ready.Message).To(ContainSubstring("Namespace claimed-ns is managed by ScalingGroup claimer"))
		Expect(c.Status.LastAction.IsZero()).To(BeFalse())

		By("not fighting the group, which brings the workloads back")
		reconcileGroup()
		Expect(webReplicas()).To(Equal(int32(2)))
		_, again := reconcileConfig()
		Expect(webReplicas()).To(Equal(int32(2)))
		Expect(again.ResourceVersion).To(Equal(c.ResourceVersion), "an overridden config is not rewritten on every reconcile")

		By("taking the namespace back when the group lets go of it")
		Expect(k8sClient.Delete(ctx, &finopsv1.ScalingGroup{Name: groupKey.Name, Namespace: groupKey.Namespace})).To(Succeed())
		_, c = reconcileConfig()
		Expect(c.Status.Phase).NotTo(Equal(PhaseOverriddenByGroup))
		Expect(c.Status.Mode).To(Equal(scaling.ModeManualDown))
		Expect(webReplicas()).To(BeZero())
	})

	It("counts a namespace a group selects by label as claimed", func() {
		n := &corev1.Namespace{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: claimed}, n)).To(Succeed())
		n.Labels = map[string]string{"claim-test": "selected"}
		Expect(k8sClient.Update(ctx, n)).To(Succeed())
		DeferCleanup(func() {
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: claimed}, n)).To(Succeed())
			n.Labels = nil
			Expect(k8sClient.Update(ctx, n)).To(Succeed())
		})
		Expect(k8sClient.Create(ctx, &finopsv1.ScalingGroup{
			Name: groupKey.Name, Namespace: groupKey.Namespace,
			Spec: finopsv1.ScalingGroupSpec{Category: "Team", NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"claim-test": "selected"}}},
		})).To(Succeed())
		reconcileGroup()

		_, c := reconcileConfig()
		Expect(c.Status.Phase).To(Equal(PhaseOverriddenByGroup))
		Expect(webReplicas()).To(Equal(int32(2)), "the config must not scale a namespace the group selected")
	})
})

func TestConfigsWithCalendarsMapsSettingsChangesToConfigsThatUseCalendars(t *testing.T) {
	s := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(s))
	utilruntime.Must(finopsv1.AddToScheme(s))
	config := func(name, ns string, calendars ...string) *finopsv1.ScalingConfig {
		return &finopsv1.ScalingConfig{Name: name, Namespace: ns, Spec: finopsv1.ScalingConfigSpec{TargetNamespace: name, Calendars: calendars}}
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(
		config("uses-holidays", "costdeck", "company"),
		config("plain-schedule", "costdeck"),
		config("other-install", "team-b", "company"),
	).Build()
	r := &ScalingConfigReconciler{Client: c}

	got := r.configsWithCalendars(context.Background(), &finopsv1.CostDeckConfig{Name: "default", Namespace: "costdeck"})
	want := []reconcile.Request{{Name: "uses-holidays", Namespace: "costdeck"}}
	if !slices.Equal(got, want) {
		t.Errorf("configsWithCalendars() = %v, want %v", got, want)
	}
}

var _ = Describe("ScalingConfig controller setup", func() {
	It("builds a scaling engine on the reconciler's client when none is given", func() {
		r := &ScalingConfigReconciler{Client: rawClient, Scheme: rawClient.Scheme()}
		Expect(r.SetupWithManager(newTestManager())).To(Succeed())
		Expect(r.Engine).NotTo(BeNil())
		Expect(r.Engine.Client).To(BeIdenticalTo(r.Client))
	})
})

var _ = Describe("ScalingConfig letting go of its namespace", func() {
	ctx := context.Background()
	key := types.NamespacedName{Name: "config-letgo", Namespace: "default"}
	const ns = "config-letgo"

	var reconciler *ScalingConfigReconciler
	replicas := func() int32 {
		GinkgoHelper()
		d := &appsv1.Deployment{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "app", Namespace: ns}, d)).To(Succeed())
		return ptr.Deref(d.Spec.Replicas, -1)
	}
	reconcileConfig := func() {
		GinkgoHelper()
		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
	}

	BeforeEach(func() {
		reconciler = &ScalingConfigReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), Engine: &scaling.Engine{Client: k8sClient}, Recorder: events.NewFakeRecorder(100)}
		Expect(client.IgnoreAlreadyExists(k8sClient.Create(ctx, &corev1.Namespace{Name: ns}))).To(Succeed())
		labels := map[string]string{"app": "app"}
		Expect(k8sClient.Create(ctx, &appsv1.Deployment{
			Name: "app", Namespace: ns,
			Spec: appsv1.DeploymentSpec{
				Replicas: ptr.To[int32](2), Selector: &metav1.LabelSelector{MatchLabels: labels},
				Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "busybox"}}}},
			},
		})).To(Succeed())
		Expect(k8sClient.Create(ctx, &finopsv1.ScalingConfig{
			Name: key.Name, Namespace: key.Namespace,
			Spec: finopsv1.ScalingConfigSpec{TargetNamespace: ns, Active: new(false)},
		})).To(Succeed())
		reconcileConfig()
		Expect(replicas()).To(BeZero())
	})

	AfterEach(func() {
		Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, &finopsv1.ScalingConfig{Name: key.Name, Namespace: key.Namespace}))).To(Succeed())
		Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, &appsv1.Deployment{Name: "app", Namespace: ns}))).To(Succeed())
	})

	It("restores what it scaled down when the config is deleted", func() {
		Expect(rawClient.Delete(ctx, &finopsv1.ScalingConfig{Name: key.Name, Namespace: key.Namespace})).To(Succeed())
		reconcileConfig()
		Eventually(func() error {
			return k8sClient.Get(ctx, key, &finopsv1.ScalingConfig{})
		}).Should(Satisfy(errors.IsNotFound))
		Expect(replicas()).To(Equal(int32(2)))
	})

	It("keeps scheduling its namespace while a group only dry-runs over it", func() {
		trial := &finopsv1.ScalingGroup{Name: "config-letgo-trial", Namespace: "default",
			Spec: finopsv1.ScalingGroupSpec{Category: "Trial", Namespaces: []string{ns}, DryRun: true, Active: new(true)}}
		Expect(k8sClient.Create(ctx, trial)).To(Succeed())
		DeferCleanup(func() { Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, trial))).To(Succeed()) })

		c := &finopsv1.ScalingConfig{}
		Expect(k8sClient.Get(ctx, key, c)).To(Succeed())
		c.Spec.Active = new(true)
		Expect(k8sClient.Update(ctx, c)).To(Succeed())
		reconcileConfig()
		Expect(k8sClient.Get(ctx, key, c)).To(Succeed())
		Expect(c.Status.Phase).NotTo(Equal(PhaseOverriddenByGroup))
		Expect(replicas()).To(Equal(int32(2)))
	})

	It("refuses to move to another namespace, which would strand this one at zero", func() {
		c := &finopsv1.ScalingConfig{}
		Expect(k8sClient.Get(ctx, key, c)).To(Succeed())
		c.Spec.TargetNamespace = "somewhere-else"
		err := k8sClient.Update(ctx, c)
		Expect(errors.IsInvalid(err)).To(BeTrue(), "got %v", err)
		Expect(err.Error()).To(ContainSubstring("targetNamespace cannot change"))
	})
})

var _ = Describe("ScalingConfig workload stages", func() {
	ctx := context.Background()
	key := types.NamespacedName{Name: "config-stages", Namespace: "default"}
	const ns = "config-stages"

	var reconciler *ScalingConfigReconciler
	replicas := func(name string) int32 {
		GinkgoHelper()
		d := &appsv1.Deployment{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, d)).To(Succeed())
		return ptr.Deref(d.Spec.Replicas, -1)
	}
	reconcileConfig := func() {
		GinkgoHelper()
		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
	}
	ageTheTransition := func() {
		GinkgoHelper()
		c := &finopsv1.ScalingConfig{}
		Expect(k8sClient.Get(ctx, key, c)).To(Succeed())
		Expect(c.Status.Phase).To(Equal(scaling.PhaseScalingUp))
		c.Status.LastAction = metav1.NewTime(time.Now().Add(-2 * time.Minute))
		Expect(k8sClient.Status().Update(ctx, c)).To(Succeed())
	}

	BeforeEach(func() {
		reconciler = &ScalingConfigReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), Engine: &scaling.Engine{Client: k8sClient}, Recorder: events.NewFakeRecorder(100)}
		Expect(client.IgnoreAlreadyExists(k8sClient.Create(ctx, &corev1.Namespace{Name: ns}))).To(Succeed())
		for _, name := range []string{"db", "app"} {
			labels := map[string]string{"app": name}
			Expect(k8sClient.Create(ctx, &appsv1.Deployment{
				Name: name, Namespace: ns,
				Spec: appsv1.DeploymentSpec{
					Replicas: ptr.To[int32](1), Selector: &metav1.LabelSelector{MatchLabels: labels},
					Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: name, Image: "busybox"}}}},
				},
			})).To(Succeed())
		}
		Expect(k8sClient.Create(ctx, &finopsv1.ScalingConfig{
			Name: key.Name, Namespace: key.Namespace,
			Spec: finopsv1.ScalingConfigSpec{TargetNamespace: ns, Active: new(false), Sequence: []string{"db", "*"}},
		})).To(Succeed())
		Eventually(func() int32 {
			reconcileConfig()
			return replicas("db") + replicas("app")
		}).Should(BeZero())
		c := &finopsv1.ScalingConfig{}
		Expect(k8sClient.Get(ctx, key, c)).To(Succeed())
		c.Spec.Active = new(true)
		Expect(k8sClient.Update(ctx, c)).To(Succeed())
		reconcileConfig()
		Expect(replicas("db")).To(Equal(int32(1)))
		Expect(replicas("app")).To(BeZero())
	})

	AfterEach(func() {
		Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, &finopsv1.ScalingConfig{Name: key.Name, Namespace: key.Namespace}))).To(Succeed())
		for _, name := range []string{"db", "app"} {
			Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, &appsv1.Deployment{Name: name, Namespace: ns}))).To(Succeed())
		}
	})

	It("keeps the apps waiting for a database that is not ready, however long it takes", func() {
		ageTheTransition()
		reconcileConfig()
		Expect(replicas("app")).To(BeZero())
	})

	It("moves on once the stage misses the timeout the config opted into", func() {
		c := &finopsv1.ScalingConfig{}
		Expect(k8sClient.Get(ctx, key, c)).To(Succeed())
		c.Spec.FeatureFlags = &finopsv1.ScalingGroupFeatureFlags{SkipOnTimeout: true, TimeoutMinutes: 1}
		Expect(k8sClient.Update(ctx, c)).To(Succeed())
		ageTheTransition()
		reconcileConfig()
		Expect(replicas("app")).To(Equal(int32(1)))
	})
})
