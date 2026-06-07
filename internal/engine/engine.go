// Package engine holds the verdict logic: pure functions that turn a collected
// workload into a WorkloadAnalysis. It imports no Kubernetes packages and makes
// no cluster calls — every input arrives via model types and Options, so the
// math is fully deterministic and unit-testable. The HPA prediction implemented
// here is truce's differentiator.
package engine

import (
	"time"

	"github.com/heinanca/truce/internal/model"
)

// Defaults for the confidence heuristics. Exposed as constants so tests and the
// CLI reference the same thresholds.
const (
	// DefaultTolerance is the fallback HPA tolerance when neither the HPA's
	// spec.behavior nor the --tolerance flag overrides it.
	DefaultTolerance = 0.10
	// DefaultLowConfAge flags a VPA recommendation as LOW-CONF when the VPA is
	// younger than this (not enough history).
	DefaultLowConfAge = 48 * time.Hour
	// DefaultWideBoundFraction flags LOW-CONF when (target-lowerBound)/target
	// exceeds this on any dimension — a wide band means an unsettled estimate.
	DefaultWideBoundFraction = 0.30
)

// Options carries the tunables and external facts the engine needs. None of
// these require a cluster call at engine time: the caller resolves them first.
type Options struct {
	// DefaultTolerance is the fallback applied when an HPA has no per-direction
	// behavior tolerance. Zero means use DefaultTolerance.
	DefaultTolerance float64
	// InPlaceAvailable reports whether in-place pod resize can be relied upon.
	// When false, applying a recommendation restarts the pod (RESTART flag).
	InPlaceAvailable bool
	// Now is the reference time for the VPA-age LOW-CONF check. Zero disables the
	// age check (e.g. when VPA creation time is unknown).
	Now time.Time
	// LowConfAge / WideBoundFraction override the defaults when non-zero.
	LowConfAge        time.Duration
	WideBoundFraction float64
}

func (o Options) tolerance() float64 {
	if o.DefaultTolerance > 0 {
		return o.DefaultTolerance
	}
	return DefaultTolerance
}

// Analyze produces the full verdict for one collected workload. It is a pure
// function: same inputs always yield the same output.
//
// Verdict precedence (highest first):
//  1. OOM RISK   — a VPA memory target below current usage; dangerous to apply
//     regardless of HPA behavior, so it dominates the headline.
//  2. NO HPA     — a VPA but no HPA; plain rightsizing, apply freely.
//  3. HPA-coupled prediction — SAFE / SCALE-OUT / HITS CEILING / SCALE-IN from
//     the binding metric, or DECOUPLED when no metric depends on requests.
//
// Advisory flags (LOW-CONF, UNRELIABLE, RESTART) layer on independently. The
// footprint delta is always computed so even SAFE/DECOUPLED rows show the
// rightsizing effect at fixed replicas.
func Analyze(cw model.CollectedWorkload, opts Options) model.WorkloadAnalysis {
	a := model.WorkloadAnalysis{
		Workload:          cw.Workload,
		HPA:               cw.HPA,
		Containers:        cw.Containers,
		CurrentReplicas:   cw.Workload.Replicas,
		PredictedReplicas: cw.Workload.Replicas,
		ToleranceUp:       opts.tolerance(),
		ToleranceDown:     opts.tolerance(),
	}

	// Nothing to advise without a recommendation.
	if !cw.HasAnyVPARec() {
		a.Actionable = false
		a.FootprintDelta = footprintDelta(cw.Containers, a.CurrentReplicas, a.PredictedReplicas)
		return a
	}
	a.Actionable = true
	a.UsageBasis = usageBasis(cw)

	flags := newFlagSet()

	// --- Cross-cutting flags independent of the verdict branch. ---
	if cw.HPA.ManagedByKEDA {
		flags.add(model.FlagKEDA)
	}
	if !opts.InPlaceAvailable {
		flags.add(model.FlagRestart)
	}
	// Recommendation confidence no longer comes from VPA age/bound width — it
	// comes from the measured usage spread (SPIKY), raised by the recommender.

	// --- Verdict. ---
	switch {
	case oomRisk(cw.Containers):
		a.Verdict = model.VerdictOOMRisk
		// Replicas left at N: prediction is not the headline for an OOM row.

	case !cw.HPA.Present:
		a.Verdict = model.VerdictNoHPA

	default:
		res := predictWorkload(cw, opts.tolerance(), flags)
		a.Verdict = res.verdict
		a.PredictedReplicas = res.predictedReplicas
		a.BindingMetric = res.binding
		a.PredictedUtilization = res.predictedUtil
		a.ToleranceUp = res.tolUp
		a.ToleranceDown = res.tolDown
	}

	a.Flags = flags.slice()
	a.FootprintDelta = footprintDelta(cw.Containers, a.CurrentReplicas, a.PredictedReplicas)
	return a
}

// oomRisk reports whether any container's VPA memory target is below its working
// set — applying it would likely OOM-kill the pod. It uses the peak working set
// when available (memory is non-compressible, so the worst moment is what
// matters), else the instantaneous snapshot.
func oomRisk(containers []model.ContainerAnalysis) bool {
	for _, c := range containers {
		if !c.HasVPA {
			continue
		}
		target, ok := c.VPA.Target.Mem()
		ws, _ := c.OOMWorkingSet()
		if !ok || ws == nil {
			continue
		}
		if target < *ws {
			return true
		}
	}
	return false
}

// usageBasis reports whether any peak time-series input is present; if so the
// verdict reflects peak behavior, otherwise it is snapshot-only.
func usageBasis(cw model.CollectedWorkload) model.UsageBasis {
	for _, m := range cw.HPA.Metrics {
		if m.PeakUtilization != nil {
			return model.BasisPeak
		}
	}
	for _, c := range cw.Containers {
		if c.PeakMemWorkingSet != nil {
			return model.BasisPeak
		}
	}
	return model.BasisSnapshot
}

// flagSet is a tiny ordered, de-duplicated flag collector.
type flagSet struct {
	seen  map[model.Flag]bool
	order []model.Flag
}

func newFlagSet() *flagSet { return &flagSet{seen: map[model.Flag]bool{}} }

func (f *flagSet) add(flag model.Flag) {
	if !f.seen[flag] {
		f.seen[flag] = true
		f.order = append(f.order, flag)
	}
}

func (f *flagSet) slice() []model.Flag {
	if len(f.order) == 0 {
		return nil
	}
	return f.order
}
