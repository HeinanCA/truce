package collect

import (
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/heinanca/truce/internal/model"
)

// resourcesFromList converts a core/v1 ResourceList into model.Resources. CPU
// is recorded as milli-cores, memory as bytes. A resource absent from the list
// stays nil (unset), which the engine reads as "no request declared".
func resourcesFromList(rl corev1.ResourceList) model.Resources {
	var out model.Resources
	if q, ok := rl[corev1.ResourceCPU]; ok {
		out.CPUMilli = model.Int64(q.MilliValue())
	}
	if q, ok := rl[corev1.ResourceMemory]; ok {
		out.MemBytes = model.Int64(q.Value())
	}
	return out
}

// quantityToResources places a single parsed CPU/memory quantity into a
// Resources by name. Used for VPA recommendation fields.
func quantityToResources(name corev1.ResourceName, q resource.Quantity, into *model.Resources) {
	switch name {
	case corev1.ResourceCPU:
		into.CPUMilli = model.Int64(q.MilliValue())
	case corev1.ResourceMemory:
		into.MemBytes = model.Int64(q.Value())
	}
}

// hpaToInfo converts an autoscaling/v2 HPA into model.HPAInfo, joining each
// spec metric with its current value from status.currentMetrics.
func hpaToInfo(hpa autoscalingv2.HorizontalPodAutoscaler) model.HPAInfo {
	info := model.HPAInfo{
		Present:     true,
		Name:        hpa.Name,
		MaxReplicas: hpa.Spec.MaxReplicas,
	}
	if hpa.Spec.MinReplicas != nil {
		info.MinReplicas = *hpa.Spec.MinReplicas
	} else {
		info.MinReplicas = 1 // autoscaling/v2 default
	}

	// Per-direction tolerance (k8s >= 1.33, beta HPAConfigurableTolerance).
	if b := hpa.Spec.Behavior; b != nil {
		if b.ScaleUp != nil && b.ScaleUp.Tolerance != nil {
			info.ScaleUpTolerance = float64Ptr(b.ScaleUp.Tolerance.AsApproximateFloat64())
		}
		if b.ScaleDown != nil && b.ScaleDown.Tolerance != nil {
			info.ScaleDownTolerance = float64Ptr(b.ScaleDown.Tolerance.AsApproximateFloat64())
		}
	}

	for _, ms := range hpa.Spec.Metrics {
		m := metricSpecToModel(ms)
		m.CurrentUtilization = currentUtilFor(hpa.Status.CurrentMetrics, ms)
		info.Metrics = append(info.Metrics, m)
	}
	return info
}

// metricSpecToModel maps one autoscaling/v2 MetricSpec to model.HPAMetric,
// extracting the resource/container/target fields relevant to prediction.
func metricSpecToModel(ms autoscalingv2.MetricSpec) model.HPAMetric {
	m := model.HPAMetric{SourceType: model.MetricSourceType(ms.Type)}

	switch ms.Type {
	case autoscalingv2.ResourceMetricSourceType:
		if r := ms.Resource; r != nil {
			m.ResourceName = model.ResourceName(r.Name)
			m.Identifier = string(r.Name)
			m.TargetType = model.MetricTargetType(r.Target.Type)
			m.TargetUtilization = r.Target.AverageUtilization
		}
	case autoscalingv2.ContainerResourceMetricSourceType:
		if r := ms.ContainerResource; r != nil {
			m.ResourceName = model.ResourceName(r.Name)
			m.ContainerName = r.Container
			m.Identifier = string(r.Name) + "@" + r.Container
			m.TargetType = model.MetricTargetType(r.Target.Type)
			m.TargetUtilization = r.Target.AverageUtilization
		}
	case autoscalingv2.PodsMetricSourceType:
		if r := ms.Pods; r != nil {
			m.Identifier = r.Metric.Name
			m.TargetType = model.MetricTargetType(r.Target.Type)
		}
	case autoscalingv2.ObjectMetricSourceType:
		if r := ms.Object; r != nil {
			m.Identifier = r.Metric.Name
			m.TargetType = model.MetricTargetType(r.Target.Type)
		}
	case autoscalingv2.ExternalMetricSourceType:
		if r := ms.External; r != nil {
			m.Identifier = r.Metric.Name
			m.TargetType = model.MetricTargetType(r.Target.Type)
		}
	}
	return m
}

// currentUtilFor finds the current average utilization reported in
// status.currentMetrics that matches the given spec metric (by type, resource
// name, and — for ContainerResource — container). Returns nil if absent.
func currentUtilFor(statuses []autoscalingv2.MetricStatus, ms autoscalingv2.MetricSpec) *int32 {
	for _, st := range statuses {
		if st.Type != ms.Type {
			continue
		}
		switch ms.Type {
		case autoscalingv2.ResourceMetricSourceType:
			if st.Resource != nil && ms.Resource != nil && st.Resource.Name == ms.Resource.Name {
				return st.Resource.Current.AverageUtilization
			}
		case autoscalingv2.ContainerResourceMetricSourceType:
			if st.ContainerResource != nil && ms.ContainerResource != nil &&
				st.ContainerResource.Name == ms.ContainerResource.Name &&
				st.ContainerResource.Container == ms.ContainerResource.Container {
				return st.ContainerResource.Current.AverageUtilization
			}
		}
	}
	return nil
}

func float64Ptr(v float64) *float64 { return &v }
