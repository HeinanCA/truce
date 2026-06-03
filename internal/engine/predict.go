package engine

import (
	"math"

	"github.com/heinanca/truce/internal/model"
)

// predictResult is the outcome of analyzing all of an HPA's metrics together.
type predictResult struct {
	verdict           model.Verdict
	predictedReplicas int32
	binding           *model.HPAMetric
	predictedUtil     *int32
	tolUp             float64
	tolDown           float64
}

// predictWorkload runs the prediction across every HPA metric and selects the
// binding metric — the one yielding the most replicas, mirroring how the HPA
// itself takes the maximum desired replica count across metrics.
//
// Decoupled metrics (External/Object/Pods, and AverageValue/Value targets) do
// not respond to request changes and are skipped. A coupled metric whose basis
// is incomplete (a considered container without a request, or no current/target
// utilization reported) is skipped for prediction and raises the UNRELIABLE
// flag, so truce never emits a confident number it cannot stand behind.
func predictWorkload(cw model.CollectedWorkload, defaultTol float64, flags *flagSet) predictResult {
	n := cw.Workload.Replicas
	res := predictResult{
		verdict:           model.VerdictSafe,
		predictedReplicas: n,
		tolUp:             defaultTol,
		tolDown:           defaultTol,
	}

	tolUp, tolDown := tolerancesFor(cw.HPA, defaultTol)
	res.tolUp, res.tolDown = tolUp, tolDown

	coupledCount := 0
	bestReplicas := n
	haveReliable := false

	for i := range cw.HPA.Metrics {
		m := &cw.HPA.Metrics[i]
		if !m.IsUtilizationCoupled() {
			continue
		}
		coupledCount++

		mr, ok := predictMetric(m, cw.Containers, n, cw.HPA.MinReplicas, cw.HPA.MaxReplicas, tolUp, tolDown, flags)
		if !ok {
			continue // unreliable; flag already raised
		}
		haveReliable = true

		// Binding = the metric driving the most replicas. Ties keep the first.
		if mr.predictedReplicas > bestReplicas || res.binding == nil {
			bestReplicas = mr.predictedReplicas
			res.predictedReplicas = mr.predictedReplicas
			res.verdict = mr.verdict
			res.binding = m
			pu := mr.predictedUtil
			res.predictedUtil = &pu
		}
	}

	switch {
	case coupledCount == 0:
		// No metric depends on requests at all.
		res.verdict = model.VerdictDecoupled
		res.predictedReplicas = n
		res.binding = nil
		res.predictedUtil = nil
	case !haveReliable:
		// Coupled metrics exist but none could be evaluated reliably.
		res.verdict = model.VerdictSafe
		res.predictedReplicas = n
		res.binding = nil
		res.predictedUtil = nil
	}
	return res
}

// metricResult is the per-metric prediction.
type metricResult struct {
	verdict           model.Verdict
	predictedReplicas int32
	predictedUtil     int32
}

// predictMetric predicts the HPA's response for a single coupled metric.
//
//	predicted_util = current_util * (R_old / R_new)
//	ratio          = predicted_util / target_util
//	  ratio > 1+tol_up   -> SCALE-OUT  (HITS CEILING if clamped to max)
//	  ratio < 1-tol_down -> SCALE-IN
//	  otherwise          -> SAFE
//	predicted_replicas = clamp(ceil(N*ratio), min, max)
//
// R_old/R_new are summed over the containers the metric considers: all
// containers for a pod-level Resource metric, or the single named container for
// a ContainerResource metric. R_new uses the VPA target where present and the
// unchanged current request otherwise. Returns ok=false when the basis is
// incomplete (raising UNRELIABLE).
func predictMetric(m *model.HPAMetric, containers []model.ContainerAnalysis, n, minR, maxR int32, tolUp, tolDown float64, flags *flagSet) (metricResult, bool) {
	util, _ := m.UsageUtil() // peak when present, else snapshot
	if util == nil || m.TargetUtilization == nil || *m.TargetUtilization <= 0 {
		return metricResult{}, false
	}

	considered := consideredContainers(m, containers)
	if len(considered) == 0 {
		return metricResult{}, false
	}

	var rOld, rNew int64
	for _, c := range considered {
		old, ok := dimension(c.Requests, m.ResourceName)
		if !ok {
			// A considered container has no request for this resource: the HPA's
			// utilization basis is untrustworthy.
			flags.add(model.FlagUnreliable)
			return metricResult{}, false
		}
		rOld += old
		if nv, ok := dimension(c.VPA.Target, m.ResourceName); ok {
			rNew += nv // container is being resized
		} else {
			rNew += old // unchanged
		}
	}
	if rOld <= 0 || rNew <= 0 {
		return metricResult{}, false
	}

	predUtil := float64(*util) * float64(rOld) / float64(rNew)
	ratio := predUtil / float64(*m.TargetUtilization)

	mr := metricResult{predictedUtil: int32(math.Round(predUtil))}

	switch {
	case ratio > 1+tolUp:
		repl := clamp(int32(math.Ceil(float64(n)*ratio)), minR, maxR)
		mr.predictedReplicas = repl
		if maxR > 0 && repl == maxR {
			mr.verdict = model.VerdictHitsCeiling
		} else {
			mr.verdict = model.VerdictScaleOut
		}
	case ratio < 1-tolDown:
		mr.predictedReplicas = clamp(int32(math.Ceil(float64(n)*ratio)), minR, maxR)
		mr.verdict = model.VerdictScaleIn
	default:
		mr.predictedReplicas = n
		mr.verdict = model.VerdictSafe
	}
	return mr, true
}

// consideredContainers returns the containers a metric's R_old/R_new sum over.
func consideredContainers(m *model.HPAMetric, containers []model.ContainerAnalysis) []model.ContainerAnalysis {
	if m.SourceType == model.MetricContainerResource {
		for _, c := range containers {
			if c.Name == m.ContainerName {
				return []model.ContainerAnalysis{c}
			}
		}
		return nil
	}
	// Resource metric: pod-level, all containers.
	return containers
}

// dimension extracts the CPU (milli) or memory (bytes) value for a resource name.
func dimension(r model.Resources, name model.ResourceName) (int64, bool) {
	switch name {
	case model.ResourceCPU:
		return r.CPU()
	case model.ResourceMemory:
		return r.Mem()
	default:
		return 0, false
	}
}

// tolerancesFor resolves the effective up/down tolerances: the HPA's
// per-direction spec.behavior values when set, otherwise the default.
func tolerancesFor(hpa model.HPAInfo, def float64) (up, down float64) {
	up, down = def, def
	if hpa.ScaleUpTolerance != nil {
		up = *hpa.ScaleUpTolerance
	}
	if hpa.ScaleDownTolerance != nil {
		down = *hpa.ScaleDownTolerance
	}
	return up, down
}

func clamp(v, lo, hi int32) int32 {
	if v < lo {
		return lo
	}
	if hi > 0 && v > hi {
		return hi
	}
	return v
}
