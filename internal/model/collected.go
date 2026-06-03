package model

import "time"

// CollectedWorkload is the assembled, k8s-free input the collect layer hands to
// the engine: one workload with its HPA, per-container live requests, VPA recs,
// and metrics. The engine consumes this and returns a completed
// WorkloadAnalysis (verdict, flags, predicted replicas, footprint delta).
type CollectedWorkload struct {
	Workload   Workload
	HPA        HPAInfo // HPA.Present == false when no HPA targets the workload
	Containers []ContainerAnalysis

	// VPACreated is the VPA object's creation time, used to flag LOW-CONF for
	// young recommendations. Zero value means no VPA matched (or time unknown).
	VPACreated time.Time
}

// HasAnyVPARec reports whether at least one container carries a VPA
// recommendation — the gate for a workload being actionable.
func (c CollectedWorkload) HasAnyVPARec() bool {
	for _, ca := range c.Containers {
		if ca.HasVPA {
			return true
		}
	}
	return false
}
