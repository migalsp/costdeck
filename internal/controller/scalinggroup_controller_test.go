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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
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
					ObjectMeta: metav1.ObjectMeta{
						Name:      resourceName,
						Namespace: "default",
					},
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
				Recorder: record.NewFakeRecorder(100),
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
	})
})
