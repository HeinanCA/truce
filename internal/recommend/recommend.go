// Package recommend turns a WorkloadAnalysis into concrete, actionable request
// values — the synthesis that neither VPA nor HPA produces alone. truce sizes
// from the MEASURED usage spread (Prometheus), not by parroting the VPA target:
//
//   - CPU request is sized to cpu_max (for a utilization-HPA workload, so the
//     autoscaler stays calm at baseline) or cpu_p95 (Burstable, no HPA to trip),
//     plus headroom. The VPA target — which on a utilization HPA is often the
//     p90 average and therefore too low — would force perpetual scale-out.
//   - Memory request is sized to mem_max + headroom, and an OOM guard clamps it
//     up if it ever lands below the observed peak. Memory is non-squeezable, so a
//     request below peak is an OOM, full stop.
//   - The HPA is RE-PREDICTED with the new request (baseline cpu_p95/p50 against
//     the new request) to prove it holds replicas at current/min. If it still
//     scales out, that is flagged.
//   - The VPA target is kept only as a cross-check; a >50% divergence is flagged.
//
// It is pure: it reads a model.WorkloadAnalysis (carrying the Prometheus spread
// when Prometheus was queried) and returns numbers. No cluster or file access.
package recommend

import (
	"fmt"
	"math"
	"strings"

	"github.com/heinanca/truce/internal/engine"
	"github.com/heinanca/truce/internal/model"
)

// SpikyThreshold flags SPIKY when cpu_max/cpu_p95 exceeds it: rare large bursts
// that a p95-sized request would miss.
const SpikyThreshold = 5.0

// VPADivergeFraction flags VPA-DIVERGES when |truce_rec - vpa_rec|/truce_rec
// exceeds it.
const VPADivergeFraction = 0.5

// Config tunes the sizing. DefaultConfig mirrors the CLI flag defaults.
type Config struct {
	Window      string  // informational, e.g. "7d"
	CPUHeadroom float64 // multiplier over the CPU basis (default 1.2)
	MemHeadroom float64 // multiplier over mem_max (default 1.25)
	Baseline    string  // re-prediction baseline: "p95" (default) or "p50"
	SetCPULimit bool    // when true, set a CPU limit = ceil(cpu_max * 1.5)
	Tolerance   float64 // HPA tolerance used in the re-prediction (default 0.10)
}

// DefaultConfig returns the standard sizing knobs.
func DefaultConfig() Config {
	return Config{
		Window:      "7d",
		CPUHeadroom: 1.2,
		MemHeadroom: 1.25,
		Baseline:    "p95",
		Tolerance:   0.10,
	}
}

func (c Config) cpuHeadroom() float64 {
	if c.CPUHeadroom > 0 {
		return c.CPUHeadroom
	}
	return 1.2
}

func (c Config) memHeadroom() float64 {
	if c.MemHeadroom > 0 {
		return c.MemHeadroom
	}
	return 1.25
}

func (c Config) tolerance() float64 {
	if c.Tolerance > 0 {
		return c.Tolerance
	}
	return 0.10
}

// ContainerRec is the recommendation for one container.
type ContainerRec struct {
	Name string

	CPUNow   *int64 // current request (milli), nil if unset
	CPURec   *int64 // recommended request (milli)
	CPULimit *int64 // recommended CPU limit (milli), nil unless --set-cpu-limit
	CPUWhy   string

	MemNow   *int64 // current request (bytes), nil if unset
	MemRec   *int64 // recommended request (bytes)
	MemLimit *int64 // recommended memory limit (bytes)
	MemWhy   string

	// Measured spread surfaced in output (milli for CPU, bytes for memory).
	CPUP50    *int64
	CPUP95    *int64
	CPUMax    *int64
	MemMax    *int64
	Spikiness float64 // cpu_max/cpu_p95, 0 when unknown

	// VPA cross-check values (the VPA's own target, for divergence comparison).
	VPACPU *int64
	VPAMem *int64

	// Flags are per-container advisories: SPIKY, OOM-GUARD-CLAMPED, VPA-DIVERGES.
	Flags []model.Flag
}

