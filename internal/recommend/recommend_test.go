package recommend

import (
	"testing"

	"github.com/heinanca/truce/internal/model"
)

func mi(v int64) *int64 { return model.Int64(v) }
func pi(v int32) *int32 { return &v }

const (
	gib5     = int64(5 * 1024 * 1024 * 1024) // 5Gi current request
	mib807   = int64(807 * 1024 * 1024)      // 846200832, observed mem_max
	memRec   = int64(1057751040)             // ceil(mib807 * 1.25) ≈ 1Gi
	gib1     = int64(1024 * 1024 * 1024)     // VPA mem target
	memDelta = memRec*3 - gib5*3             // -12932689920
)

func hasFlag(flags []model.Flag, want model.Flag) bool {
	for _, f := range flags {
		if f == want {
			return true
		}
	}
	return false
}

// organization is the real-FDA validation fixture and the hard gate: a 3-replica
// CPU-utilization-HPA workload over-provisioned at 3000m / 5Gi, whose measured
// usage is tiny but spiky (p95 53m, max 416m, mem max 807MiB). The VPA's 78m
// target would drive the HPA 3→7; truce's peak-sized 500m must hold it at 3.
func organization() model.WorkloadAnalysis {
	cpu := model.HPAMetric{
		SourceType: model.MetricResource, TargetType: model.TargetUtilization,
		ResourceName: model.ResourceCPU, Identifier: "cpu", TargetUtilization: pi(55),
	}
	return model.WorkloadAnalysis{
		Workload: model.Workload{Kind: model.KindDeployment, Name: "organization", Replicas: 3},
		HPA:      model.HPAInfo{Present: true, MinReplicas: 3, MaxReplicas: 10, Metrics: []model.HPAMetric{cpu}},
		Containers: []model.ContainerAnalysis{{
			Name:     "organization",
			Requests: model.Resources{CPUMilli: mi(3000), MemBytes: mi(gib5)},
			HasVPA:   true,
			VPA:      model.VPARec{Target: model.Resources{CPUMilli: mi(78), MemBytes: mi(gib1)}},
			Spread: model.Spread{
				CPUP50: mi(30), CPUP95: mi(53), CPUMax: mi(416),
				MemP95: mi(700 * 1024 * 1024), MemMax: mi(mib807),
			},
		}},
		Verdict:           model.VerdictScaleOut,
		CurrentReplicas:   3,
		PredictedReplicas: 7, // what the VPA target alone would have driven
	}
}

// TestRecommend_OrganizationGate: the fix is not done until this passes.
func TestRecommend_OrganizationGate(t *testing.T) {
	r := ForWith(organization(), DefaultConfig())

	if len(r.Containers) != 1 {
		t.Fatalf("want 1 container, got %d", len(r.Containers))
	}
	c := r.Containers[0]

	// CPU: ceil(cpu_max 416 × 1.2) = 500m — truce's own number, NOT the VPA's 78m.
	if c.CPURec == nil || *c.CPURec != 500 {
		t.Errorf("CPURec = %v, want 500 (ceil(416×1.2), not VPA 78m)", deref(c.CPURec))
	}
	// Memory: ceil(mem_max 807MiB × 1.25) ≈ 1Gi.
	if c.MemRec == nil || *c.MemRec != memRec {
		t.Errorf("MemRec = %v, want %d (ceil(807MiB×1.25))", deref(c.MemRec), memRec)
	}
	// HPA re-prediction: holds at 3 (53/500 = 11% util, below 55% target).
	if r.PredictedReplicas != 3 {
		t.Errorf("PredictedReplicas = %d, want 3 (no scale-out)", r.PredictedReplicas)
	}
	if r.HPAStillScales {
		t.Error("HPAStillScales = true, want false (peak-sized request must not scale out)")
	}
	// Footprint delta: cpu -7.5 cores, mem -12Gi.
	if r.FootprintDelta.CPUMilli != -7500 {
		t.Errorf("Δ cpu = %d milli, want -7500 (-7.5 cores)", r.FootprintDelta.CPUMilli)
	}
	if r.FootprintDelta.MemBytes != memDelta {
		t.Errorf("Δ mem = %d, want %d (≈ -12Gi)", r.FootprintDelta.MemBytes, memDelta)
	}
	// Spread-driven confidence: spiky (416/53 ≈ 7.8) and diverges from the VPA.
	if !hasFlag(c.Flags, model.FlagSpiky) {
		t.Errorf("expected SPIKY flag (spikiness %.2f), got %v", c.Spikiness, c.Flags)
	}
	if !hasFlag(c.Flags, model.FlagVPADiverges) {
		t.Errorf("expected VPA-DIVERGES flag (500m vs 78m), got %v", c.Flags)
	}
	// Regression guard: must NOT reproduce the old VPA-based 78m → 3→7 behavior.
	if c.CPURec != nil && *c.CPURec < 100 {
		t.Errorf("CPURec %dm is the VPA-style under-size that drove 3→7 — fix regressed", *c.CPURec)
	}
}

