package promq

import (
	"fmt"

	"github.com/heinanca/truce/internal/model"
)

// containerFilter restricts a query to the containers a metric considers: a
// single named container for ContainerResource, or all real containers
// (excluding the pause "POD" pseudo-container) for a pod-level Resource metric.
func containerFilter(m *model.HPAMetric) string {
	if m.SourceType == model.MetricContainerResource && m.ContainerName != "" {
		return fmt.Sprintf(`,container="%s"`, m.ContainerName)
	}
	return `,container!="",container!="POD"`
}

// cpuPeakUsageQuery builds PromQL for the peak (quantile) per-pod average CPU
// usage in cores over the window. Averaging per pod makes the value independent
// of the replica count, matching how the HPA normalizes utilization per pod.
func cpuPeakUsageQuery(o Options, ns, podRx, cFilter string) string {
	return fmt.Sprintf(
		`quantile_over_time(%g, avg(sum by (pod) (rate(%s{namespace="%s",pod=~"%s"%s}[5m])))[%s:5m])`,
		o.CPUQuantile, o.CPUMetric, ns, podRx, cFilter, o.Window,
	)
}

// memPeakAvgQuery builds PromQL for the worst-moment per-pod average memory
// working set in bytes (for a memory-Utilization HPA metric).
func memPeakAvgQuery(o Options, ns, podRx, cFilter string) string {
	return fmt.Sprintf(
		`max_over_time(avg(%s{namespace="%s",pod=~"%s"%s})[%s:1m])`,
		o.MemMetric, ns, podRx, cFilter, o.Window,
	)
}

// memPeakMaxQuery builds PromQL for the single worst pod-moment memory working
// set in bytes for one container (for the OOM check — memory is non-compressible,
// so the absolute peak is what matters).
func memPeakMaxQuery(o Options, ns, podRx, container string) string {
	return fmt.Sprintf(
		`max_over_time(max(%s{namespace="%s",pod=~"%s",container="%s"})[%s:1m])`,
		o.MemMetric, ns, podRx, container, o.Window,
	)
}

// consideredRequestSum returns the per-pod request (CPU milli or memory bytes,
// per the metric's resource) summed over the containers the metric considers.
// ok is false when any considered container lacks the request, so the caller
// skips the peak override and lets the engine raise UNRELIABLE.
func consideredRequestSum(containers []model.ContainerAnalysis, m *model.HPAMetric) (int64, bool) {
	pick := func(c model.ContainerAnalysis) (int64, bool) {
		if m.ResourceName == model.ResourceMemory {
			return c.Requests.Mem()
		}
		return c.Requests.CPU()
	}
	if m.SourceType == model.MetricContainerResource {
		for _, c := range containers {
			if c.Name == m.ContainerName {
				return pick(c)
			}
		}
		return 0, false
	}
	var sum int64
	any := false
	for _, c := range containers {
		v, ok := pick(c)
		if !ok {
			return 0, false
		}
		sum += v
		any = true
	}
	return sum, any
}
