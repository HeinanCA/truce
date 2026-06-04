// Package recommend turns a WorkloadAnalysis into concrete, actionable request
// values — the synthesis that neither VPA nor HPA produces alone:
//
//   - CPU is set so the HPA sits at its target *under peak load* (recovers the
//     over-provisioning without waking the autoscaler), instead of the raw VPA
//     target which can trigger scale-out or peg the ceiling.
//   - Memory is floored at the observed peak working set (plus margin), so a
//     downsize can never drop a request below real usage and cause an OOM.
//   - When even a sane request leaves the workload hitting its ceiling, the
//     recommendation says to raise maxReplicas — something neither tool reports.
//
// It is pure: it reads a model.WorkloadAnalysis (already carrying peak data when
// Prometheus was queried) and returns numbers. No cluster or file access.
package recommend

import (
	"fmt"
	"math"
	"strings"

	"github.com/heinanca/truce/internal/model"
)

// MemoryMargin / CPUMargin are added above the observed peak when sizing
// requests, so a recommendation sits comfortably above the worst observed
// moment and can never starve or OOM the workload.
const (
	MemoryMargin = 1.15
	CPUMargin    = 1.15
)

// ContainerRec is the recommendation for one container.
type ContainerRec struct {
	Name string

	CPUNow *int64 // current request (milli), nil if unset
	CPURec *int64 // recommended request (milli)
	CPUWhy string

	MemNow *int64 // current request (bytes), nil if unset
	MemRec *int64 // recommended request (bytes)
	MemWhy string
}

// Recommendation is the full per-service result.
type Recommendation struct {
	Service    string
	Containers []ContainerRec

	// RaiseMaxTo is non-zero when the workload genuinely needs more headroom
	// than its HPA maxReplicas allows even at a sane request.
	RaiseMaxTo int32

	// Provisional is true when the VPA is low-confidence (young / wide bounds).
	Provisional bool

	// Contrast is the one-line "VPA alone would do X" proof-of-value note.
	Contrast string
}

// For builds the recommendation from an analyzed workload.
func For(a model.WorkloadAnalysis) Recommendation {
	r := Recommendation{
		Service:     a.Workload.Name,
		Provisional: a.HasFlag(model.FlagLowConf),
	}

	cpuMetric := coupledMetric(a.HPA, model.ResourceCPU)
	memMetric := coupledMetric(a.HPA, model.ResourceMemory)

	for _, c := range a.Containers {
		rec := ContainerRec{Name: c.Name}
		rec.CPUNow = c.Requests.CPUMilli
		rec.MemNow = c.Requests.MemBytes

		rec.CPURec, rec.CPUWhy = recommendCPU(c, cpuMetric, a.HPA.ManagedByKEDA)
		rec.MemRec, rec.MemWhy = recommendMem(c, memMetric)
		r.Containers = append(r.Containers, rec)
	}

	r.RaiseMaxTo = ceilingHeadroom(a)
	r.Contrast = contrast(a)
	if a.HPA.ManagedByKEDA {
		r.Contrast = kedaNote(a.HPA)
	}
	return r
}

// kedaNote explains why a KEDA-managed workload's requests are safe to change:
// its replica count is driven by an external trigger, not by requests.
func kedaNote(hpa model.HPAInfo) string {
	trig := "an external trigger"
	if len(hpa.KEDATriggers) > 0 {
		trig = strings.Join(hpa.KEDATriggers, ", ")
	}
	return fmt.Sprintf("scaled by KEDA on %s — replicas are external, so request changes are safe; "+
		"truce rightsizes only and cannot predict KEDA's replica count.", trig)
}

// recommendCPU returns the HPA-stable CPU request when an HPA utilization metric
// governs CPU, else the VPA target (no HPA coupling → replicas are fixed).
func recommendCPU(c model.ContainerAnalysis, m *model.HPAMetric, keda bool) (*int64, string) {
	now, hasNow := c.Requests.CPU()

	// 1. Base value: HPA-stable when a CPU HPA governs it, else the VPA target.
	var base int64
	var why string
	switch {
	case m != nil && hasNow && hpaStable(m):
		util, _ := m.UsageUtil()
		stable := float64(now) * float64(*util) / float64(*m.TargetUtilization)
		base = int64(math.Round(stable))
		if base < 1 {
			base = 1
		}
		why = fmt.Sprintf("holds the HPA at %d%% under peak load (no scale-out)", *m.TargetUtilization)
	default:
		v, ok := c.VPA.Target.CPU()
		if !ok {
			return nil, ""
		}
		base = v
		if keda {
			why = "VPA target — KEDA scales on an external trigger, so the CPU request doesn't affect replicas"
		} else {
			why = "VPA target — no HPA on CPU, so replicas are unaffected"
		}
	}

	// 2. SAFETY INVARIANT — never recommend below real usage, never cut without
	// peak evidence. The floor (7-day observed peak) holds even when the VPA is
	// young, so a downsize can never starve the workload.
	if c.PeakCPUUsage == nil {
		if hasNow && base < now {
			n := now
			return &n, "HOLD — no peak CPU data; not cutting below current (run with --prometheus)"
		}
		b := base
		return &b, why
	}
	floor := int64(float64(*c.PeakCPUUsage) * CPUMargin)
	if floor > base {
		return &floor, fmt.Sprintf("floored at observed peak CPU +%d%% — safe to apply", int((CPUMargin-1)*100))
	}
	b := base
	return &b, why
}

