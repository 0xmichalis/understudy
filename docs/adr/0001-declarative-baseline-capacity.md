---
status: accepted
---

# Separate desired baseline from effective replicas

GitOps synchronization and Understudy's temporary surge both write workload
replicas. Permanently ignoring replicas prevents Git capacity changes, while
conditional ignores do not preserve the surge through Argo CD 3.5.5's sync
preprocessing. We therefore add opt-in `spec.baselineReplicas`: Git owns the
desired baseline, and Understudy continuously reconciles effective replicas.

During handover, the captured baseline remains the readiness threshold and the
latest desired baseline waits in the resource's spec. Completion, cancellation,
or release takeover applies that latest value. Application release stand-down
disables disruption surging and holding, not ordinary baseline reconciliation.

Argo delegates the effective replica field and Understudy's handover annotations
using its native, statically scoped `ignoreDifferences` and
`RespectIgnoreDifferences=true`. No synchronization wrapper or additional
controller is introduced. Omitting the baseline retains reactive-only
protection; the opt-in contract requires a single capacity writer.

This boundary expansion was approved in Q38, originating from DBRID-2851's
service-chart integration requirement. The operator owns its implementation;
consumer charts derive the desired baseline and first-install replica count
from the same service capacity value.
