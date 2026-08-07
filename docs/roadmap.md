# Roadmap

Milestone 1 (shipped in this repo) is the core surge state machine plus the
two universal signal adapters. Everything below plugs into the same
`signal.Adapter` / `signal.NodeDoom` contract - none of it touches core.

## Milestone 2 - eviction webhook (shipped in v0.2.0)

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

## Milestone 3 - cloud sentinels (shipped in v0.2.0; AWS live-validated, GCP and Azure experimental)

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

- **Budget-pinned nodes are reported, never resolved.** Karpenter dry-runs
  PDBs and refuses to start consolidation, drift or expiry while
  `disruptionsAllowed=0`; cluster-autoscaler pre-checks similarly. The
  hostage PDB can therefore pin a node with no drain signal ever appearing.
  Settled position: understudy detects the refusal and surfaces it (the
  `NodePinned` condition, a Warning event, `understudy_node_pinned_total`)
  and the README documents the mitigations, but the operator does not act.
  Acting would mean initiating pod movement on another controller's
  behalf, which is out of scope by design: understudy reacts to
  disruptions, it never starts one.
- **Azure event approval.** Azure Scheduled Events can be held and approved
  early; the sentinel currently only reads them. Approving once the surge is
  traffic-ready would make Azure the only cloud where an involuntary
  disruption is fully mediated.
- **GCP and Azure sentinels are untested against real infrastructure.** Both
  are covered by fake-metadata-server unit tests only. They must be labelled
  experimental until someone runs them on GKE and AKS.
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
