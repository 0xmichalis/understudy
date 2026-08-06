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
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/understudy-k8s/understudy/internal/signal/taint"
)

func awsServer(t *testing.T, action string, rebalance bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/latest/api/token":
			if r.Header.Get("X-aws-ec2-metadata-token-ttl-seconds") == "" {
				t.Error("IMDSv2 token request must carry a TTL header")
			}
			_, _ = w.Write([]byte("token123"))
		case r.URL.Path == "/latest/meta-data/spot/instance-action":
			if r.Header.Get("X-aws-ec2-metadata-token") != "token123" {
				t.Error("metadata request must present the IMDSv2 token")
			}
			if action == "" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = w.Write([]byte(action))
		case r.URL.Path == "/latest/meta-data/events/recommendations/rebalance":
			if !rebalance {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = w.Write([]byte(`{"noticeTime":"2026-08-06T10:00:00Z"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func TestAWSProbeParsesSpotInterruption(t *testing.T) {
	srv := awsServer(t, `{"action":"terminate","time":"2026-08-06T10:02:00Z"}`, false)
	defer srv.Close()
	p := &AWSProbe{Host: srv.URL}

	notice, err := p.Poll(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if notice.Kind != KindSpotITN || !notice.Certain {
		t.Fatalf("expected a certain spot ITN, got %+v", notice)
	}
	want, _ := time.Parse(time.RFC3339, "2026-08-06T10:02:00Z")
	if !notice.Deadline.Equal(want) {
		t.Fatalf("expected deadline %v, got %v", want, notice.Deadline)
	}
}

func TestAWSProbeReportsNoNoticeWhenQuiet(t *testing.T) {
	srv := awsServer(t, "", false)
	defer srv.Close()
	p := &AWSProbe{Host: srv.URL}

	if _, err := p.Poll(context.Background()); err != ErrNoNotice {
		t.Fatalf("expected ErrNoNotice, got %v", err)
	}
}

func TestAWSProbeIgnoresRebalanceUnlessOptedIn(t *testing.T) {
	srv := awsServer(t, "", true)
	defer srv.Close()

	quiet := &AWSProbe{Host: srv.URL}
	if _, err := quiet.Poll(context.Background()); err != ErrNoNotice {
		t.Fatalf("rebalance must be ignored by default, got %v", err)
	}

	opted := &AWSProbe{Host: srv.URL, SurgeOnRebalance: true}
	notice, err := opted.Poll(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if notice.Kind != KindRebalance || notice.Certain {
		t.Fatalf("expected an uncertain rebalance notice, got %+v", notice)
	}
}

func TestGCPProbeDetectsPreemption(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Metadata-Flavor") != "Google" {
			t.Error("GCP metadata requests must set Metadata-Flavor: Google")
		}
		_, _ = w.Write([]byte("TRUE"))
	}))
	defer srv.Close()
	p := &GCPProbe{Host: srv.URL}

	notice, err := p.Poll(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if notice.Kind != KindPreemption {
		t.Fatalf("expected preemption, got %+v", notice)
	}
	if time.Until(notice.Deadline) > 5*time.Second {
		t.Fatal("GCP preemption is already underway; the deadline must be immediate")
	}
}

func TestGCPProbeQuiet(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("FALSE"))
	}))
	defer srv.Close()
	p := &GCPProbe{Host: srv.URL}

	if _, err := p.Poll(context.Background()); err != ErrNoNotice {
		t.Fatalf("expected ErrNoNotice, got %v", err)
	}
}

func TestAzureProbeParsesPreemptEvent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Metadata") != "true" {
			t.Error("Azure scheduled events requests must set Metadata: true")
		}
		_, _ = w.Write([]byte(`{"Events":[{"EventType":"Preempt","NotBefore":"Mon, 06 Aug 2026 10:02:00 GMT"}]}`))
	}))
	defer srv.Close()
	p := &AzureProbe{Host: srv.URL}

	notice, err := p.Poll(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if notice.Kind != KindScheduled || !notice.Certain {
		t.Fatalf("expected a certain scheduled event, got %+v", notice)
	}
}

func TestAzureProbeIgnoresUninterestingEvents(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"Events":[{"EventType":"Freeze","NotBefore":"Mon, 06 Aug 2026 10:02:00 GMT"}]}`))
	}))
	defer srv.Close()
	p := &AzureProbe{Host: srv.URL}

	if _, err := p.Poll(context.Background()); err != ErrNoNotice {
		t.Fatalf("Freeze does not terminate the node; expected ErrNoNotice, got %v", err)
	}
}

func TestRunnerTaintsItsOwnNodeWithDeadline(t *testing.T) {
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n1"}}
	c := fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(node).Build()
	deadline := time.Now().Add(2 * time.Minute).Truncate(time.Second)
	r := &Runner{Client: c, NodeName: "n1"}

	if err := r.applyTaint(context.Background(), &Notice{Deadline: deadline, Kind: KindSpotITN, Certain: true}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var got corev1.Node
	if err := c.Get(context.Background(), client.ObjectKey{Name: "n1"}, &got); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got.Spec.Taints) != 1 {
		t.Fatalf("expected exactly one taint, got %d", len(got.Spec.Taints))
	}
	tn := got.Spec.Taints[0]
	if tn.Key != taint.DoomKey || tn.Effect != corev1.TaintEffectNoSchedule {
		t.Fatalf("unexpected taint %+v", tn)
	}
	if tn.Value != strconv.FormatInt(deadline.Unix(), 10) {
		t.Fatalf("taint value must be the unix deadline, got %s", tn.Value)
	}
}

func TestRunnerRebalanceUsesPreferNoSchedule(t *testing.T) {
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n1"}}
	c := fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(node).Build()
	r := &Runner{Client: c, NodeName: "n1"}

	if err := r.applyTaint(context.Background(), &Notice{Deadline: time.Now().Add(10 * time.Minute), Kind: KindRebalance}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var got corev1.Node
	_ = c.Get(context.Background(), client.ObjectKey{Name: "n1"}, &got)
	if got.Spec.Taints[0].Key != taint.RebalanceKey ||
		got.Spec.Taints[0].Effect != corev1.TaintEffectPreferNoSchedule {
		t.Fatalf("rebalance must be a soft hint, got %+v", got.Spec.Taints[0])
	}
}

func TestRunnerTaintIsIdempotent(t *testing.T) {
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n1"}}
	c := fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(node).Build()
	r := &Runner{Client: c, NodeName: "n1"}
	notice := &Notice{Deadline: time.Now().Add(2 * time.Minute), Kind: KindSpotITN}

	for range 3 {
		if err := r.applyTaint(context.Background(), notice); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}

	var got corev1.Node
	_ = c.Get(context.Background(), client.ObjectKey{Name: "n1"}, &got)
	if len(got.Spec.Taints) != 1 {
		t.Fatalf("expected the taint to be applied once, got %d", len(got.Spec.Taints))
	}
}
