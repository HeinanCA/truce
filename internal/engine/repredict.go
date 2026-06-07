package engine

import "math"

// RepredictCPU re-runs the HPA's replica math for a NEW per-pod CPU request —
// truce's own recommendation — against an absolute baseline CPU usage in
// milli-cores (p95 or p50 over the window), rather than the snapshot
// utilization the VPA path predicts from. Peak-sizing the request is supposed to
// hold the autoscaler at the current/min replica count instead of scaling out;
// this function proves it.
//
//	predicted_util% = baselineMilli / newReqMilli * 100
//	ratio           = predicted_util / target
//	predicted       = clamp(ceil(N*ratio), min, max)
//
// scalesOut is true when the new request still trips scale-out (ratio > 1+tol),
// which the caller surfaces as HPA-STILL-SCALES. With a non-positive target or
// request the prediction is undefined, so it returns the unchanged count.
func RepredictCPU(target int32, baselineMilli, newReqMilli int64, n, minR, maxR int32, tol float64) (replicas int32, utilPct int32, scalesOut bool) {
	if target <= 0 || newReqMilli <= 0 || baselineMilli < 0 {
		return n, 0, false
	}
	util := float64(baselineMilli) / float64(newReqMilli) * 100
	ratio := util / float64(target)
	replicas = clamp(int32(math.Ceil(float64(n)*ratio)), minR, maxR)
	scalesOut = ratio > 1+tol
	return replicas, int32(math.Round(util)), scalesOut
}
