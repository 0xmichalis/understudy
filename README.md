# Understudy

**High availability with one replica.** Run singleton workloads on spot nodes
and survive what a cluster does to them, without paying for a second replica
around the clock.

In theatre the understudy waits in the wings and steps in when the lead exits.
This operator does the same for a single-replica Deployment: when a node is
going away, it brings up a stand-in pod, waits until that pod is genuinely
serving traffic, and only then lets the original leave. The second pod exists
for the couple of minutes a disruption is actually in flight.

```yaml
apiVersion: apps.understudy.dev/v1alpha1
kind: Understudy
metadata:
  name: my-service
spec:
  targetRef:
    name: my-service
  hostageMode: always
  minSurgeTimeSeconds: 60
  readinessDeadlineSeconds: 600
```

## Why not just run two replicas

Two replicas is rent paid every hour of every day against an event that lasts
minutes a month. It also halves your capacity headroom during every drain, and
for workloads that are singletons by nature (leader-elected controllers,
schedulers, queue consumers with sticky assignment) the second replica is idle
spend that buys nothing.

Understudy's bet is narrower: your workload can tolerate two pods for a few
minutes. That is the same property a rolling update with `maxSurge: 1` already
requires, so if you deploy without downtime today, you qualify. The second pod
then exists only while a disruption is happening.

## What it survives

| Kind of disruption | Examples | Time available |
|---|---|---|
| Voluntary | node drain, Karpenter drift and consolidation and expiry, node pool upgrades, manual `kubectl drain` | unbounded, the eviction is held |
| Involuntary | spot and preemptible reclaim, host maintenance | fixed deadline, seconds to minutes |
| Deploys | rolling update of the workload itself | not our business, Understudy stands down |

## Results

Measured on EKS with Karpenter on arm64 spot nodes, one replica behind an
application load balancer, with no spare capacity so every replacement waited
for a new EC2 instance. The availability probe ran at roughly three requests
per second.

| Event | Failed requests |
|---|---|
| Nothing happening (control) | 0 of 120 |
| `kubectl drain` | 1 of 350 |
| Karpenter disruption | 1 of 189 |
| Real AWS spot interruption | 1 of 764 |
| Ordinary rolling update, no Understudy involved (control) | 2 of 38 |

The last row is the important one. The handful of requests lost during a
disruption are lost as the old pod exits, and an ordinary deploy of the same
workload loses more. Understudy makes a node disruption cost about what a
routine deploy costs.

## Install

```sh
helm install understudy oci://ghcr.io/kylan11/charts/understudy \
  --namespace understudy-system --create-namespace
```

Or from a checkout:

```sh
helm install understudy ./charts/understudy \
  --namespace understudy-system --create-namespace
```

Then protect a workload by creating an `Understudy` next to it. `kubectl get
understudies` shows the target, the mode, the phase, and how long anything has
been blocked.

On clusters where every node carries a taint, the operator needs a matching
toleration and node selector, the same as any other controller you run there.

### Cloud sentinels

Optional, and only useful on preemptible or spot capacity. The sentinel is a
DaemonSet that reads the cloud's termination notice and tells the operator how
long is left, which is what lets the operator decide between holding the
eviction and releasing it immediately.

```sh
helm upgrade understudy ./charts/understudy \
  --set sentinel.enabled=true \
  --set sentinel.cloud=aws \
  --set 'sentinel.nodeSelector.karpenter\.sh/nodepool=spot'
```

The AWS probe has been validated against real spot interruptions. The GCP and
Azure probes are covered by unit tests only. Treat them as experimental until
someone runs them on GKE and AKS.

## Configuration

| Field | Default | What it does |
|---|---|---|
| `targetRef.name` | required | The Deployment to protect, in the same namespace |
| `surgeReplicas` | 1 | How many stand-ins to add |
| `minSurgeTimeSeconds` | 60 | How long this workload realistically takes to start serving. If a termination deadline is nearer than this, the eviction is released immediately instead of being held |
| `readinessDeadlineSeconds` | 600 | How long to hold an eviction with no progress before giving up and letting it through |
| `hostageMode` | `always` | `always`, `voluntary-only`, or `off` |

If you run on spot without the sentinel, set `readinessDeadlineSeconds` below
the platform's notice window, otherwise the operator can hold an eviction
against a deadline it cannot see.

## How it works

Briefly: a PodDisruptionBudget sized to block every eviction, a surge when a
node is reported doomed, a release once the replacement passes its readiness
gates, and a scale-back that removes the right pod. The full explanation, with
diagrams and the reasoning behind each decision, is in
[docs/how-it-works.md](docs/how-it-works.md).

Signals reach the operator through four adapters, and the core contains no
provider-specific code:

| Adapter | Sees |
|---|---|
| Cordon watch | `kubectl drain`, GKE and AKS and EKS upgrades, cluster-autoscaler, kured |
| Taint watch | Karpenter, cluster-autoscaler, plus any key you configure |
| Eviction webhook | every eviction attempt from every drainer, including ones nothing else detects |
| Cloud sentinel | spot and preemptible notices, with their deadlines |

## Safety

Alert on `understudy_oldest_blocked_eviction_seconds`. It exists so that a held
eviction is always visible and bounded.

If a surge cannot make progress, the budget is relaxed rather than stalling the
drain forever. If the operator itself disappears, a CronJob removes the budgets
it left behind and the cluster returns to plain Kubernetes behaviour. There is
a manual version of the same thing:

```sh
kubectl delete pdb -A -l understudy.dev/owned=true
```

The operator refuses to manage itself, skips targets it cannot help, and stands
down while a rollout is in progress.

## Limits

Understudy guarantees a ready replacement. It does not save requests already in
flight to the departing pod, which is a workload concern: give the pod a
`preStop` hook that outlasts your load balancer's deregistration delay.

On an involuntary deadline the handover is a race against the platform. AWS
gives about two minutes, which a fast-booting workload can win even when a new
node has to be provisioned first. GCP and Azure give about thirty seconds,
which cannot be won cold, so there the value is an orderly release and an early
replacement rather than zero downtime.

Karpenter checks disruption budgets before consolidating, so a protected
workload can stop its node from being consolidated. You keep availability and
lose some bin-packing.

Workloads that cannot run two instances at once, including single-replica
StatefulSets, are out of scope and always will be.

## Development

```sh
make test    # envtest suite, downloads Kubernetes binaries on first run
make lint
make build
```

## License

Apache 2.0
