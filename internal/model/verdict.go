package model

// Verdict is the primary per-workload conclusion. Exactly one verdict applies
// to a workload; advisory Flags layer on top.
type Verdict string

const (
	// VerdictSafe: applying the VPA rec keeps the HPA within tolerance.
	VerdictSafe Verdict = "SAFE"
	// VerdictScaleOut: the smaller request pushes predicted utilization above
	// target, so the HPA adds replicas.
	VerdictScaleOut Verdict = "SCALE-OUT"
	// VerdictHitsCeiling: predicted scale-out clamps at maxReplicas.
	VerdictHitsCeiling Verdict = "HITS CEILING"
	// VerdictScaleIn: the larger request drops predicted utilization below
	// target, so the HPA removes replicas.
	VerdictScaleIn Verdict = "SCALE-IN"
	// VerdictOOMRisk: VPA memory target is below the current working set.
	VerdictOOMRisk Verdict = "OOM RISK"
	// VerdictDecoupled: the HPA's metric does not depend on requests.
	VerdictDecoupled Verdict = "DECOUPLED"
	// VerdictNoHPA: workload has a VPA but no HPA — plain rightsizing.
	VerdictNoHPA Verdict = "NO HPA"
)

// Flag is an advisory marker that can accompany any verdict.
type Flag string

const (
	// FlagLowConf: VPA is young (< ~48h) or its bounds are wide.
	FlagLowConf Flag = "LOW-CONF"
	// FlagUnreliable: an HPA-considered container has no request set, so the
	// utilization basis cannot be trusted.
	FlagUnreliable Flag = "UNRELIABLE"
	// FlagGitOps: workload is managed by Argo CD or Flux; live edits revert.
	FlagGitOps Flag = "GITOPS"
	// FlagRestart: in-place resize is unavailable, so applying the rec restarts
	// the pod.
	FlagRestart Flag = "RESTART"
	// FlagKEDA: the workload is autoscaled by KEDA on an external trigger, so its
	// replica count is decoupled from CPU/memory requests.
	FlagKEDA Flag = "KEDA"
	// FlagSpiky: cpu_max/cpu_p95 exceeds the spikiness threshold — the workload
	// has rare large bursts, so the recommendation sizes to the peak and the row
	// surfaces p50/p95/max.
	FlagSpiky Flag = "SPIKY"
	// FlagOOMGuardClamped: the proposed memory request fell below the observed
	// peak working set and was clamped up to peak×1.1 so it can never OOM.
	FlagOOMGuardClamped Flag = "OOM-GUARD-CLAMPED"
	// FlagHPAStillScales: even after sizing CPU to the peak, the re-predicted HPA
	// still scales out — headroom (maxReplicas) or the target needs attention.
	FlagHPAStillScales Flag = "HPA-STILL-SCALES"
	// FlagVPADiverges: truce's peak-based recommendation and the VPA target differ
	// by more than 50% — a cross-check that the VPA estimate is off.
	FlagVPADiverges Flag = "VPA-DIVERGES"
)

// IsProblem reports whether a verdict should sort ahead of benign rows in the
// default problems-first ordering.
func (v Verdict) IsProblem() bool {
	switch v {
	case VerdictScaleOut, VerdictHitsCeiling, VerdictScaleIn, VerdictOOMRisk:
		return true
	default:
		return false
	}
}
