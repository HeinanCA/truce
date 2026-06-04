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
		cf := fmt.Sprintf(`,container="%s"`, ca.Name)

		// CPU spread (milli) for EVERY container — the recommender sizes from
		// cpu_max (HPA-coupled) or cpu_p95 (Burstable), and re-predicts the HPA
		// from the chosen baseline. Independent of any HPA on the workload.
		ca.Spread.CPUP50 = cpuScalar(ctx, c, cpuQuantileQuery(o, ns, rx, cf, 0.5), ca.Name, "cpu_p50", warns)
		ca.Spread.CPUP95 = cpuScalar(ctx, c, cpuQuantileQuery(o, ns, rx, cf, o.CPUQuantile), ca.Name, "cpu_p95", warns)
		ca.Spread.CPUMax = cpuScalar(ctx, c, cpuMaxQuery(o, ns, rx, cf), ca.Name, "cpu_max", warns)
		// Legacy floor field: the worst observed CPU moment.
		ca.PeakCPUUsage = ca.Spread.CPUMax

		// Memory spread (bytes). mem_max drives the request and the OOM guard;
		// mem_p95 is surfaced for context.
		ca.Spread.MemP95 = memScalar(ctx, c, memQuantileQuery(o, ns, rx, ca.Name, o.CPUQuantile), ca.Name, "mem_p95", warns)
		ca.Spread.MemMax = memScalar(ctx, c, memPeakMaxQuery(o, ns, rx, ca.Name), ca.Name, "mem_max", warns)
		// Legacy OOM-floor field: the worst observed working set.
		ca.PeakMemWorkingSet = ca.Spread.MemMax
	}
	return containers
}

// cpuScalar runs a CPU query (result in cores) and returns the value in
// milli-cores, or nil when there was no data. Errors are collected as warnings.
func cpuScalar(ctx context.Context, c *Client, q, container, label string, warns *[]string) *int64 {
	cores, found, err := c.queryScalar(ctx, q)
	if err != nil {
		*warns = append(*warns, fmt.Sprintf("container %s %s: %v", container, label, err))
		return nil
	}
	if !found {
		return nil
	}
	milli := int64(cores * 1000)
	return &milli
}

// memScalar runs a memory query (result in bytes) and returns the value, or nil
// when there was no data. Errors are collected as warnings.
func memScalar(ctx context.Context, c *Client, q, container, label string, warns *[]string) *int64 {
	bytes, found, err := c.queryScalar(ctx, q)
	if err != nil {
		*warns = append(*warns, fmt.Sprintf("container %s %s: %v", container, label, err))
		return nil
	}
	if !found {
		return nil
	}
	b := int64(bytes)
	return &b
}
