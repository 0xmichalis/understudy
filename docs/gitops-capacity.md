# GitOps capacity contract

Available starting with **Understudy v0.4.4**, Helm chart **0.4.4**, and its
`apps.understudy.sh/v1alpha1` CRD. Upgrade the CRD and operator together;
v0.4.3 does not support the field.

Git supplies the **desired baseline**. Understudy owns **effective replicas**:
the normal baseline plus temporary capacity while handing over departing pods.
This applies equally to Kubernetes Deployments and Argo Rollouts.

## Configure the two owners

Enable capacity management on the Understudy resource:

```yaml
apiVersion: apps.understudy.sh/v1alpha1
kind: Understudy
metadata:
  name: worker
  namespace: workers
spec:
  targetRef:
    kind: Deployment # or Rollout
    name: worker
  baselineReplicas: 1
```

In the Argo CD Application, delegate the effective field and handover markers
for that specific target:

```yaml
spec:
  ignoreDifferences:
    - group: apps # argoproj.io for a Rollout
      kind: Deployment # Rollout for an Argo Rollout
      name: worker
      namespace: workers
      jsonPointers:
        - /spec/replicas
        - /metadata/annotations/understudy.sh~1base-replicas
        - /metadata/annotations/understudy.sh~1target-revision
  syncPolicy:
    automated:
      prune: true
      selfHeal: true
    syncOptions:
      - RespectIgnoreDifferences=true
```

Do not ignore `Understudy.spec.baselineReplicas`. Argo continues to synchronize
that input, application releases, and unrelated settings normally. The ignore
rules are static and scoped to the protected target: no conditional JQ,
managed-field ownership inference, or manual sync pause is needed.

Consumer charts should render the workload's initial `spec.replicas` and the
Understudy's `spec.baselineReplicas` from **one service capacity value**. On first
creation, Argo applies the workload as rendered; `RespectIgnoreDifferences`
only preserves ignored fields on resources that already exist. Understudy then
corrects any mismatch once both resources exist. A CR created before its target
waits for the target to appear.

## Lifecycle

| Situation | Effective replicas and protection |
|---|---|
| No handover | Understudy reconciles effective replicas to the desired baseline, including live drift. |
| Handover starts | Captures the currently applied capacity as the handover baseline and adds `surgeReplicas`. |
| Baseline changes during handover | Keeps the captured baseline and surge; the latest requested value waits in the CR spec. Intermediate requests are superseded. |
| Replacement becomes ready | The readiness threshold remains the captured baseline, not a pending larger or smaller request. |
| Departing pods leave, or the disruption clears | Removes handover markers and applies the latest desired baseline. |
| Readiness deadline expires | Relaxes the eviction hold; keeps surge capacity until departing pods leave or the disruption clears, then applies the latest baseline. |
| Application release starts during handover | Ends the disruption surge, clears deletion-cost hints, admits evictions, and applies the latest baseline. |
| Application release is updating, paused, or aborted | Disruption protection stands down; ordinary baseline reconciliation continues. Argo Rollouts still owns promotion and traffic routing. |
| Desired baseline is zero | Scales to zero. A zero requested during handover waits like any other change; an already-zero workload does not start a disruption surge. |

The baseline and revision captured for handover live in annotations on the
target. The pending request lives in `Understudy.spec.baselineReplicas`, so a
lost status update cannot discard it. On restart the operator reconstructs
disruption signals and resumes reconciliation. Webhook availability still has
the existing fail-open behavior while the operator is unavailable.

## Observe the result

- `spec.baselineReplicas`: latest Git request.
- `status.baselineReplicas`: applied baseline, frozen during handover.
- `BaselineApplied=False`, reason `HandoverInProgress`: desired baseline is pending.
- `BaselineApplied=True`: baseline has been applied to the workload's replica
  request. This does **not** claim that every requested pod is Ready.
- `SurgeReady` and `Protected`: replacement readiness and eviction-hold behavior.
- Target `spec.replicas`: effective replicas, including a possible surge.

Conditions include `observedGeneration`, so a client can distinguish the latest
request from an older status observation.

## Enable and retire ownership

Use one Understudy resource per target and one capacity writer. An HPA, KEDA,
another Understudy resource, or another GitOps application must not concurrently
write that target's effective replicas. Workloads must still tolerate temporary
overlap; baseline management does not make overlap-unsafe singletons safe.

Omitting `baselineReplicas` keeps reactive-only protection. Removing it stops
continuous baseline reconciliation; an active handover finishes at its captured
baseline. Deleting the CR does not restore replicas: it removes the controller's
input. To retire protection, finish or cancel the handover first, align the Git
replica count with the intended capacity, then remove the CR and its Argo ignore
rules together. Keep a target reference fixed through handover; retargeting a CR
does not clean up the old target.

The documented Argo configuration uses its ordinary apply synchronization path.
Force/replace synchronization is outside this contract because it can recreate
resources and discard the runtime state. Argo's native documentation explains
[RespectIgnoreDifferences](https://argo-cd.readthedocs.io/en/stable/user-guide/sync-options/#respect-ignore-differences-configs).

For Helm upgrades, update the CRD explicitly before using the new field: Helm
does not upgrade CRDs in a chart's `crds/` directory. See [upgrade notes](../UPGRADING.md).

## Verified integration

Validated with Argo CD **3.5.5**, Argo Rollouts **1.10.1**, and Kubernetes
**1.36.4** in isolated Kind clusters, with automatic sync and self-heal enabled.
Deployment, canary, and blue/green targets passed initial installation,
unrelated sync during surge, repeated pending baseline updates, cold operator
restart and leadership recovery, eviction hold/release, final convergence,
steady scaling, HTTP serving, and release takeover. Blue/green also converged a
pending scale-to-zero request and subsequently scaled up from Git.

The test used the ordinary Argo apply path and the scoped ignore rules above.
Server-side apply and force/replace sync were not part of this verification.
