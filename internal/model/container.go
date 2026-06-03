package model

// VPARec is a VPA recommendation for one container, matched by container name.
// Each bound is a CPU/memory pair; any dimension may be unset.
type VPARec struct {
	Target         Resources // the recommendation truce treats as R_new
	LowerBound     Resources
	UpperBound     Resources
	UncappedTarget Resources
}

// ContainerAnalysis is the per-container view assembled from live pod state,
// the VPA recommendation, and metrics. The engine reads requests and VPA target
// from here; the memory working set drives the OOM-risk check.
type ContainerAnalysis struct {
	Name string

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
	// metrics-server, used to flag OOM risk when VPA memory target is below it.
	// Nil if metrics were unavailable for the container.
	CurrentMemWorkingSet *int64
}
