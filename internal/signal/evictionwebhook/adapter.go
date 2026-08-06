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

package evictionwebhook

import (
	"context"
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/understudy-k8s/understudy/internal/metrics"
	"github.com/understudy-k8s/understudy/internal/signal"
)

const (
	Path         = "/observe-eviction"
	SourcePrefix = "eviction:"
	ownedLabel   = "understudy.dev/owned"
	doomLifetime = 90 * time.Second
)

type Adapter struct {
	DoomLifetime time.Duration

	reader   client.Reader
	registry *signal.Registry
}

func New() *Adapter { return &Adapter{} }

func (a *Adapter) lifetime() time.Duration {
	if a.DoomLifetime > 0 {
		return a.DoomLifetime
	}
	return doomLifetime
}

func (a *Adapter) Name() string { return "eviction-webhook" }

func (a *Adapter) SetupWithManager(mgr ctrl.Manager, reg *signal.Registry) error {
	a.reader = mgr.GetClient()
	a.registry = reg
	mgr.GetWebhookServer().Register(Path, &webhook.Admission{Handler: a})
	return nil
}

func (a *Adapter) Handle(ctx context.Context, req admission.Request) admission.Response {
	allowed := admission.Allowed("")
	if req.DryRun != nil && *req.DryRun {
		return allowed
	}
	if req.Name == "" || req.Namespace == "" {
		return allowed
	}

	var pod corev1.Pod
	if err := a.reader.Get(ctx, types.NamespacedName{Namespace: req.Namespace, Name: req.Name}, &pod); err != nil {
		return allowed
	}
	if pod.Spec.NodeName == "" {
		return allowed
	}

	protected := a.podProtected(ctx, &pod)
	metrics.EvictionAttemptsObserved.WithLabelValues(strconv.FormatBool(protected)).Inc()
	if !protected {
		return allowed
	}

	expires := time.Now().Add(a.lifetime())
	a.registry.Set(signal.NodeDoom{
		Node:      pod.Spec.NodeName,
		Class:     signal.VoluntaryUnbounded,
		ExpiresAt: &expires,
		Source:    SourcePrefix + req.UserInfo.Username,
	})
	logf.FromContext(ctx).V(1).Info("eviction attempt observed",
		"pod", req.Namespace+"/"+req.Name, "node", pod.Spec.NodeName, "evictor", req.UserInfo.Username)
	return allowed
}

func (a *Adapter) podProtected(ctx context.Context, pod *corev1.Pod) bool {
	var pdbs policyv1.PodDisruptionBudgetList
	if err := a.reader.List(ctx, &pdbs, client.InNamespace(pod.Namespace),
		client.MatchingLabels{ownedLabel: "true"}); err != nil {
		return false
	}
	for i := range pdbs.Items {
		selector, err := metav1.LabelSelectorAsSelector(pdbs.Items[i].Spec.Selector)
		if err != nil {
			continue
		}
		if selector.Matches(labels.Set(pod.Labels)) {
			return true
		}
	}
	return false
}