// HasFlag reports whether the container recommendation carries a flag.
func (c ContainerRec) HasFlag(f model.Flag) bool {
	for _, g := range c.Flags {
		if g == f {
			return true
		}
	}
	return false
}

// Recommendation is the full per-service result.
type Recommendation struct {
	Service    string
	Containers []ContainerRec

	// RaiseMaxTo is non-zero when even the peak-sized request leaves the HPA at
	// its ceiling — the operator needs more maxReplicas.
	RaiseMaxTo int32

	// CurrentReplicas / PredictedReplicas come from re-predicting the HPA with the
	// NEW request. Peak-sizing should hold these equal.
	CurrentReplicas   int32
	PredictedReplicas int32
	// HPAStillScales is true when the re-prediction still trips scale-out.
	HPAStillScales bool

	// FootprintDelta = PredictedReplicas*newRequest - CurrentReplicas*oldRequest.
	FootprintDelta model.Delta

	// HasPeak is true when at least one container had Prometheus spread data; when
	// false the recommendation HOLDs at current (never a blind cut).
	HasPeak bool

	// Flags are workload-level advisories (HPA-STILL-SCALES).
	Flags []model.Flag

	// Contrast is the one-line "VPA alone would do X" proof-of-value note.
	Contrast string
}

// HasFlag reports whether the recommendation carries a workload-level flag.
func (r Recommendation) HasFlag(f model.Flag) bool {
	for _, g := range r.Flags {
		if g == f {
			return true
		}
	}
	return false
}

// For builds the recommendation with the default config.
func For(a model.WorkloadAnalysis) Recommendation { return ForWith(a, DefaultConfig()) }

// ForWith builds the recommendation from an analyzed workload and config.
func ForWith(a model.WorkloadAnalysis, cfg Config) Recommendation {
	r := Recommendation{
		Service:           a.Workload.Name,
		CurrentReplicas:   a.CurrentReplicas,
		PredictedReplicas: a.CurrentReplicas,
	}

	cpuMetric := coupledMetric(a.HPA, model.ResourceCPU)
	hasCPUHPA := cpuMetric != nil

	for _, c := range a.Containers {
		rec := ContainerRec{
			Name:   c.Name,
			CPUNow: c.Requests.CPUMilli,
			MemNow: c.Requests.MemBytes,
			CPUP50: c.Spread.CPUP50,
			CPUP95: c.Spread.CPUP95,
			CPUMax: c.Spread.CPUMax,
			MemMax: c.Spread.MemMax,
			VPACPU: c.VPA.Target.CPUMilli,
			VPAMem: c.VPA.Target.MemBytes,
		}
		sizeCPU(&rec, c, cfg, hasCPUHPA, a.HPA.ManagedByKEDA)
		sizeMem(&rec, c, cfg)
		spikinessFlag(&rec)
		vpaDivergeFlag(&rec)
		if rec.CPUMax != nil || rec.MemMax != nil {
			r.HasPeak = true
		}
		r.Containers = append(r.Containers, rec)
	}

	repredict(&r, a, cpuMetric, cfg)
	r.FootprintDelta = footprintFromRec(a, r)
	r.RaiseMaxTo = ceilingHeadroom(a, r)
	r.Contrast = contrast(a, r)
	return r
}

// sizeCPU sets the CPU request: cpu_max+headroom when a utilization HPA governs
// CPU (so the autoscaler stays calm at baseline), else cpu_p95+headroom. With no
// spread data it HOLDs at the current request — never a blind cut.
func sizeCPU(rec *ContainerRec, c model.ContainerAnalysis, cfg Config, hasCPUHPA, keda bool) {
	var basis *int64
	if hasCPUHPA {
		basis = c.Spread.CPUMax
	} else {
		basis = c.Spread.CPUP95
	}
	if basis == nil {
		if rec.CPUNow != nil {
			n := *rec.CPUNow
			rec.CPURec = &n
			rec.CPUWhy = "HOLD — no CPU usage data (run with --prometheus)"
		}
		return
	}

	hr := cfg.cpuHeadroom()
	v := int64(math.Ceil(float64(*basis) * hr))
	if v < 1 {
		v = 1
	}
	rec.CPURec = &v
	switch {
	case hasCPUHPA:
		rec.CPUWhy = fmt.Sprintf("ceil(cpu_max × %.2g) — peak-sized so the HPA stays calm at baseline", hr)
	case keda:
		rec.CPUWhy = fmt.Sprintf("ceil(cpu_p95 × %.2g) — KEDA scales externally; request doesn't move replicas", hr)
	default:
		rec.CPUWhy = fmt.Sprintf("ceil(cpu_p95 × %.2g) — Burstable, no HPA to trip", hr)
	}

	if cfg.SetCPULimit && c.Spread.CPUMax != nil {
		lim := int64(math.Ceil(float64(*c.Spread.CPUMax) * 1.5))
		rec.CPULimit = &lim
	}
}

