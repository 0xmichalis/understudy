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
	"strconv"

	rolloutsv1alpha1 "github.com/argoproj/argo-rollouts/pkg/apis/rollouts/v1alpha1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	appsv1alpha1 "github.com/kylan11/understudy/api/v1alpha1"
	"github.com/kylan11/understudy/internal/workload"
)

const stableHash = "stable-hash"

func newRolloutFixture(blueGreen bool) (*fixture, *rolloutsv1alpha1.Rollout) {
	f := newFixture(func(cr *appsv1alpha1.Understudy) { cr.Spec.TargetRef.Kind = workload.KindRollout })
	dep := getDeployment(f)
	ro := &rolloutsv1alpha1.Rollout{
		ObjectMeta: metav1.ObjectMeta{Name: dep.Name, Namespace: f.ns},
		Spec: rolloutsv1alpha1.RolloutSpec{
			Replicas: ptr.To(int32(1)),
			Selector: dep.Spec.Selector.DeepCopy(),
			Template: *dep.Spec.Template.DeepCopy(),
			Strategy: rolloutsv1alpha1.RolloutStrategy{Canary: &rolloutsv1alpha1.CanaryStrategy{}},
		},
	}
	if blueGreen {
		ro.Spec.Strategy = rolloutsv1alpha1.RolloutStrategy{
			BlueGreen: &rolloutsv1alpha1.BlueGreenStrategy{ActiveService: "active", PreviewService: "preview"},
		}
	}
	Expect(k8sClient.Create(ctx, ro)).To(Succeed())
	updateRolloutStatus(ro, func(current *rolloutsv1alpha1.Rollout) {
		current.Status = rolloutsv1alpha1.RolloutStatus{
			ObservedGeneration: strconv.FormatInt(current.Generation, 10),
			StableRS:           stableHash, CurrentPodHash: stableHash,
			Replicas: 1, UpdatedReplicas: 1, ReadyReplicas: 1, AvailableReplicas: 1,
		}
		if blueGreen {
			current.Status.BlueGreen.ActiveSelector = stableHash
			current.Status.BlueGreen.PreviewSelector = stableHash
		}
	})
	labelRolloutPod(f.pod, stableHash)
	Eventually(func() *metav1.Condition { return findCond(getCR(f), appsv1alpha1.ConditionProtected) }).Should(
		HaveField("Status", metav1.ConditionTrue))
	return f, ro
}

func getRollout(ro *rolloutsv1alpha1.Rollout) *rolloutsv1alpha1.Rollout {
	current := &rolloutsv1alpha1.Rollout{}
	ExpectWithOffset(1, k8sClient.Get(ctx, client.ObjectKeyFromObject(ro), current)).To(Succeed())
	return current
}

func updateRolloutStatus(ro *rolloutsv1alpha1.Rollout, mutate func(*rolloutsv1alpha1.Rollout)) {
	EventuallyWithOffset(1, func() error {
		current := getRollout(ro)
		mutate(current)
		return k8sClient.Status().Update(ctx, current)
	}).Should(Succeed())
}

func labelRolloutPod(pod *corev1.Pod, hash string) {
	EventuallyWithOffset(1, func() error {
		current := &corev1.Pod{}
		if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(pod), current); err != nil {
			return err
		}
		current.Labels[rolloutsv1alpha1.DefaultRolloutUniqueLabelKey] = hash
		return k8sClient.Update(ctx, current)
	}).Should(Succeed())
}

