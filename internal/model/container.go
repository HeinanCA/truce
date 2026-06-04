package model

// VPARec is a VPA recommendation for one container, matched by container name.
// Each bound is a CPU/memory pair; any dimension may be unset.
type VPARec struct {
	Target         Resources // the recommendation truce treats as R_new
	LowerBound     Resources
	UpperBound     Resources
	UncappedTarget Resources
}

// Spread is the measured usage distribution for one container over the
// Prometheus window, averaged across the workload's pods. CPU values are
// milli-cores, memory values are bytes. Confidence in a recommendation comes
// from this spread (how spiky the series is — see cpu_max/cpu_p95), not from the
// VPA's age or bound width. A nil field means that statistic was not collected
// (no time-series source was queried, or the series returned no data).
type Spread struct {
	CPUP50 *int64 // milli-cores, P50 of per-pod-average CPU usage
	CPUP95 *int64 // milli-cores, P95
	CPUMax *int64 // milli-cores, max over the window
	MemP95 *int64 // bytes, P95 of per-pod-average working set
	MemMax *int64 // bytes, max over the window
}

// ContainerAnalysis is the per-container view assembled from live pod state,
// the VPA recommendation, and metrics. The engine reads requests and VPA target
// from here; the memory working set drives the OOM-risk check.
type ContainerAnalysis struct {
	Name string

	// Spread carries the measured CPU/memory distribution from Prometheus. The
	// recommender sizes requests from it (cpu_max / cpu_p95, mem_max) rather than
	// from the VPA target. Zero value means no time-series data was collected.
	Spread Spread

	// Requests are read from a LIVE running pod (in-place resize can diverge the
	// live values from the template). A dimension is unset when no request is
	// declared, which makes any HPA-considered metric over this container an
	// UNRELIABLE basis.
	Requests Resources

	// TemplateRequests are the requests from the workload pod template, kept only
	// as a drift cross-check against Requests. Not used for prediction.
	TemplateRequests Resources

	// VPA holds the recommendation for this container (matched by name). Zero
	// value means no recommendation was found for the container.
	VPA VPARec

	// HasVPA reports whether a VPA recommendation was matched for this container.
	HasVPA bool

	// CurrentMemWorkingSet is the current memory working set in bytes from
	// metrics-server (instantaneous), used to flag OOM risk when the VPA memory
	// target is below it. Nil if metrics were unavailable for the container.
	CurrentMemWorkingSet *int64

	// PeakMemWorkingSet is an optional max working set in bytes over a window
	// from a time-series source (Prometheus). When set, the engine prefers it for
	// the OOM check — memory is non-compressible, so the worst moment is what
	// matters, not a snapshot. Nil when no time-series source was queried.
	PeakMemWorkingSet *int64

	// PeakCPUUsage is an optional peak CPU usage in milli-cores over a window
	// from Prometheus, independent of any HPA. The recommender floors CPU
	// requests at this value so a recommendation can never fall below what the
	// container actually used at its worst observed moment. Nil when no
	// time-series source was queried.
	PeakCPUUsage *int64
}

// OOMWorkingSet returns the working set to compare the VPA memory target
// against (peak when available, else the snapshot) and whether it came from the
// peak source.
func (c ContainerAnalysis) OOMWorkingSet() (bytes *int64, fromPeak bool) {
	if c.PeakMemWorkingSet != nil {
		return c.PeakMemWorkingSet, true
	}
	return c.CurrentMemWorkingSet, false
}
