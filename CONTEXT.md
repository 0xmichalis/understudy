# Understudy

Understudy prepares temporary replacement capacity for announced node disruptions.

## Language

**Desired baseline**: The capacity the service author requests outside a disruption.
_Avoid_: Effective replicas, surge capacity

**Effective replicas**: The capacity currently requested from the workload controller,
including any temporary surge.

**Handover baseline**: The baseline committed to an active disruption handover;
later desired-baseline changes do not alter its readiness requirement.

**Handover**: The interval in which Understudy prepares replacement capacity and
allows departing pods to leave, ending when the disruption clears or a release takes over.

**Pending baseline**: The latest desired baseline awaiting completion of a handover.
Intermediate requests are superseded rather than queued.

**Release**: A change to the application's revision, managed by its workload controller.
_Avoid_: Handover, node drain