// hpaStable reports whether a metric can yield an HPA-stable request.
func hpaStable(m *model.HPAMetric) bool {
	util, _ := m.UsageUtil()
	return util != nil && m.TargetUtilization != nil && *m.TargetUtilization > 0
}

// recommendMem returns a memory request floored at the observed peak (plus
// margin), never below real usage. When a memory HPA governs the container, the
// floor is raised to keep the HPA stable too.
func recommendMem(c model.ContainerAnalysis, m *model.HPAMetric) (*int64, string) {
	vpa, hasVPA := c.VPA.Target.Mem()
	peak, fromPeak := c.OOMWorkingSet()

	now, hasNow := c.Requests.Mem()

	// Start from the VPA target (or current if no VPA).
	var rec int64
	why := "VPA target"
	if hasVPA {
		rec = vpa
	} else if hasNow {
		rec = now
		why = "current request (no VPA memory target)"
	}

	// SAFETY INVARIANT — memory is non-compressible, so never recommend below the
	// observed peak, and never cut without peak evidence.
	if peak == nil {
		if hasNow && rec < now {
			n := now
			return &n, "HOLD — no peak memory data; not cutting below current (run with --prometheus)"
		}
	} else {
		floor := int64(float64(*peak) * MemoryMargin)
		if floor > rec {
			rec = floor
			if fromPeak {
				why = fmt.Sprintf("floored at observed peak working set +%d%% (OOM-safe)", int((MemoryMargin-1)*100))
			} else {
				why = "floored at current working set (OOM-safe; no peak data)"
			}
		}
	}

	// If a memory HPA governs it, also keep the HPA stable at peak.
	if m != nil {
		if now, ok := c.Requests.Mem(); ok {
			if util, _ := m.UsageUtil(); util != nil && m.TargetUtilization != nil && *m.TargetUtilization > 0 {
				stable := int64(float64(now) * float64(*util) / float64(*m.TargetUtilization))
				if stable > rec {
					rec = stable
					why = fmt.Sprintf("holds the memory HPA at %d%% under peak load", *m.TargetUtilization)
				}
			}
		}
	}

	if rec == 0 {
		return nil, ""
	}
	return &rec, why
}

// coupledMetric returns the first utilization-coupled HPA metric for a resource.
func coupledMetric(hpa model.HPAInfo, res model.ResourceName) *model.HPAMetric {
	if !hpa.Present {
		return nil
	}
	for i := range hpa.Metrics {
		m := &hpa.Metrics[i]
		if m.IsUtilizationCoupled() && m.ResourceName == res {
			return m
		}
	}
	return nil
}

// ceilingHeadroom returns a suggested higher maxReplicas when the workload hits
// its ceiling — i.e. it genuinely needs more replicas than allowed. Zero when no
// bump is warranted.
func ceilingHeadroom(a model.WorkloadAnalysis) int32 {
	if a.Verdict != model.VerdictHitsCeiling {
		return 0
	}
	// Suggest ~50% more ceiling as a starting point for the operator to size.
	bumped := int32(math.Ceil(float64(a.HPA.MaxReplicas) * 1.5))
	if bumped <= a.HPA.MaxReplicas {
		bumped = a.HPA.MaxReplicas + 1
	}
	return bumped
}

// contrast renders the proof-of-value line: what applying the raw VPA target
// would have done, per the engine's verdict.
func contrast(a model.WorkloadAnalysis) string {
	switch a.Verdict {
	case model.VerdictHitsCeiling:
		return fmt.Sprintf("VPA target alone → HPA pegs the ceiling (%d→%d replicas, %s). truce keeps it stable.",
			a.CurrentReplicas, a.PredictedReplicas, signed(a.FootprintDelta))
	case model.VerdictScaleOut:
		return fmt.Sprintf("VPA target alone → HPA scales %d→%d replicas. truce sizes CPU to avoid it.",
			a.CurrentReplicas, a.PredictedReplicas)
	case model.VerdictOOMRisk:
		return "VPA target alone → memory below real peak → OOM-kill. truce floors memory above peak."
	default:
		return ""
	}
}

func signed(d model.Delta) string {
	cpu := d.CPUMilli
	sign := "+"
	if cpu < 0 {
		sign, cpu = "-", -cpu
	}
	return fmt.Sprintf("%s%dm cpu", sign, cpu)
}
