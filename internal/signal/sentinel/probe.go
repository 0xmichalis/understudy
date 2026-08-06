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
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

type Kind string

const (
	KindSpotITN    Kind = "spot-itn"
	KindRebalance  Kind = "rebalance"
	KindPreemption Kind = "preemption"
	KindScheduled  Kind = "scheduled-event"
)

type Notice struct {
	Deadline time.Time
	Kind     Kind
	Certain  bool
}

type Probe interface {
	Name() string
	Poll(ctx context.Context) (*Notice, error)
}

var ErrNoNotice = errors.New("no notice")

const (
	AWSMetadataHost   = "http://169.254.169.254"
	GCPMetadataHost   = "http://metadata.google.internal"
	AzureMetadataHost = "http://169.254.169.254"
)

type AWSProbe struct {
	Host             string
	Client           *http.Client
	SurgeOnRebalance bool
	token            string
	tokenExpiry      time.Time
}

func (p *AWSProbe) Name() string { return "aws" }

func (p *AWSProbe) host() string {
	if p.Host != "" {
		return p.Host
	}
	return AWSMetadataHost
}

func (p *AWSProbe) httpClient() *http.Client {
	if p.Client != nil {
		return p.Client
	}
	return &http.Client{Timeout: 3 * time.Second}
}

func (p *AWSProbe) ensureToken(ctx context.Context) error {
	if p.token != "" && time.Now().Before(p.tokenExpiry) {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, p.host()+"/latest/api/token", nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-aws-ec2-metadata-token-ttl-seconds", "300")
	resp, err := p.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return errors.New("imds token request failed")
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	p.token = strings.TrimSpace(string(body))
	p.tokenExpiry = time.Now().Add(4 * time.Minute)
	return nil
}

func (p *AWSProbe) get(ctx context.Context, path string) ([]byte, bool, error) {
	if err := p.ensureToken(ctx); err != nil {
		return nil, false, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.host()+path, nil)
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("X-aws-ec2-metadata-token", p.token)
	resp, err := p.httpClient().Do(req)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return nil, false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, false, errors.New("imds request failed")
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, false, err
	}
	return body, true, nil
}

func (p *AWSProbe) Poll(ctx context.Context) (*Notice, error) {
	body, found, err := p.get(ctx, "/latest/meta-data/spot/instance-action")
	if err != nil {
		return nil, err
	}
	if found {
		var action struct {
			Action string `json:"action"`
			Time   string `json:"time"`
		}
		if err := json.Unmarshal(body, &action); err == nil {
			deadline := time.Now().Add(2 * time.Minute)
			if t, err := time.Parse(time.RFC3339, action.Time); err == nil {
				deadline = t
			}
			return &Notice{Deadline: deadline, Kind: KindSpotITN, Certain: true}, nil
		}
	}

	if !p.SurgeOnRebalance {
		return nil, ErrNoNotice
	}
	body, found, err = p.get(ctx, "/latest/meta-data/events/recommendations/rebalance")
	if err != nil {
		return nil, err
	}
	if found {
		var rec struct {
			NoticeTime string `json:"noticeTime"`
		}
		deadline := time.Now().Add(2 * time.Minute)
		if err := json.Unmarshal(body, &rec); err == nil {
			if t, err := time.Parse(time.RFC3339, rec.NoticeTime); err == nil {
				deadline = t.Add(2 * time.Minute)
			}
		}
		return &Notice{Deadline: deadline, Kind: KindRebalance, Certain: false}, nil
	}
	return nil, ErrNoNotice
}

type GCPProbe struct {
	Host   string
	Client *http.Client
}

func (p *GCPProbe) Name() string { return "gcp" }

func (p *GCPProbe) host() string {
	if p.Host != "" {
		return p.Host
	}
	return GCPMetadataHost
}

func (p *GCPProbe) httpClient() *http.Client {
	if p.Client != nil {
		return p.Client
	}
	return &http.Client{Timeout: 90 * time.Second}
}

func (p *GCPProbe) Poll(ctx context.Context) (*Notice, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		p.host()+"/computeMetadata/v1/instance/preempted?wait_for_change=true", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Metadata-Flavor", "Google")
	resp, err := p.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, ErrNoNotice
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(strings.TrimSpace(string(body)), "true") {
		return nil, ErrNoNotice
	}
	return &Notice{Deadline: time.Now(), Kind: KindPreemption, Certain: true}, nil
}

type AzureProbe struct {
	Host   string
	Client *http.Client
}

func (p *AzureProbe) Name() string { return "azure" }

func (p *AzureProbe) host() string {
	if p.Host != "" {
		return p.Host
	}
	return AzureMetadataHost
}

func (p *AzureProbe) httpClient() *http.Client {
	if p.Client != nil {
		return p.Client
	}
	return &http.Client{Timeout: 5 * time.Second}
}

func (p *AzureProbe) Poll(ctx context.Context) (*Notice, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		p.host()+"/metadata/scheduledevents?api-version=2020-07-01", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Metadata", "true")
	resp, err := p.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, ErrNoNotice
	}
	var payload struct {
		Events []struct {
			EventType string `json:"EventType"`
			NotBefore string `json:"NotBefore"`
		} `json:"Events"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	for _, e := range payload.Events {
		switch e.EventType {
		case "Preempt", "Terminate", "Redeploy":
			deadline := time.Now().Add(30 * time.Second)
			if t, err := time.Parse(time.RFC1123, e.NotBefore); err == nil {
				deadline = t
			}
			return &Notice{Deadline: deadline, Kind: KindScheduled, Certain: true}, nil
		}
	}
	return nil, ErrNoNotice
}

func Detect(ctx context.Context, client *http.Client) Probe {
	aws := &AWSProbe{Client: client}
	if err := aws.ensureToken(ctx); err == nil {
		return aws
	}
	gcp := &GCPProbe{Client: client}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, gcp.host()+"/computeMetadata/v1/instance/id", nil)
	if err == nil {
		req.Header.Set("Metadata-Flavor", "Google")
		c := client
		if c == nil {
			c = &http.Client{Timeout: 3 * time.Second}
		}
		if resp, err := c.Do(req); err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return gcp
			}
		}
	}
	return &AzureProbe{Client: client}
}
