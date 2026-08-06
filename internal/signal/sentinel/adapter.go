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

package sentinel

import (
	"context"
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/kylan11/understudy/internal/signal/taint"
)

type Runner struct {
	Client   client.Client
	Probe    Probe
	NodeName string
	Interval time.Duration
}

func (r *Runner) Run(ctx context.Context) error {
	log := logf.FromContext(ctx).WithValues("probe", r.Probe.Name(), "node", r.NodeName)
	log.Info("sentinel started")
	interval := r.Interval
	if interval <= 0 {
		interval = 2 * time.Second
	}
	for {
		notice, err := r.Probe.Poll(ctx)
		switch {
		case ctx.Err() != nil:
			return nil
		case err != nil && err != ErrNoNotice:
			log.V(1).Info("probe error", "error", err.Error())
		case notice != nil:
			if applyErr := r.applyTaint(ctx, notice); applyErr != nil {
				log.Error(applyErr, "failed to taint node")
			} else {
				log.Info("termination notice observed",
					"kind", string(notice.Kind), "deadline", notice.Deadline.UTC().Format(time.RFC3339))
				if notice.Certain {
					return nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(interval):
		}
	}
}

func (r *Runner) applyTaint(ctx context.Context, notice *Notice) error {
	key := taint.DoomKey
	effect := corev1.TaintEffectNoSchedule
	value := strconv.FormatInt(notice.Deadline.Unix(), 10)
	if notice.Kind == KindRebalance {
		key = taint.RebalanceKey
		effect = corev1.TaintEffectPreferNoSchedule
	}

	var node corev1.Node
	if err := r.Client.Get(ctx, client.ObjectKey{Name: r.NodeName}, &node); err != nil {
		return err
	}
	for _, t := range node.Spec.Taints {
		if t.Key == key && t.Value == value {
			return nil
		}
	}
	patched := node.DeepCopy()
	filtered := patched.Spec.Taints[:0]
	for _, t := range patched.Spec.Taints {
		if t.Key != key {
			filtered = append(filtered, t)
		}
	}
	patched.Spec.Taints = append(filtered, corev1.Taint{Key: key, Value: value, Effect: effect})
	return r.Client.Patch(ctx, patched, client.MergeFrom(&node))
}
