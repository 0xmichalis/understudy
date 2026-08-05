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
	"errors"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/understudy-k8s/understudy/internal/signal"
)

var ErrNotImplemented = errors.New("sentinel adapter is milestone 3; see docs/roadmap.md")

type Notice struct {
	Node     string
	Deadline time.Time
	Kind     string
	Certain  bool
}

type Adapter struct{}

func New() *Adapter { return &Adapter{} }

func (a *Adapter) Name() string { return "sentinel" }

func (a *Adapter) SetupWithManager(_ ctrl.Manager, _ *signal.Registry) error {
	return ErrNotImplemented
}
