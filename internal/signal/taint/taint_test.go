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

package taint

import (
	"strconv"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kylan11/understudy/internal/signal"
)

func nodeWith(key, value string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "n1"},
		Spec: corev1.NodeSpec{
			Taints: []corev1.Taint{{Key: key, Value: value, Effect: corev1.TaintEffectNoSchedule}},
		},
	}
}

func TestDoomTaintCarriesDeadlineAsInvoluntary(t *testing.T) {
	a := New(nil)
	deadline := time.Now().Add(2 * time.Minute).Truncate(time.Second)
	d := a.doomFor(nodeWith(DoomKey, strconv.FormatInt(deadline.Unix(), 10)), DoomKey)

	if d.Class != signal.Involuntary {
		t.Fatalf("expected Involuntary, got %s", d.Class)
	}
	if d.Deadline == nil || !d.Deadline.Equal(deadline) {
		t.Fatalf("expected deadline %v, got %v", deadline, d.Deadline)
	}
}

func TestDoomTaintWithGarbageValueStaysInvoluntaryWithoutDeadline(t *testing.T) {
	a := New(nil)
	d := a.doomFor(nodeWith(DoomKey, "not-a-timestamp"), DoomKey)

	if d.Class != signal.Involuntary {
		t.Fatalf("expected Involuntary, got %s", d.Class)
	}
	if d.Deadline != nil {
		t.Fatal("garbage values must not produce a deadline")
	}
}

func TestDrainerTaintsRemainVoluntaryUnbounded(t *testing.T) {
	a := New(nil)
	for _, key := range DefaultKeys {
		d := a.doomFor(nodeWith(key, "1754400000"), key)
		if d.Class != signal.VoluntaryUnbounded {
			t.Fatalf("%s: expected VoluntaryUnbounded, got %s", key, d.Class)
		}
		if d.Deadline != nil {
			t.Fatalf("%s: drainer taint values are timestamps of when they were applied, not deadlines", key)
		}
	}
}

func TestRebalanceTaintExpires(t *testing.T) {
	a := New(nil)
	d := a.doomFor(nodeWith(RebalanceKey, "1754400000"), RebalanceKey)

	if d.Class != signal.VoluntaryUnbounded {
		t.Fatalf("expected VoluntaryUnbounded, got %s", d.Class)
	}
	if d.ExpiresAt == nil {
		t.Fatal("rebalance signals must expire; they are risk hints, not commitments")
	}
}

func TestWatchKeysIncludeWellKnownAndConfigured(t *testing.T) {
	a := New([]string{"custom.io/drain"})
	keys := a.watchKeys()

	want := map[string]bool{DoomKey: false, RebalanceKey: false, "custom.io/drain": false}
	for _, k := range keys {
		if _, ok := want[k]; ok {
			want[k] = true
		}
	}
	for k, found := range want {
		if !found {
			t.Fatalf("expected %s to be watched", k)
		}
	}
}