// sizeMem sets the memory request to mem_max+headroom, clamping up to peak×1.1
// if the proposal somehow lands below the observed peak (OOM guard). With no
// spread data it HOLDs at the current request.
func sizeMem(rec *ContainerRec, c model.ContainerAnalysis, cfg Config) {
	mx := c.Spread.MemMax
	if mx == nil {
		if rec.MemNow != nil {
			n := *rec.MemNow
			rec.MemRec = &n
			rec.MemWhy = "HOLD — no memory usage data (run with --prometheus)"
		}
		return
	}

	hr := cfg.memHeadroom()
	v := int64(math.Ceil(float64(*mx) * hr))
	if v < *mx {
		// OOM guard: never below the observed peak.
		v = int64(math.Ceil(float64(*mx) * 1.1))
		rec.Flags = append(rec.Flags, model.FlagOOMGuardClamped)
		rec.MemWhy = "OOM-guard: clamped up to peak×1.1 (proposal was below observed peak)"
	} else {
		rec.MemWhy = fmt.Sprintf("ceil(mem_max × %.2g) — non-squeezable, sized to peak+headroom", hr)
	}
	rec.MemRec = &v

	lim := int64(math.Ceil(float64(*mx) * 1.5))
	rec.MemLimit = &lim
}

// spikinessFlag computes cpu_max/cpu_p95 and flags SPIKY past the threshold.
func spikinessFlag(rec *ContainerRec) {
	if rec.CPUP95 == nil || rec.CPUMax == nil || *rec.CPUP95 <= 0 {
		return
	}
	rec.Spikiness = float64(*rec.CPUMax) / float64(*rec.CPUP95)
	if rec.Spikiness > SpikyThreshold {
		rec.Flags = append(rec.Flags, model.FlagSpiky)
	}
}

// vpaDivergeFlag flags VPA-DIVERGES when the CPU recommendation and VPA target
// differ by more than VPADivergeFraction.
func vpaDivergeFlag(rec *ContainerRec) {
	if rec.CPURec == nil || rec.VPACPU == nil || *rec.CPURec <= 0 {
		return
	}
	if math.Abs(float64(*rec.CPURec-*rec.VPACPU))/float64(*rec.CPURec) > VPADivergeFraction {
		rec.Flags = append(rec.Flags, model.FlagVPADiverges)
	}
}

// repredict re-runs the HPA's replica math against the NEW CPU request, using
// the configured baseline (cpu_p95 or cpu_p50) summed over the metric's
// considered containers. Sets PredictedReplicas, HPAStillScales, and the
// workload-level HPA-STILL-SCALES flag.
func repredict(r *Recommendation, a model.WorkloadAnalysis, cpuMetric *model.HPAMetric, cfg Config) {
	if cpuMetric == nil || cpuMetric.TargetUtilization == nil {
		return
	}
	considered := consideredContainers(cpuMetric, a.Containers)
	if len(considered) == 0 {
		return
	}

	var newReqSum, baseSum int64
	for _, c := range considered {
		cr := r.containerRec(c.Name)
		if cr == nil || cr.CPURec == nil {
			return // incomplete basis — leave PredictedReplicas at current
		}
		b := baselineFor(c, cfg.Baseline)
		if b == nil {
			return
		}
		newReqSum += *cr.CPURec
		baseSum += *b
	}
	if newReqSum <= 0 {
		return
	}

	repl, _, scales := engine.RepredictCPU(
		*cpuMetric.TargetUtilization, baseSum, newReqSum,
		a.CurrentReplicas, a.HPA.MinReplicas, a.HPA.MaxReplicas, cfg.tolerance(),
	)
	r.PredictedReplicas = repl
	r.HPAStillScales = scales
	if scales {
		r.Flags = append(r.Flags, model.FlagHPAStillScales)
	}
}

