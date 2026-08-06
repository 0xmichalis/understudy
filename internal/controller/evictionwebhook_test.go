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
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	appsv1alpha1 "github.com/kylan11/understudy/api/v1alpha1"
)

func evict(pod *corev1.Pod, dryRun bool) error {
	eviction := &policyv1.Eviction{
		ObjectMeta: metav1.ObjectMeta{Name: pod.Name, Namespace: pod.Namespace},
	}
	var opts []client.SubResourceCreateOption
	if dryRun {
		opts = append(opts, client.DryRunAll)
	}
	return k8sClient.SubResource("eviction").Create(ctx, pod, eviction, opts...)
}

var _ = Describe("Eviction webhook adapter", func() {

	It("surges when a protected pod's eviction is attempted, with no node signal at all", func() {
		f := newFixture(nil)

		By("waiting for the hostage PDB so the pod counts as protected")
		Eventually(func() error {
			_, err := getPDB(f)
			return err
		}).Should(Succeed())

		By("attempting an eviction the PDB will reject")
		Eventually(func() bool {
			err := evict(f.pod, false)
			return err != nil
		}).Should(BeTrue(), "the hostage PDB should reject the eviction with 429")

		By("surging purely on the observed eviction attempt")
		Eventually(func() int32 {
			dep := &appsv1.Deployment{}
			if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(f.dep), dep); err != nil {
				return -1
			}
			return *dep.Spec.Replicas
		}).Should(Equal(int32(2)))
		Eventually(func() appsv1alpha1.Phase { return getCR(f).Status.Phase }).Should(Equal(appsv1alpha1.PhaseSurging))
	})

	It("ignores evictions of pods that no Understudy protects", func() {
		f := newFixture(nil)
		Eventually(func() error { _, err := getPDB(f); return err }).Should(Succeed())

		By("creating an unrelated pod on the same node")
		other := makePod(f.ns, "bystander", "bystander", f.node.Name, true)

		By("evicting the bystander")
		Expect(evict(other, false)).To(Succeed())

		By("never surging the protected workload")
		Consistently(func() int32 { return *getDeployment(f).Spec.Replicas }).Should(Equal(int32(1)))
		Expect(getCR(f).Status.Phase).To(Equal(appsv1alpha1.PhaseIdle))
	})

	It("ignores dry-run eviction attempts", func() {
		f := newFixture(nil)
		Eventually(func() error { _, err := getPDB(f); return err }).Should(Succeed())

		By("issuing a dry-run eviction")
		_ = evict(f.pod, true)

		By("never surging")
		Consistently(func() int32 { return *getDeployment(f).Spec.Replicas }).Should(Equal(int32(1)))
	})

	It("un-surges once eviction attempts stop and the signal expires", func() {
		f := newFixture(nil)
		Eventually(func() error { _, err := getPDB(f); return err }).Should(Succeed())

		By("triggering a surge via an eviction attempt")
		Eventually(func() bool { return evict(f.pod, false) != nil }).Should(BeTrue())
		Eventually(func() int32 { return *getDeployment(f).Spec.Replicas }).Should(Equal(int32(2)))

		By("letting the signal expire naturally once attempts stop")
		Eventually(func() int32 { return *getDeployment(f).Spec.Replicas }).Should(Equal(int32(1)))
		Eventually(func() appsv1alpha1.Phase { return getCR(f).Status.Phase }).Should(Equal(appsv1alpha1.PhaseIdle))
	})
})