// TestRecommend_BurstableUsesP95: with no HPA on CPU, the request is sized to
// cpu_p95 (not cpu_max) since there is no autoscaler to keep calm.
func TestRecommend_BurstableUsesP95(t *testing.T) {
	a := model.WorkloadAnalysis{
		Workload: model.Workload{Kind: model.KindDeployment, Name: "batch", Replicas: 1},
		HPA:      model.HPAInfo{Present: false},
		Containers: []model.ContainerAnalysis{{
			Name:     "batch",
			Requests: model.Resources{CPUMilli: mi(1000)},
			HasVPA:   true,
			VPA:      model.VPARec{Target: model.Resources{CPUMilli: mi(200)}},
			Spread:   model.Spread{CPUP50: mi(80), CPUP95: mi(100), CPUMax: mi(900)},
		}},
		Verdict: model.VerdictNoHPA, CurrentReplicas: 1, PredictedReplicas: 1,
	}
	c := ForWith(a, DefaultConfig()).Containers[0]
	// ceil(cpu_p95 100 × 1.2) = 120m, NOT ceil(cpu_max 900 × 1.2)=1080m.
	if c.CPURec == nil || *c.CPURec != 120 {
		t.Errorf("CPURec = %v, want 120 (ceil(p95 100×1.2))", deref(c.CPURec))
	}
}

// TestRecommend_HoldsWithoutSpread: no Prometheus data → HOLD at current, never
// a blind cut.
func TestRecommend_HoldsWithoutSpread(t *testing.T) {
	a := model.WorkloadAnalysis{
		Workload: model.Workload{Kind: model.KindDeployment, Name: "x", Replicas: 1},
		HPA:      model.HPAInfo{Present: false},
		Containers: []model.ContainerAnalysis{{
			Name:     "x",
			Requests: model.Resources{CPUMilli: mi(1000), MemBytes: mi(gib1)},
			HasVPA:   true,
			VPA:      model.VPARec{Target: model.Resources{CPUMilli: mi(25), MemBytes: mi(512 << 20)}},
			// no Spread
		}},
		Verdict: model.VerdictNoHPA, CurrentReplicas: 1, PredictedReplicas: 1,
	}
	c := ForWith(a, DefaultConfig()).Containers[0]
	if c.CPURec == nil || *c.CPURec != 1000 {
		t.Errorf("CPURec = %v, want 1000 (HOLD — no spread)", deref(c.CPURec))
	}
	if c.MemRec == nil || *c.MemRec != gib1 {
		t.Errorf("MemRec = %v, want %d (HOLD — no spread)", deref(c.MemRec), gib1)
	}
}

// TestRecommend_OOMGuardClamp: a sub-1.0 memory headroom would size below the
// observed peak — the guard must clamp up to peak×1.1 and flag it.
func TestRecommend_OOMGuardClamp(t *testing.T) {
	a := model.WorkloadAnalysis{
		Workload: model.Workload{Kind: model.KindDeployment, Name: "m", Replicas: 1},
		HPA:      model.HPAInfo{Present: false},
		Containers: []model.ContainerAnalysis{{
			Name:     "m",
			Requests: model.Resources{MemBytes: mi(gib1)},
			HasVPA:   true,
			VPA:      model.VPARec{Target: model.Resources{MemBytes: mi(gib1)}},
			Spread:   model.Spread{MemMax: mi(mib807)},
		}},
		Verdict: model.VerdictNoHPA, CurrentReplicas: 1, PredictedReplicas: 1,
	}
	cfg := DefaultConfig()
	cfg.MemHeadroom = 0.5 // would propose below peak
	c := ForWith(a, cfg).Containers[0]
	peak := mib807
	wantClamp := int64(float64(peak) * 1.1)
	// ceil(mib807*1.1): compute deterministically.
	if c.MemRec == nil || *c.MemRec < mib807 {
		t.Errorf("MemRec = %v, must be >= mem_max %d (OOM guard)", deref(c.MemRec), mib807)
	}
	if *c.MemRec < wantClamp {
		t.Errorf("MemRec = %d, want clamp up to ~peak×1.1 (%d)", *c.MemRec, wantClamp)
	}
	if !hasFlag(c.Flags, model.FlagOOMGuardClamped) {
		t.Errorf("expected OOM-GUARD-CLAMPED flag, got %v", c.Flags)
	}
}

// TestRecommend_HPAStillScales: when even the peak-sized request can't hold the
// HPA (target far below baseline utilization), flag HPA-STILL-SCALES and suggest
// a higher ceiling.
func TestRecommend_HPAStillScales(t *testing.T) {
	cpu := model.HPAMetric{
		SourceType: model.MetricResource, TargetType: model.TargetUtilization,
		ResourceName: model.ResourceCPU, Identifier: "cpu", TargetUtilization: pi(5), // absurdly low target
	}
	a := model.WorkloadAnalysis{
		Workload: model.Workload{Kind: model.KindDeployment, Name: "hot", Replicas: 2},
		HPA:      model.HPAInfo{Present: true, MinReplicas: 1, MaxReplicas: 4, Metrics: []model.HPAMetric{cpu}},
		Containers: []model.ContainerAnalysis{{
			Name:     "hot",
			Requests: model.Resources{CPUMilli: mi(1000)},
			HasVPA:   true,
			VPA:      model.VPARec{Target: model.Resources{CPUMilli: mi(500)}},
			Spread:   model.Spread{CPUP50: mi(100), CPUP95: mi(200), CPUMax: mi(300)},
		}},
		Verdict: model.VerdictHitsCeiling, CurrentReplicas: 2, PredictedReplicas: 4,
	}
	r := ForWith(a, DefaultConfig())
	// baseline p95 200 / new req ceil(300×1.2)=360 → 55% util / 5% target = 11x → clamps to max 4.
	if !r.HPAStillScales {
		t.Error("expected HPAStillScales = true")
	}
	if !r.HasFlag(model.FlagHPAStillScales) {
		t.Errorf("expected HPA-STILL-SCALES flag, got %v", r.Flags)
	}
	if r.RaiseMaxTo <= 4 {
		t.Errorf("RaiseMaxTo = %d, want > 4", r.RaiseMaxTo)
	}
}

func deref(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}
