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

package pinned

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	appsv1alpha1 "github.com/kylan11/understudy/api/v1alpha1"
)

const (
	testNS   = "team-a"
	testCR   = "lead"
	testPDB  = ownedPDBPrefix + testCR
	testNode = "n1"
)

func TestPDBRefsParsesKarpenterQuotedMessage(t *testing.T) {
	refs := pdbRefs(`Cannot disrupt Node: pdb "team-a/understudy-lead" prevents pod evictions`)
	if len(refs) != 1 {
		t.Fatalf("expected 1 ref, got %v", refs)
	}
	want := types.NamespacedName{Namespace: testNS, Name: testPDB}
	if refs[0] != want {
		t.Fatalf("expected %v, got %v", want, refs[0])
	}
}

func TestPDBRefsParsesTheLiveObservedKarpenterFormat(t *testing.T) {
	for _, msg := range []string{
		"Pdb prevents pod evictions (PodDisruptionBudget=[team-a/understudy-lead])",
		"(combined from similar events): Pdb prevents pod evictions (PodDisruptionBudget=[team-a/understudy-lead])",
		"Cannot disrupt NodeClaim: PDB team-a/understudy-lead prevents pod evictions",
	} {
		refs := pdbRefs(msg)
		if len(refs) != 1 || refs[0].Name != testPDB || refs[0].Namespace != testNS {
			t.Fatalf("expected the live-observed format to parse, got %v for %q", refs, msg)
		}
	}
}

func TestPDBRefsIgnoresMessagesWithoutABudget(t *testing.T) {
	for _, msg := range []string{
		"Cannot disrupt Node: state node is nominated for a pending pod",
		`Cannot disrupt Node: pod "team-a/lead-0" has "karpenter.sh/do-not-disrupt" annotation`,
		"Node isn't initialized",
	} {
		if refs := pdbRefs(msg); refs != nil {
			t.Fatalf("expected no refs for %q, got %v", msg, refs)
		}
	}
}

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := appsv1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return s
}

func fixtures() (*policyv1.PodDisruptionBudget, *appsv1alpha1.Understudy) {
	pdb := &policyv1.PodDisruptionBudget{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testPDB,
			Namespace: testNS,
			Labels:    map[string]string{ownedLabel: "true"},
		},
	}
	cr := &appsv1alpha1.Understudy{
		ObjectMeta: metav1.ObjectMeta{Name: testCR, Namespace: testNS},
	}
	return pdb, cr
}

func blockedEvent(message string, seen time.Time) *corev1.Event {
	return &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Name: testNode + ".blocked", Namespace: "default"},
		InvolvedObject: corev1.ObjectReference{Kind: nodeKind, Name: testNode},
		Reason:         BlockedReason,
		Type:           corev1.EventTypeNormal,
		Message:        message,
		LastTimestamp:  metav1.NewTime(seen),
	}
}

func newTestWatcher(t *testing.T, objs ...runtime.Object) *Watcher {
	t.Helper()
	c := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithRuntimeObjects(objs...).
		WithStatusSubresource(&appsv1alpha1.Understudy{}).
		Build()
	return &Watcher{client: c, pinnedBy: map[types.NamespacedName]types.NamespacedName{}}
}

func reconcile(t *testing.T, w *Watcher, ev *corev1.Event) ctrl.Result {
	t.Helper()
	res, err := w.Reconcile(context.Background(),
		ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ev.Namespace, Name: ev.Name}})
	if err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}
	return res
}

func pinnedCondition(t *testing.T, w *Watcher) *metav1.Condition {
	t.Helper()
	var cr appsv1alpha1.Understudy
	if err := w.client.Get(context.Background(),
		types.NamespacedName{Namespace: testNS, Name: testCR}, &cr); err != nil {
		t.Fatalf("get cr: %v", err)
	}
	return meta.FindStatusCondition(cr.Status.Conditions, appsv1alpha1.ConditionNodePinned)
}

func TestFreshRefusalSetsNodePinned(t *testing.T) {
	pdb, cr := fixtures()
	ev := blockedEvent(`Cannot disrupt Node: pdb "team-a/understudy-lead" prevents pod evictions`, time.Now())
	w := newTestWatcher(t, pdb, cr, ev)

	res := reconcile(t, w, ev)
	cond := pinnedCondition(t, w)
	if cond == nil || cond.Status != metav1.ConditionTrue {
		t.Fatalf("expected NodePinned=True, got %+v", cond)
	}
	if res.RequeueAfter == 0 {
		t.Fatal("expected a requeue to eventually clear a stale pin")
	}
}

func TestStaleRefusalClearsNodePinned(t *testing.T) {
	pdb, cr := fixtures()
	fresh := blockedEvent(`Cannot disrupt Node: pdb "team-a/understudy-lead" prevents pod evictions`, time.Now())
	w := newTestWatcher(t, pdb, cr, fresh)
	reconcile(t, w, fresh)

	stale := fresh.DeepCopy()
	stale.LastTimestamp = metav1.NewTime(time.Now().Add(-time.Hour))
	if err := w.client.Update(context.Background(), stale); err != nil {
		t.Fatalf("age the event: %v", err)
	}
	reconcile(t, w, stale)

	cond := pinnedCondition(t, w)
	if cond == nil || cond.Status != metav1.ConditionFalse {
		t.Fatalf("expected NodePinned=False after the refusals went stale, got %+v", cond)
	}
}

func TestDeletedEventClearsNodePinned(t *testing.T) {
	pdb, cr := fixtures()
	ev := blockedEvent(`Cannot disrupt Node: pdb "team-a/understudy-lead" prevents pod evictions`, time.Now())
	w := newTestWatcher(t, pdb, cr, ev)
	reconcile(t, w, ev)

	if err := w.client.Delete(context.Background(), ev); err != nil {
		t.Fatalf("delete event: %v", err)
	}
	reconcile(t, w, ev)

	cond := pinnedCondition(t, w)
	if cond == nil || cond.Status != metav1.ConditionFalse {
		t.Fatalf("expected NodePinned=False after the event vanished, got %+v", cond)
	}
}

func TestForeignBudgetSetsNothing(t *testing.T) {
	_, cr := fixtures()
	foreign := &policyv1.PodDisruptionBudget{
		ObjectMeta: metav1.ObjectMeta{Name: "bystander", Namespace: testNS},
	}
	ev := blockedEvent(`Cannot disrupt Node: pdb "team-a/bystander" prevents pod evictions`, time.Now())
	w := newTestWatcher(t, foreign, cr, ev)
	reconcile(t, w, ev)

	if cond := pinnedCondition(t, w); cond != nil {
		t.Fatalf("a budget this operator does not own must not pin anything, got %+v", cond)
	}
}
