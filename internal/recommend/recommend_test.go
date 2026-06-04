package recommend

import (
	"testing"

	"github.com/heinanca/truce/internal/model"
)

func mi(v int64) *int64 { return model.Int64(v) }
func pi(v int32) *int32 { return &v }

const (
	gib16 = int64(1717986918) // ~1.6Gi  (VPA mem target)
	gib15 = int64(1610612736) // 1.5Gi   (observed peak working set)
)

// mlManagement reproduces the real-cluster row: 1 replica, CPU request 1000m at
// ~18% peak utilization against a 50% target, VPA target 35m (the trap that
// drives 1→10), memory peak 1.5Gi above the VPA's 1.6Gi target, LOW-CONF, and a
// HITS CEILING verdict at max=10.
func mlManagement() model.WorkloadAnalysis {
	cpuMetric := model.HPAMetric{
		SourceType: model.MetricResource, TargetType: model.TargetUtilization,
		ResourceName: model.ResourceCPU, Identifier: "cpu",
		TargetUtilization: pi(50), CurrentUtilization: pi(18), PeakUtilization: pi(18),
	}
	return model.WorkloadAnalysis{
		Workload: model.Workload{Kind: model.KindDeployment, Name: "ml-management", Replicas: 1},
		HPA:      model.HPAInfo{Present: true, MinReplicas: 1, MaxReplicas: 10, Metrics: []model.HPAMetric{cpuMetric}},
		Containers: []model.ContainerAnalysis{{
			Name:              "ml-management",
			Requests:          model.Resources{CPUMilli: mi(1000), MemBytes: mi(2 * 1024 * 1024 * 1024)},
			HasVPA:            true,
			VPA:               model.VPARec{Target: model.Resources{CPUMilli: mi(35), MemBytes: mi(gib16)}},
			PeakMemWorkingSet: mi(gib15),
		}},
		Verdict:           model.VerdictHitsCeiling,
		Flags:             []model.Flag{model.FlagLowConf, model.FlagGitOps},
		CurrentReplicas:   1,
		PredictedReplicas: 10,
		FootprintDelta:    model.Delta{CPUMilli: -650, MemBytes: 14 * 1024 * 1024 * 1024},
	}
}

func TestRecommend_MLManagement(t *testing.T) {
	r := For(mlManagement())

	if len(r.Containers) != 1 {
		t.Fatalf("want 1 container, got %d", len(r.Containers))
	}
	c := r.Containers[0]

	// CPU: HPA-stable = 1000 * 18 / 50 = 360m — NOT the VPA's 35m.
	if c.CPURec == nil || *c.CPURec != 360 {
		t.Errorf("CPURec = %v, want 360 (HPA-stable, not VPA 35m)", c.CPURec)
	}

	// Memory: floored above the 1.5Gi observed peak (peak*1.15 > VPA 1.6Gi).
	peak := gib15
	wantMemFloor := int64(float64(peak) * MemoryMargin)
	if c.MemRec == nil || *c.MemRec != wantMemFloor {
		t.Errorf("MemRec = %v, want %d (peak-floored, above VPA target)", c.MemRec, wantMemFloor)
	}
	if c.MemRec != nil && *c.MemRec <= gib16 {
		t.Errorf("MemRec %d should exceed VPA target %d (OOM floor)", *c.MemRec, gib16)
	}

	// Ceiling: recommend raising max above 10.
	if r.RaiseMaxTo <= 10 {
		t.Errorf("RaiseMaxTo = %d, want > 10", r.RaiseMaxTo)
	}
	if !r.Provisional {
		t.Error("expected Provisional (LOW-CONF)")
	}
	if r.Contrast == "" {
		t.Error("expected a contrast line vs VPA target")
	}
}

// TestRecommend_NoHPA: without an HPA, CPU should fall back to the VPA target
// (replicas are fixed, so rightsizing is safe).
func TestRecommend_NoHPA(t *testing.T) {
	a := model.WorkloadAnalysis{
		Workload: model.Workload{Kind: model.KindDeployment, Name: "demo", Replicas: 1},
		HPA:      model.HPAInfo{Present: false},
		Containers: []model.ContainerAnalysis{{
			Name:     "demo",
			Requests: model.Resources{CPUMilli: mi(1000)},
			HasVPA:   true,
			VPA:      model.VPARec{Target: model.Resources{CPUMilli: mi(25)}},
		}},
		Verdict:         model.VerdictNoHPA,
		CurrentReplicas: 1,
	}
	r := For(a)
	if r.Containers[0].CPURec == nil || *r.Containers[0].CPURec != 25 {
		t.Errorf("no-HPA CPURec = %v, want 25 (VPA target)", r.Containers[0].CPURec)
	}
}
