package promq

import (
	"context"
	"fmt"
	"math"

	"github.com/heinanca/truce/internal/model"
)

// Enrich returns copies of the workloads with peak utilization (per coupled HPA
// metric) and peak memory working set (per VPA-recommended container) filled in
// from Prometheus. Failures are non-fatal: a workload whose query errors or
// returns no data keeps its snapshot basis, and the reason is collected as a
// warning. The input slice is not mutated.
func Enrich(ctx context.Context, c *Client, workloads []model.CollectedWorkload, o Options) ([]model.CollectedWorkload, []string) {
	out := make([]model.CollectedWorkload, len(workloads))
	var warns []string

	for i, cw := range workloads {
		enriched := cw
		ns := cw.Workload.Namespace
		rx := podRegex(cw.Workload)

		// Per-metric peak utilization.
		enriched.HPA.Metrics = peakMetrics(ctx, c, o, ns, rx, cw, &warns)
		// Per-container peak working set for the OOM check.
		enriched.Containers = peakContainers(ctx, c, o, ns, rx, cw.Containers, &warns)

		out[i] = enriched
	}
	return out, warns
}

// peakMetrics returns a copy of the workload's metrics with PeakUtilization set
// where a coupled metric had a queryable basis.
func peakMetrics(ctx context.Context, c *Client, o Options, ns, rx string, cw model.CollectedWorkload, warns *[]string) []model.HPAMetric {
	metrics := make([]model.HPAMetric, len(cw.HPA.Metrics))
	copy(metrics, cw.HPA.Metrics)

	for i := range metrics {
		m := &metrics[i]
		if !m.IsUtilizationCoupled() {
			continue
		}
		req, ok := consideredRequestSum(cw.Containers, m)
		if !ok || req <= 0 {
			continue // engine will flag UNRELIABLE
		}
		cf := containerFilter(m)

		var peakUsage float64
		var found bool
		var err error
		if m.ResourceName == model.ResourceMemory {
			peakUsage, found, err = c.queryScalar(ctx, memPeakAvgQuery(o, ns, rx, cf))
		} else {
			peakUsage, found, err = c.queryScalar(ctx, cpuPeakUsageQuery(o, ns, rx, cf))
		}
		if err != nil {
			*warns = append(*warns, fmt.Sprintf("%s metric %s: %v", cw.Workload.Key(), m.Identifier, err))
			continue
		}
		if !found {
			continue
		}

		// Convert peak usage to a utilization percent against the per-pod request.
		var usageInReqUnits float64
		if m.ResourceName == model.ResourceMemory {
			usageInReqUnits = peakUsage // bytes
		} else {
			usageInReqUnits = peakUsage * 1000 // cores -> milli, matching request units
		}
		util := int32(math.Round(usageInReqUnits / float64(req) * 100))
		m.PeakUtilization = &util
	}
	return metrics
}

// peakContainers returns a copy of the containers with PeakMemWorkingSet set for
// each VPA-recommended container that has memory data.
func peakContainers(ctx context.Context, c *Client, o Options, ns, rx string, in []model.ContainerAnalysis, warns *[]string) []model.ContainerAnalysis {
	containers := make([]model.ContainerAnalysis, len(in))
	copy(containers, in)

	for i := range containers {
		ca := &containers[i]
		if !ca.HasVPA {
			continue
		}
		if _, ok := ca.VPA.Target.Mem(); !ok {
			continue // no memory target -> no OOM comparison to refine
		}
		bytes, found, err := c.queryScalar(ctx, memPeakMaxQuery(o, ns, rx, ca.Name))
		if err != nil {
			*warns = append(*warns, fmt.Sprintf("container %s memory: %v", ca.Name, err))
			continue
		}
		if !found {
			continue
		}
		b := int64(bytes)
		ca.PeakMemWorkingSet = &b
	}
	return containers
}
