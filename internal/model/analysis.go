package model

// WorkloadAnalysis is one output row: a workload plus everything truce concluded
// about it. The engine produces this from the collected inputs; the renderer
// consumes it. Per-container detail lives in Containers.
type WorkloadAnalysis struct {
	Workload   Workload
	HPA        HPAInfo
	Containers []ContainerAnalysis

	// Actionable is false when the workload has no VPA recommendation, so there
	// is nothing to advise; the renderer skips such rows. When false, Verdict is
	// the zero value.
	Actionable bool

	Verdict Verdict
	Flags   []Flag

	// CurrentReplicas is N at analysis time (mirrors Workload.Replicas, kept here
	// so the row is self-contained).
	CurrentReplicas int32
	// PredictedReplicas is the HPA's predicted replica count after applying the
	// VPA rec, clamped to [min, max]. Equals CurrentReplicas when SAFE/DECOUPLED/
	// NO HPA.
	PredictedReplicas int32

	// BindingMetric is the HPA metric that yielded PredictedReplicas (the max
	// across metrics). Nil when no coupled metric drove the prediction.
	BindingMetric *HPAMetric
	// PredictedUtilization is the predicted utilization percent for the binding
	// metric, for the wide view. Nil when not applicable.
	PredictedUtilization *int32
	// ToleranceUp/ToleranceDown are the effective tolerances used (from behavior
	// or the flag fallback), surfaced in the wide view.
	ToleranceUp   float64
	ToleranceDown float64

	// FootprintDelta is the headline change if the rec is applied:
	// PredictedReplicas*R_new - CurrentReplicas*R_old.
	FootprintDelta Delta
}

// HasFlag reports whether the analysis carries the given advisory flag.
func (a WorkloadAnalysis) HasFlag(f Flag) bool {
	for _, got := range a.Flags {
		if got == f {
			return true
		}
	}
	return false
}