var _ = Describe("Argo Rollouts", func() {
	BeforeEach(func() {
		if os.Getenv("ARGO_ROLLOUTS_CRD") == "" {
			Skip("run make test to install the upstream Rollout CRD")
		}
	})

	DescribeTable("holds, surges, releases only for a serving replacement, and scales back",
		func(blueGreen bool) {
			f, ro := newRolloutFixture(blueGreen)
			cordonNode(f.node)
			Eventually(func() int32 { return *getRollout(ro).Spec.Replicas }).Should(Equal(int32(2)))
			Expect(getRollout(ro).Annotations).To(HaveKeyWithValue(AnnotationBaseReplicas, "1"))
			Eventually(func() string { return getPodDeletionCost(f, f.pod.Name) }).Should(Equal(doomedPodDeletionCost))
			Expect(apierrors.IsTooManyRequests(evict(f.pod, false))).To(BeTrue())

			By("ignoring a ready pod from a different revision")
			healthy := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: f.ns + "-healthy"}}
			Expect(k8sClient.Create(ctx, healthy)).To(Succeed())
			preview := makePod(f.ns, "preview", ro.Name, healthy.Name, true)
			labelRolloutPod(preview, "preview-hash")
			Consistently(func() bool { return apierrors.IsTooManyRequests(evict(f.pod, false)) }).Should(BeTrue())
			Expect(getCR(f).Status.Phase).To(Equal(appsv1alpha1.PhaseSurging))

			By("waiting for the serving revision's readiness gate")
			standIn := makePod(f.ns, "stand-in", ro.Name, healthy.Name, false)
			labelRolloutPod(standIn, stableHash)
			Consistently(func() bool { return apierrors.IsTooManyRequests(evict(f.pod, false)) }).Should(BeTrue())
			markPodReady(standIn)
			Eventually(func() error { return evict(f.pod, false) }).Should(Succeed())
			Eventually(func() int32 { return *getRollout(ro).Spec.Replicas }).Should(Equal(int32(1)))
			Eventually(func() map[string]string { return getRollout(ro).Annotations }).ShouldNot(HaveKey(AnnotationBaseReplicas))
			Expect(*getDeployment(f).Spec.Replicas).To(Equal(int32(1)), "same-named Deployment must not be scaled")
			Expect(getRollout(ro).Spec.Strategy).To(Equal(ro.Spec.Strategy))
		}, Entry("canary", false), Entry("blue/green", true))

	DescribeTable("stands down during progressive delivery and admits evictions",
		func(blueGreen, paused, abort bool) {
			f, ro := newRolloutFixture(blueGreen)
			updateRolloutStatus(ro, func(current *rolloutsv1alpha1.Rollout) {
				current.Status.CurrentPodHash = "new-revision"
				current.Status.ControllerPause = paused
				current.Status.Abort = abort
			})
			cordonNode(f.node)
			Eventually(func() *metav1.Condition { return findCond(getCR(f), appsv1alpha1.ConditionProtected) }).Should(
				HaveField("Reason", "RolloutInProgress"))
			Consistently(func() int32 { return *getRollout(ro).Spec.Replicas }).Should(Equal(int32(1)))
			Expect(evict(f.pod, false)).To(Succeed())
		}, Entry("canary progressing", false, false, false), Entry("paused canary", false, true, false),
		Entry("aborted canary", false, false, true), Entry("blue/green awaiting promotion", true, true, false))

	It("relinquishes an active surge when a release begins", func() {
		f, ro := newRolloutFixture(false)
		cordonNode(f.node)
		Eventually(func() int32 { return *getRollout(ro).Spec.Replicas }).Should(Equal(int32(2)))
		updateRolloutStatus(ro, func(current *rolloutsv1alpha1.Rollout) { current.Status.CurrentPodHash = "new-revision" })
		Eventually(func() int32 { return *getRollout(ro).Spec.Replicas }).Should(Equal(int32(1)))
		Eventually(func() map[string]string { return getRollout(ro).Annotations }).ShouldNot(HaveKey(AnnotationBaseReplicas))
		Eventually(func() string { return getPodDeletionCost(f, f.pod.Name) }).Should(BeEmpty())
		Expect(evict(f.pod, false)).To(Succeed())
	})

	It("relaxes a stalled Rollout surge after the readiness deadline", func() {
		f, ro := newRolloutFixture(false)
		Eventually(func() error {
			cr := getCR(f)
			cr.Spec.ReadinessDeadlineSeconds = ptr.To(int32(1))
			return k8sClient.Update(ctx, cr)
		}).Should(Succeed())
		cordonNode(f.node)
		Eventually(func() int32 { return *getRollout(ro).Spec.Replicas }).Should(Equal(int32(2)))
		Eventually(func() appsv1alpha1.Phase { return getCR(f).Status.Phase }).Should(Equal(appsv1alpha1.PhaseRelaxed))
		Expect(evict(f.pod, false)).To(Succeed())
	})

	It("surges from eviction attempts alone", func() {
		f, ro := newRolloutFixture(false)
		Eventually(func() int32 {
			Expect(apierrors.IsTooManyRequests(evict(f.pod, false))).To(BeTrue())
			return *getRollout(ro).Spec.Replicas
		}).Should(Equal(int32(2)))
	})
})

var _ = Describe("Optional Rollout API", func() {
	It("keeps serving Deployments when the Rollout CRD is absent", func() {
		if os.Getenv("ARGO_ROLLOUTS_CRD") != "" {
			Skip("run make test ARGO_ROLLOUTS_CRD= to exercise missing-CRD startup")
		}
		f := newFixture(func(cr *appsv1alpha1.Understudy) { cr.Spec.TargetRef.Kind = workload.KindRollout })
		Eventually(func() appsv1alpha1.Phase { return getCR(f).Status.Phase }).Should(Equal(appsv1alpha1.PhaseUnsupported))
		Expect(evict(f.pod, false)).To(Succeed())
		deployment := newFixture(nil)
		cordonNode(deployment.node)
		expectSurged(deployment)
	})
})