// baselineFor returns the re-prediction baseline (milli) for a container.
func baselineFor(c model.ContainerAnalysis, baseline string) *int64 {
	if baseline == "p50" && c.Spread.CPUP50 != nil {
		return c.Spread.CPUP50
	}
	return c.Spread.CPUP95
}

// containerRec finds the recommendation for a container by name.
func (r *Recommendation) containerRec(name string) *ContainerRec {
	for i := range r.Containers {
		if r.Containers[i].Name == name {
			return &r.Containers[i]
		}
	}
	return nil
}

// footprintFromRec computes the footprint delta from the NEW request and the
// re-predicted replica count:
//
//	PredictedReplicas*newRequest - CurrentReplicas*oldRequest, per resource.
func footprintFromRec(a model.WorkloadAnalysis, r Recommendation) model.Delta {
	var oldCPU, oldMem int64
	for _, c := range a.Containers {
		if v, ok := c.Requests.CPU(); ok {
			oldCPU += v
		}
		if v, ok := c.Requests.Mem(); ok {
			oldMem += v
		}
	}
	var newCPU, newMem int64
	for _, cr := range r.Containers {
		switch {
		case cr.CPURec != nil:
			newCPU += *cr.CPURec
		case cr.CPUNow != nil:
			newCPU += *cr.CPUNow
		}
		switch {
		case cr.MemRec != nil:
			newMem += *cr.MemRec
		case cr.MemNow != nil:
			newMem += *cr.MemNow
		}
	}
	return model.Delta{
		CPUMilli: newCPU*int64(r.PredictedReplicas) - oldCPU*int64(a.CurrentReplicas),
		MemBytes: newMem*int64(r.PredictedReplicas) - oldMem*int64(a.CurrentReplicas),
	}
}

// consideredContainers returns the containers a metric's request sums over: the
// single named container for ContainerResource, else all containers.
func consideredContainers(m *model.HPAMetric, containers []model.ContainerAnalysis) []model.ContainerAnalysis {
	if m.SourceType == model.MetricContainerResource {
		for _, c := range containers {
			if c.Name == m.ContainerName {
				return []model.ContainerAnalysis{c}
			}
		}
		return nil
	}
	return containers
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

// ceilingHeadroom suggests a higher maxReplicas when the re-predicted HPA still
// pins at its ceiling even with the peak-sized request. Zero when no bump is
// warranted.
func ceilingHeadroom(a model.WorkloadAnalysis, r Recommendation) int32 {
	if !r.HPAStillScales || a.HPA.MaxReplicas <= 0 || r.PredictedReplicas != a.HPA.MaxReplicas {
		return 0
	}
	bumped := int32(math.Ceil(float64(a.HPA.MaxReplicas) * 1.5))
	if bumped <= a.HPA.MaxReplicas {
		bumped = a.HPA.MaxReplicas + 1
	}
	return bumped
}

// contrast renders the proof-of-value line: what the VPA target alone would have
// driven the HPA to do (from the engine's VPA-based prediction) versus truce's
// peak-sized request holding replicas steady.
func contrast(a model.WorkloadAnalysis, r Recommendation) string {
	if a.HPA.ManagedByKEDA {
		return kedaNote(a.HPA)
	}
	if a.PredictedReplicas > a.CurrentReplicas {
		return fmt.Sprintf("VPA target alone → HPA predicts %d→%d replicas (scale-out); truce's peak-sized request holds at %d→%d.",
			a.CurrentReplicas, a.PredictedReplicas, r.CurrentReplicas, r.PredictedReplicas)
	}
	if a.Verdict == model.VerdictOOMRisk {
		return "VPA target alone → memory below real peak → OOM-kill. truce sizes memory above peak."
	}
	return ""
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
