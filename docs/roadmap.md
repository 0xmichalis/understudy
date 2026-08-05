# Roadmap

Milestone 1 (shipped in this repo) is the core surge state machine plus the
two universal signal adapters. Everything below plugs into the same
`signal.Adapter` / `signal.NodeDoom` contract - none of it touches core.

## Milestone 2 - eviction webhook (`internal/signal/evictionwebhook`)

An observe-only ValidatingWebhook on `pods/eviction` CREATE: the universal
fallback that sees every eviction attempt from every drainer past, present
and future - including attempts the PDB then rejects with 429 (empirically
verified during the project's analysis phase; admission runs before the
eviction registry's PDB check). Design points:

- **Fail-open, always-allow.** `failurePolicy: Ignore`, admits everything;
  the PDB does the blocking. Operator down -> plain-Kubernetes behavior.
- The AdmissionReview carries pod name/namespace and the evictor's
  `userInfo` - free `NodeDoom.Source` attribution (which service account is
  draining).
- Signal shape: `NodeDoom{Class: VoluntaryUnbounded}` for the pod's node -
  an evictor that retries is by definition waiting on us.
- Needs cert management (cert-manager or built-in rotation) - the reason
  it's not in the bootstrap milestone.

## Milestone 3 - cloud sentinels (`internal/signal/sentinel`)

Per-node probes (DaemonSet) that watch each cloud's metadata channel and
normalize to `NodeDoom{Involuntary, deadline}`. Deadlines are data measured
from the cloud's own notice - never constants. Per-cloud notes (full signal
matrix in the README):

- **AWS**: IMDS spot ITN (~120s) + rebalance recommendation (early,
  non-guaranteed - surging on it is a churn/cost knob, off by default).
- **GCP**: metadata `preempted` hanging-GET (~30s total, ACPI fires
  immediately by default) - always below `minSurgeTimeSeconds`, so the value
  is fast *release*, not surge.
- **Azure**: Scheduled Events (Preempt 30s; Redeploy/Terminate 5-15 min) -
  uniquely, events can be **held and approved early** once the surge
  completes; the sentinel should exploit this.

This is the only place cloud SDKs/endpoints will ever live.

## Post-M3 / open design items

- **Disruption-candidate signals.** Karpenter dry-runs PDBs and refuses to
  start consolidation while `disruptionsAllowed=0`; cluster-autoscaler
  pre-checks similarly. The hostage PDB can therefore suppress the very
  drain signal we wait for - nodes silently never consolidate. Candidate
  ideas: watch CA's `DeletionCandidateOfClusterAutoscaler` soft taint
  (already a default key), Karpenter `NodeClaim` disruption conditions, or a
  periodic "would this node consolidate if allowed?" probe that pre-surges
  and briefly relaxes.
- **Doomed-node scheduling exclusion** for signals that carry no taint
  (sentinel-only dooms, `PreferNoSchedule` soft taints): the surge pod can
  land back on the dying node. Today core just refuses to count such a pod
  as viable (so the surge retries/TTLs); a transient webhook-injected node
  anti-affinity would prevent it outright.
- **GitOps coexistence.** Argo CD / Flux will fight the replica bump.
  Mitigations to document/implement: `ignoreDifferences` recipes, or driving
  the surge through a separate standby Deployment instead of `spec.replicas`.
- **EvictionRequest API migration.** KEP-4563 (alpha targeted v1.37) gives
  controllers a first-class hook on eviction intent. When it lands and
  drainers adopt it, Understudy becomes an EvictionRequest *responder* that
  surges before acking - same state machine, better signal, no hostage PDB.
  The `signal.Adapter` contract is where it plugs in.
- **kubectl plugin** (`kubectl understudy status`) for incident-time
  visibility.
