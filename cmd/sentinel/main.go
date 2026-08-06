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

package main

import (
	"context"
	"flag"
	"os"
	"strconv"
	"time"

	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/kylan11/understudy/internal/signal/sentinel"
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
}

func main() {
	var mode, cloud, nodeName, staleAfter string
	var surgeOnRebalance bool
	var interval time.Duration
	flag.StringVar(&mode, "mode", "sentinel", "Operating mode: sentinel or janitor.")
	flag.StringVar(&cloud, "cloud", "auto", "Cloud metadata dialect to probe: aws, gcp, azure or auto.")
	flag.StringVar(&nodeName, "node-name", os.Getenv("NODE_NAME"), "Node this sentinel is responsible for.")
	flag.BoolVar(&surgeOnRebalance, "surge-on-rebalance", false,
		"Treat AWS rebalance recommendations as doom signals. They indicate elevated risk, not a committed reclaim.")
	flag.DurationVar(&interval, "interval", 2*time.Second, "Metadata poll interval.")
	flag.StringVar(&staleAfter, "stale-after", "15m",
		"Janitor mode: delete owned PDBs whose heartbeat is older than this.")
	opts := zap.Options{Development: false}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	log := ctrl.Log.WithName(mode)
	ctx := ctrl.SetupSignalHandler()

	cfg, err := ctrl.GetConfig()
	if err != nil {
		log.Error(err, "failed to load kubeconfig")
		os.Exit(1)
	}
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		log.Error(err, "failed to build client")
		os.Exit(1)
	}

	switch mode {
	case "janitor":
		d, err := time.ParseDuration(staleAfter)
		if err != nil {
			log.Error(err, "invalid stale-after")
			os.Exit(1)
		}
		if err := runJanitor(logf.IntoContext(ctx, log), c, d); err != nil {
			log.Error(err, "janitor failed")
			os.Exit(1)
		}
	case "sentinel":
		if nodeName == "" {
			log.Error(nil, "node-name is required (set NODE_NAME via the downward API)")
			os.Exit(1)
		}
		runner := &sentinel.Runner{
			Client:   c,
			Probe:    selectProbe(ctx, cloud, surgeOnRebalance),
			NodeName: nodeName,
			Interval: interval,
		}
		if err := runner.Run(ctx); err != nil {
			log.Error(err, "sentinel failed")
			os.Exit(1)
		}
	default:
		log.Error(nil, "unknown mode", "mode", mode)
		os.Exit(1)
	}
}

func selectProbe(ctx context.Context, cloud string, surgeOnRebalance bool) sentinel.Probe {
	switch cloud {
	case "aws":
		return &sentinel.AWSProbe{SurgeOnRebalance: surgeOnRebalance}
	case "gcp":
		return &sentinel.GCPProbe{}
	case "azure":
		return &sentinel.AzureProbe{}
	default:
		p := sentinel.Detect(ctx, nil)
		if aws, ok := p.(*sentinel.AWSProbe); ok {
			aws.SurgeOnRebalance = surgeOnRebalance
		}
		return p
	}
}

func runJanitor(ctx context.Context, c client.Client, staleAfter time.Duration) error {
	log := logf.FromContext(ctx)
	var pdbs policyv1.PodDisruptionBudgetList
	if err := c.List(ctx, &pdbs, client.MatchingLabels{"understudy.sh/owned": "true"}); err != nil {
		return err
	}
	now := time.Now()
	deleted := 0
	for i := range pdbs.Items {
		pdb := &pdbs.Items[i]
		raw := pdb.Annotations["understudy.sh/heartbeat"]
		secs, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			log.Info("owned PDB has no usable heartbeat; leaving it alone",
				"pdb", pdb.Namespace+"/"+pdb.Name)
			continue
		}
		age := now.Sub(time.Unix(secs, 0))
		if age < staleAfter {
			continue
		}
		if err := c.Delete(ctx, pdb); err != nil {
			log.Error(err, "failed to delete stale PDB", "pdb", pdb.Namespace+"/"+pdb.Name)
			continue
		}
		deleted++
		log.Info("deleted stale hostage PDB; operator appears to be gone",
			"pdb", pdb.Namespace+"/"+pdb.Name, "heartbeatAge", age.String())
	}
	log.Info("janitor finished", "ownedPDBs", len(pdbs.Items), "deleted", deleted)
	return nil
}
