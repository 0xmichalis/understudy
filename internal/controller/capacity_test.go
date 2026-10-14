/*
Copyright 2026 The Understudy Authors.

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
	"os"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	appsv1alpha1 "github.com/kylan11/understudy/api/v1alpha1"
	"github.com/kylan11/understudy/internal/workload"
)

func setBaseline(f *fixture, replicas int32) {
	EventuallyWithOffset(1, func() error {
		cr := getCR(f)
		cr.Spec.BaselineReplicas = ptr.To(replicas)
		return k8sClient.Update(ctx, cr)
	}).Should(Succeed())
}

func capacityTarget(f *fixture) *workload.Target {
	cr := getCR(f)
	target, err := workload.Resolve(ctx, k8sClient, client.ObjectKey{Namespace: f.ns, Name: cr.Spec.TargetRef.Name}, cr.Spec.TargetRef.Kind)
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	return target
}

func expectBaselineApplied(f *fixture, replicas int32) {
	EventuallyWithOffset(1, func() *int32 { return getCR(f).Status.BaselineReplicas }).Should(Equal(ptr.To(replicas)))
	EventuallyWithOffset(1, func() *metav1.Condition { return findCond(getCR(f), appsv1alpha1.ConditionBaselineApplied) }).Should(
		HaveField("Status", metav1.ConditionTrue))
}

func newCapacityFixture(kind string) *fixture {
	if kind != workload.KindDeployment {
		if os.Getenv("ARGO_ROLLOUTS_CRD") == "" {
			Skip("run make test to install the upstream Rollout CRD")
		}
		f, _ := newRolloutFixture(kind == "BlueGreen")
		setBaseline(f, 1)
		expectBaselineApplied(f, 1)
		return f
	}
	f := newFixture(func(cr *appsv1alpha1.Understudy) { cr.Spec.BaselineReplicas = ptr.To(int32(1)) })
	Eventually(func() error {
		dep := getDeployment(f)
		dep.Annotations = map[string]string{"deployment.kubernetes.io/revision": "1"}
		return k8sClient.Update(ctx, dep)
	}).Should(Succeed())
	settleDeployment(f.dep)
	expectBaselineApplied(f, 1)
	return f
}

var _ = Describe("Declarative baseline capacity", func() {
	It("converges an initial baseline, subsequent capacity changes, and live replica drift", func() {
		f := newFixture(func(cr *appsv1alpha1.Understudy) { cr.Spec.BaselineReplicas = ptr.To(int32(3)) })
		Eventually(func() int32 { return capacityTarget(f).Replicas() }).Should(Equal(int32(3)))
		expectBaselineApplied(f, 3)
		setBaseline(f, 2)
		Eventually(func() int32 { return capacityTarget(f).Replicas() }).Should(Equal(int32(2)))
		expectBaselineApplied(f, 2)
		Expect(capacityTarget(f).PatchReplicas(ctx, k8sClient, 4, nil)).To(Succeed())
		Eventually(func() int32 { return capacityTarget(f).Replicas() }).Should(Equal(int32(2)))
		setBaseline(f, 0)
		Eventually(func() int32 { return capacityTarget(f).Replicas() }).Should(BeZero())
		cordonNode(f.node)
		Consistently(func() int32 { return capacityTarget(f).Replicas() }).Should(BeZero())
		Expect(evict(f.pod, false)).To(Succeed())
		setBaseline(f, 1)
		Eventually(func() int32 { return capacityTarget(f).Replicas() }).Should(Equal(int32(1)))
		expectBaselineApplied(f, 1)
	})

	DescribeTable("keeps the handover baseline and applies the latest request after eviction",
		func(kind string, desired int32) {
			f := newCapacityFixture(kind)
			cordonNode(f.node)
			Eventually(func() int32 { return capacityTarget(f).Replicas() }).Should(Equal(int32(2)))
			setBaseline(f, 4)
			setBaseline(f, desired)
			Eventually(func() *metav1.Condition { return findCond(getCR(f), appsv1alpha1.ConditionBaselineApplied) }).Should(
				And(HaveField("Status", metav1.ConditionFalse), HaveField("Reason", "HandoverInProgress")))
			Expect(getCR(f).Status.BaselineReplicas).To(Equal(ptr.To(int32(1))))
			Consistently(func() int32 { return capacityTarget(f).Replicas() }).Should(Equal(int32(2)))
			Expect(apierrors.IsTooManyRequests(evict(f.pod, false))).To(BeTrue())

			By("repairing effective replica drift without changing the captured baseline")
			target := capacityTarget(f)
			Expect(target.PatchReplicas(ctx, k8sClient, 1, ptr.To(int32(1)))).To(Succeed())
			Eventually(func() int32 { return capacityTarget(f).Replicas() }).Should(Equal(int32(2)))

			By("recovering from a lost status write using the persisted handover marker")
			Eventually(func() error {
				cr := getCR(f)
				cr.Status = appsv1alpha1.UnderstudyStatus{}
				return k8sClient.Status().Update(ctx, cr)
			}).Should(Succeed())
			Eventually(func() *int32 { return getCR(f).Status.BaselineReplicas }).Should(Equal(ptr.To(int32(1))))
			Expect(capacityTarget(f).Replicas()).To(Equal(int32(2)))

			By("using the frozen readiness threshold even when a larger baseline is pending")
			healthy := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: f.ns + "-healthy"}}
			Expect(k8sClient.Create(ctx, healthy)).To(Succeed())
			standIn := makePod(f.ns, "stand-in", f.dep.Name, healthy.Name, true)
			if kind != workload.KindDeployment {
				labelRolloutPod(standIn, stableHash)
			}
			Eventually(func() error { return evict(f.pod, false) }).Should(Succeed())
			Eventually(func() int32 { return capacityTarget(f).Replicas() }).Should(Equal(desired))
			Eventually(func() map[string]string { return capacityTarget(f).GetAnnotations() }).ShouldNot(HaveKey(AnnotationBaseReplicas))
			expectBaselineApplied(f, desired)
		}, Entry("Deployment scale-up", workload.KindDeployment, int32(3)),
		Entry("Deployment scale-to-zero", workload.KindDeployment, int32(0)),
		Entry("canary scale-up", workload.KindRollout, int32(3)),
		Entry("blue/green scale-down", "BlueGreen", int32(0)))

	It("applies the pending baseline when the disruption is cancelled", func() {
		f := newCapacityFixture(workload.KindDeployment)
		cordonNode(f.node)
		Eventually(func() int32 { return capacityTarget(f).Replicas() }).Should(Equal(int32(2)))
		setBaseline(f, 3)
		Eventually(func() error {
			node := &corev1.Node{}
			if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(f.node), node); err != nil {
				return err
			}
			node.Spec.Unschedulable = false
			return k8sClient.Update(ctx, node)
		}).Should(Succeed())
		Eventually(func() int32 { return capacityTarget(f).Replicas() }).Should(Equal(int32(3)))
		expectBaselineApplied(f, 3)
	})

	It("hands a Deployment release the latest baseline and releases its eviction hold", func() {
		f := newCapacityFixture(workload.KindDeployment)
		cordonNode(f.node)
		Eventually(func() int32 { return capacityTarget(f).Replicas() }).Should(Equal(int32(2)))
		setBaseline(f, 3)
		Eventually(func() error {
			dep := getDeployment(f)
			dep.Status.Conditions = []appsv1.DeploymentCondition{{
				Type: appsv1.DeploymentProgressing, Status: corev1.ConditionTrue, Reason: "ReplicaSetUpdated",
			}}
			return k8sClient.Status().Update(ctx, dep)
		}).Should(Succeed())
		Eventually(func() error {
			dep := getDeployment(f)
			dep.Annotations["deployment.kubernetes.io/revision"] = "2"
			return k8sClient.Update(ctx, dep)
		}).Should(Succeed())
		Eventually(func() int32 { return capacityTarget(f).Replicas() }).Should(Equal(int32(3)))
		Eventually(func() map[string]string { return capacityTarget(f).GetAnnotations() }).ShouldNot(HaveKey(AnnotationBaseReplicas))
		expectBaselineApplied(f, 3)
		Eventually(func() error { return evict(f.pod, false) }).Should(Succeed())
	})

	It("continues baseline management during a paused Rollout release", func() {
		f := newCapacityFixture(workload.KindRollout)
		ro, err := workload.Resolve(ctx, k8sClient, client.ObjectKey{Namespace: f.ns, Name: f.dep.Name}, workload.KindRollout)
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Patch(ctx, ro.Object, client.RawPatch(types.MergePatchType, []byte(`{"spec":{"paused":true}}`)))).To(Succeed())
		setBaseline(f, 3)
		Eventually(func() int32 { return capacityTarget(f).Replicas() }).Should(Equal(int32(3)))
		expectBaselineApplied(f, 3)
		Eventually(func() *metav1.Condition { return findCond(getCR(f), appsv1alpha1.ConditionProtected) }).Should(HaveField("Status", metav1.ConditionFalse))
		Expect(evict(f.pod, false)).To(Succeed())
	})

	It("rejects a negative desired baseline at admission", func() {
		f := newFixture(nil)
		cr := &appsv1alpha1.Understudy{
			ObjectMeta: metav1.ObjectMeta{Name: "invalid", Namespace: f.ns},
			Spec: appsv1alpha1.UnderstudySpec{
				TargetRef: f.cr.Spec.TargetRef, BaselineReplicas: ptr.To(int32(-1)),
			},
		}
		Expect(apierrors.IsInvalid(k8sClient.Create(ctx, cr))).To(BeTrue())
	})
})
