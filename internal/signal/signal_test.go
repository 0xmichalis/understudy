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

package signal

import (
	"testing"
	"time"
)

const testEvictionSource = "eviction:drainer"

func TestLookupIgnoresExpiredSignals(t *testing.T) {
	r := NewRegistry()
	past := time.Now().Add(-time.Minute)
	r.Set(NodeDoom{Node: "n1", Class: VoluntaryUnbounded, Source: testEvictionSource, ExpiresAt: &past})

	if _, ok := r.Lookup("n1"); ok {
		t.Fatal("expected expired signal to be invisible to Lookup")
	}
}

func TestLookupKeepsUnexpiredSignals(t *testing.T) {
	r := NewRegistry()
	future := time.Now().Add(time.Minute)
	r.Set(NodeDoom{Node: "n1", Class: VoluntaryUnbounded, Source: testEvictionSource, ExpiresAt: &future})

	if _, ok := r.Lookup("n1"); !ok {
		t.Fatal("expected unexpired signal to be visible")
	}
}

func TestSweepRemovesExpiredAndNotifies(t *testing.T) {
	r := NewRegistry()
	ch := r.Subscribe(4)
	past := time.Now().Add(-time.Minute)
	r.Set(NodeDoom{Node: "n1", Class: VoluntaryUnbounded, Source: testEvictionSource, ExpiresAt: &past})
	<-ch

	r.sweep(time.Now())

	select {
	case ev := <-ch:
		if ev.Object.GetName() != "n1" {
			t.Fatalf("expected notification for n1, got %s", ev.Object.GetName())
		}
	default:
		t.Fatal("expected a notification after sweeping an expired signal")
	}
	if _, ok := r.Lookup("n1"); ok {
		t.Fatal("expected swept signal to be gone")
	}
}

func TestSweepKeepsSignalsWithoutExpiry(t *testing.T) {
	r := NewRegistry()
	r.Set(NodeDoom{Node: "n1", Class: VoluntaryUnbounded, Source: "cordon"})

	r.sweep(time.Now().Add(24 * time.Hour))

	if _, ok := r.Lookup("n1"); !ok {
		t.Fatal("signals without an expiry must never be swept")
	}
}

func TestInvoluntaryWinsOverVoluntary(t *testing.T) {
	r := NewRegistry()
	deadline := time.Now().Add(2 * time.Minute)
	r.Set(NodeDoom{Node: "n1", Class: VoluntaryUnbounded, Source: "cordon"})
	r.Set(NodeDoom{Node: "n1", Class: Involuntary, Deadline: &deadline, Source: "taint:understudy.sh/doomed"})

	got, ok := r.Lookup("n1")
	if !ok || got.Class != Involuntary {
		t.Fatalf("expected involuntary signal to win, got %+v", got)
	}
	if got.Deadline == nil {
		t.Fatal("expected the deadline to be carried through")
	}
}
