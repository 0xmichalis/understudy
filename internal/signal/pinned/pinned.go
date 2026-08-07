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
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	appsv1alpha1 "github.com/kylan11/understudy/api/v1alpha1"
	"github.com/kylan11/understudy/internal/metrics"
)

const (
	BlockedReason     = "DisruptionBlocked"
	nodeKind          = "Node"
	ownedLabel        = "understudy.sh/owned"
	ownedPDBPrefix    = "understudy-"
	defaultStaleAfter = 15 * time.Minute
)

var (
	pdbWord = regexp.MustCompile(`(?i)\bpdbs?\b`)
	objRef  = regexp.MustCompile(`\b[a-z0-9][a-z0-9.-]*/[a-z0-9][a-z0-9.-]*\b`)
)

type Watcher struct {
	StaleAfter time.Duration

	client   client.Client
	recorder events.EventRecorder
	mu       sync.Mutex
	pinnedBy map[types.NamespacedName]types.NamespacedName
}

func New() *Watcher { return &Watcher{} }

func (w *Watcher) Name() string { return "pin-detection" }

func (w *Watcher) staleAfter() time.Duration {
	if w.StaleAfter > 0 {
		return w.StaleAfter
	}
	return defaultStaleAfter
}

func (w *Watcher) SetupWithManager(mgr ctrl.Manager) error {
	w.client = mgr.GetClient()
	w.recorder = mgr.GetEventRecorder("understudy")
	w.pinnedBy = map[types.NamespacedName]types.NamespacedName{}
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Event{}).
		Named("pin-detection").
		WithEventFilter(predicate.NewPredicateFuncs(func(obj client.Object) bool {
			ev, ok := obj.(*corev1.Event)
			return ok && ev.Reason == BlockedReason && ev.InvolvedObject.Kind == nodeKind
		})).
		Complete(w)
}

func (w *Watcher) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var ev corev1.Event
	if err := w.client.Get(ctx, req.NamespacedName, &ev); err != nil {
		if apierrors.IsNotFound(err) {
			if cr, ok := w.forget(req.NamespacedName); ok {
				return ctrl.Result{}, w.setPinned(ctx, cr, false, "")
			}
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	if ev.Reason != BlockedReason || ev.InvolvedObject.Kind != nodeKind {
		return ctrl.Result{}, nil
	}

	seen := lastSeen(&ev)
	if seen.IsZero() {
		seen = time.Now()
	}
	fresh := time.Since(seen) < w.staleAfter()

	for _, ref := range pdbRefs(ev.Message) {
		cr, owned, err := w.resolveOwned(ctx, ref)
		if err != nil {
			return ctrl.Result{}, err
		}
		if !owned {
			continue
		}
		if !fresh {
			w.forget(req.NamespacedName)
			return ctrl.Result{}, w.setPinned(ctx, cr, false, "")
		}
		w.remember(req.NamespacedName, cr)
		if err := w.setPinned(ctx, cr, true, ev.InvolvedObject.Name); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: w.staleAfter() * 3 / 2}, nil
	}
	return ctrl.Result{}, nil
}

func (w *Watcher) resolveOwned(ctx context.Context, ref types.NamespacedName) (types.NamespacedName, bool, error) {
	var pdb policyv1.PodDisruptionBudget
	if err := w.client.Get(ctx, ref, &pdb); err != nil {
		if apierrors.IsNotFound(err) {
			return types.NamespacedName{}, false, nil
		}
		return types.NamespacedName{}, false, err
	}
	if pdb.Labels[ownedLabel] != "true" || !strings.HasPrefix(pdb.Name, ownedPDBPrefix) {
		return types.NamespacedName{}, false, nil
	}
	return types.NamespacedName{
		Namespace: pdb.Namespace,
		Name:      strings.TrimPrefix(pdb.Name, ownedPDBPrefix),
	}, true, nil
}

func (w *Watcher) setPinned(ctx context.Context, key types.NamespacedName, pinned bool, node string) error {
	var cr appsv1alpha1.Understudy
	if err := w.client.Get(ctx, key, &cr); err != nil {
		return client.IgnoreNotFound(err)
	}
	desired := metav1.ConditionFalse
	reason := "NoRecentRefusals"
	message := "no disruption refusals observed recently"
	if pinned {
		desired = metav1.ConditionTrue
		reason = "BudgetBlocksDisruption"
		message = fmt.Sprintf(
			"a disrupter wants node %s but the hostage budget blocks it; graceful consolidation, drift and expiry cannot proceed (see The tradeoff in the README)",
			node)
	}
	current := meta.FindStatusCondition(cr.Status.Conditions, appsv1alpha1.ConditionNodePinned)
	if current != nil && current.Status == desired {
		return nil
	}
	patched := cr.DeepCopy()
	meta.SetStatusCondition(&patched.Status.Conditions, metav1.Condition{
		Type:               appsv1alpha1.ConditionNodePinned,
		Status:             desired,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: cr.Generation,
	})
	if err := w.client.Status().Patch(ctx, patched, client.MergeFrom(&cr)); err != nil {
		return err
	}
	if pinned {
		metrics.NodePinnedTotal.Inc()
		if w.recorder != nil {
			w.recorder.Eventf(&cr, nil, corev1.EventTypeWarning, "NodePinned", "Detect", "%s", message)
		}
		logf.FromContext(ctx).Info("protected workload is pinning its node",
			"understudy", key.String(), "node", node)
	}
	return nil
}

func (w *Watcher) remember(event, cr types.NamespacedName) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pinnedBy[event] = cr
}

func (w *Watcher) forget(event types.NamespacedName) (types.NamespacedName, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	cr, ok := w.pinnedBy[event]
	delete(w.pinnedBy, event)
	return cr, ok
}

func pdbRefs(message string) []types.NamespacedName {
	if !pdbWord.MatchString(message) {
		return nil
	}
	var refs []types.NamespacedName
	for _, token := range objRef.FindAllString(message, 8) {
		parts := strings.SplitN(token, "/", 2)
		refs = append(refs, types.NamespacedName{Namespace: parts[0], Name: parts[1]})
	}
	return refs
}

func lastSeen(ev *corev1.Event) time.Time {
	t := ev.FirstTimestamp.Time
	for _, c := range []time.Time{ev.LastTimestamp.Time, ev.EventTime.Time} {
		if c.After(t) {
			t = c
		}
	}
	if ev.Series != nil && ev.Series.LastObservedTime.After(t) {
		t = ev.Series.LastObservedTime.Time
	}
	return t
}
