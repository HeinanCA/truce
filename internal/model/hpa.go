package model

// MetricSourceType mirrors autoscaling/v2 metric source types. It determines
// whether a metric is coupled to container requests (only Resource and
// ContainerResource can be), and is the first gate in the engine.
type MetricSourceType string

const (
	MetricResource          MetricSourceType = "Resource"
	MetricContainerResource MetricSourceType = "ContainerResource"
	MetricPods              MetricSourceType = "Pods"
	MetricObject            MetricSourceType = "Object"
	MetricExternal          MetricSourceType = "External"
)

// MetricTargetType mirrors autoscaling/v2 target types. Only Utilization
// targets are request-coupled; AverageValue and Value are decoupled.
type MetricTargetType string

const (
	TargetUtilization  MetricTargetType = "Utilization"
	TargetAverageValue MetricTargetType = "AverageValue"
	TargetValue        MetricTargetType = "Value"
)

// ResourceName names the resource a Resource/ContainerResource metric tracks.
type ResourceName string

const (
	ResourceCPU    ResourceName = "cpu"
	ResourceMemory ResourceName = "memory"
)

// HPAMetric is one entry from an HPA's spec.metrics, paired with the matching
// current value from status.currentMetrics. The engine treats each metric
// independently and the binding metric is the one yielding the most replicas.
type HPAMetric struct {
	SourceType MetricSourceType
	TargetType MetricTargetType

	// ResourceName is set for Resource and ContainerResource metrics.
	ResourceName ResourceName

	// ContainerName is set for ContainerResource metrics: the single container
	// whose request the metric divides by.
	ContainerName string

	// Identifier is a human-readable label for the metric (resource name,
	// pods/object metric name, or external metric name) used in output.
	Identifier string

	// TargetUtilization is the target average utilization percent. Set only for
	// Utilization targets.
	TargetUtilization *int32

	// CurrentUtilization is the current average utilization percent reported by
	// the HPA's own status.currentMetrics. This — not metrics-server, not the
	// VPA target — is the authoritative usage basis for prediction. Nil if the
	// HPA has not yet reported a value for this metric.
	CurrentUtilization *int32
}

// IsUtilizationCoupled reports whether this metric's behavior changes when
// container requests change. Only Resource/ContainerResource with a Utilization
// target qualify; everything else is DECOUPLED.
func (m HPAMetric) IsUtilizationCoupled() bool {
	if m.TargetType != TargetUtilization {
		return false
	}
	return m.SourceType == MetricResource || m.SourceType == MetricContainerResource
}

// HPAInfo captures the HPA targeting a workload. Present is false when no HPA
// targets the workload (the NO HPA case).
type HPAInfo struct {
	Present     bool
	Name        string
	MinReplicas int32
	MaxReplicas int32
	Metrics     []HPAMetric

	// ScaleUpTolerance/ScaleDownTolerance are the per-direction tolerances from
	// spec.behavior (k8s >= 1.33). Nil means unset; the engine falls back to the
	// --tolerance flag value. Expressed as a fraction (0.10 = 10%).
	ScaleUpTolerance   *float64
	ScaleDownTolerance *float64
}
