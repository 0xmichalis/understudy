# Upgrading

Version-to-version notes. Anything not mentioned here needs no action.

## 0.4.2 to 0.4.3

Argo Rollouts are now supported with `targetRef.kind: Rollout`. Both canary and
blue/green strategies are protected once fully promoted. Install Argo Rollouts
before starting Understudy, or restart Understudy after installing its CRD.
Existing Deployment targets need no configuration changes.

Upgrade the chart to install the Rollout permissions alongside the new image:

```sh
helm upgrade understudy oci://ghcr.io/kylan11/charts/understudy \
  --namespace understudy-system --version 0.4.3
```

If you manage RBAC yourself (`rbac.create: false`), grant `get`, `list`, `watch`,
`update`, and `patch` on `rollouts` in the `argoproj.io` API group.

The eviction webhook now stands down together with the controller during a
target's release. Rollouts that are updating, paused, or aborted are not held;
preview pods cannot count as production replacements. See
[Argo Rollouts](README.md#argo-rollouts) for the full behavior.

## 0.4.0 to 0.4.1

`hostageMode` is now `holdMode`. The old spelling described a budget that
took evictions hostage, and there is no budget any more.

Nothing breaks. Both fields are accepted, `holdMode` wins when both are
set, and a resource that sets only the old one keeps working while the
operator raises a single warning event against it. The field will be
removed in a later release, so rename at your convenience:

```sh
kubectl get understudies -A \
  -o custom-columns=NS:.metadata.namespace,NAME:.metadata.name,OLD:.spec.hostageMode \
  | grep -v '<none>'
```

The `MODE` column of `kubectl get understudies` now reports the mode the
operator actually resolved, from `status.mode`, whichever field set it.

## 0.3.x to 0.4.0

The mechanism changed underneath you. Understudy used to own a
PodDisruptionBudget sized to block every eviction; it now owns nothing and
answers the eviction itself, at admission, with the same 429 the budget
returned. The reason is that a budget is visible to a disrupter before it
acts, so a protected singleton could stop its node from ever being
consolidated, drifted or expired. A hold is invisible until the moment of
the attempt, so that class of stuck node cannot happen.

### What you have to do

Run `helm upgrade`. The rest is automatic, and worth verifying once:

```sh
kubectl get pdb -A -l understudy.sh/owned=true      # expect none
kubectl get validatingwebhookconfiguration | grep understudy
```

At startup the operator sweeps away every budget it used to own, once,
under leader election so it cannot race an old replica. The webhook
configuration is renamed from `<release>-eviction-observer` to
`<release>-eviction-hold`, the failsafe CronJob and its RBAC are removed,
and the serving certificate is carried across the upgrade rather than
regenerated.

### Chart values

| Was | Now |
|---|---|
| `signals.evictionWebhook` | gone; the webhook is the mechanism and is always on |
| `signals.evictionWebhookTimeoutSeconds` | `webhook.timeoutSeconds` |
| `signals.evictionWebhookNamespaceSelector` | `webhook.namespaceSelector` |
| `signals.pinDetection` | gone with the budget it detected |
| `failsafe.*` | gone; there is no budget left to clean up after |

Removed keys in your values file are ignored rather than rejected, so an
upgrade will not fail on them, but they no longer do anything.

### Metrics

| Was | Now |
|---|---|
| `understudy_pdb_relaxed_total` | `understudy_hold_relaxed_total`, same `reason` label |
| `understudy_node_pinned_total` | gone |
| - | `understudy_evictions_held_total`, evictions answered with 429 |

`understudy_oldest_blocked_eviction_seconds` is unchanged and is still the
one to alert on.

### Two behaviours worth knowing

Protection now depends on the API server reaching the webhook. That is the
default nearly everywhere, but a private control plane with narrow security
groups can block it, and because the webhook fails open the result looks
exactly like a healthy install with nothing to do. The check is in
[Requirements](README.md#requirements); run it once after upgrading.

Operator downtime now means no protection, immediately, where a budget
would have kept blocking. That is the deliberate trade for never wedging a
cluster, and the answer to it is `replicaCount: 2`: every replica serves
the webhook, and only reconciliation takes the leader lease.

### Rolling back

`helm rollback` returns you to 0.3.x, which recreates its budgets on the
next reconcile. Nothing in 0.4.0 destroys state that 0.3.x needs.
