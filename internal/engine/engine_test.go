package engine

import (
	"strconv"
	"testing"
	"time"

	"github.com/heinanca/truce/internal/model"
)

// --- fixture builders -------------------------------------------------------

func mi(v int64) *int64     { return model.Int64(v) }
func pi(v int32) *int32     { return &v }
func pf(v float64) *float64 { return &v }

const (
	mib512 = 512 * 1024 * 1024 // 536870912
	mib600 = 600 * 1024 * 1024 // 629145600
	mib400 = 400 * 1024 * 1024 // 419430400
)

// resMetric builds a pod-level Resource(Utilization) metric.
func resMetric(res model.ResourceName, target, current int32) model.HPAMetric {
	return model.HPAMetric{
		SourceType:         model.MetricResource,
		TargetType:         model.TargetUtilization,
		ResourceName:       res,
		Identifier:         string(res),
		TargetUtilization:  pi(target),
		CurrentUtilization: pi(current),
	}
}

// ctrResMetric builds a ContainerResource(Utilization) metric on a container.
func ctrResMetric(res model.ResourceName, container string, target, current int32) model.HPAMetric {
	m := resMetric(res, target, current)
	m.SourceType = model.MetricContainerResource
	m.ContainerName = container
	m.Identifier = string(res) + "@" + container
	return m
}

func hpa(min, max int32, metrics ...model.HPAMetric) model.HPAInfo {
	return model.HPAInfo{Present: true, Name: "h", MinReplicas: min, MaxReplicas: max, Metrics: metrics}
}

// container with CPU request + optional VPA cpu target.
func cpuContainer(name string, reqCPU int64, vpaCPU *int64) model.ContainerAnalysis {
	c := model.ContainerAnalysis{Name: name, Requests: model.Resources{CPUMilli: mi(reqCPU)}}
	if vpaCPU != nil {
		c.HasVPA = true
		c.VPA.Target = model.Resources{CPUMilli: vpaCPU}
	}
	return c
}

// --- the case table ---------------------------------------------------------

type wsCase struct {
	name       string
	cw         model.CollectedWorkload
	opts       Options
	verdict    model.Verdict
	replicas   int32
	predUtil   *int32 // expected; nil = expect no binding util
	flags      []model.Flag
	deltaCPU   int64
	deltaMem   int64
	actionable bool
}

func cases() []wsCase {
	inPlaceOn := Options{InPlaceAvailable: true}

	return []wsCase{
		{
			name: "1_resource_utilization_scale_out",
			cw: model.CollectedWorkload{
				Workload:   model.Workload{Kind: model.KindDeployment, Name: "web", Replicas: 4},
				HPA:        hpa(1, 10, resMetric(model.ResourceCPU, 70, 60)),
				Containers: []model.ContainerAnalysis{cpuContainer("app", 1000, mi(500))},
			},
			opts: inPlaceOn, actionable: true,
			verdict: model.VerdictScaleOut, replicas: 7, predUtil: pi(120),
			deltaCPU: -500, deltaMem: 0,
		},
		{
			name: "2_container_resource",
			cw: model.CollectedWorkload{
				Workload: model.Workload{Kind: model.KindDeployment, Name: "api", Replicas: 4},
				HPA:      hpa(1, 10, ctrResMetric(model.ResourceCPU, "app", 50, 40)),
				Containers: []model.ContainerAnalysis{
					cpuContainer("app", 1000, mi(500)),
					cpuContainer("sidecar", 200, nil), // no VPA: unchanged
				},
			},
			opts: inPlaceOn, actionable: true,
			verdict: model.VerdictScaleOut, replicas: 7, predUtil: pi(80),
			deltaCPU: 100, deltaMem: 0, // +100m: a downsize that backfires into more replicas
		},
		{
			name: "3_resource_sidecar_without_requests",
			cw: model.CollectedWorkload{
				Workload: model.Workload{Kind: model.KindDeployment, Name: "mesh", Replicas: 4},
				HPA:      hpa(1, 10, resMetric(model.ResourceCPU, 70, 60)),
				Containers: []model.ContainerAnalysis{
					cpuContainer("app", 1000, mi(500)),
					{Name: "sidecar"}, // NO request, NO vpa -> unreliable basis
				},
			},
			opts: inPlaceOn, actionable: true,
			verdict: model.VerdictSafe, replicas: 4, predUtil: nil,
			flags:    []model.Flag{model.FlagUnreliable},
			deltaCPU: -2000, deltaMem: 0,
		},
		{
			name: "4a_tolerance_absent_safe",
			cw: model.CollectedWorkload{
				Workload:   model.Workload{Kind: model.KindDeployment, Name: "t", Replicas: 4},
				HPA:        hpa(1, 10, resMetric(model.ResourceCPU, 50, 50)),
				Containers: []model.ContainerAnalysis{cpuContainer("app", 1080, mi(1000))},
			},
			opts: inPlaceOn, actionable: true,
			verdict: model.VerdictSafe, replicas: 4, predUtil: pi(54),
			deltaCPU: -320, deltaMem: 0, // ratio 1.08 within default 0.10
		},
		{
			name: "4b_tolerance_present_scale_out",
			cw: model.CollectedWorkload{
				Workload: model.Workload{Kind: model.KindDeployment, Name: "t", Replicas: 4},
				HPA: func() model.HPAInfo {
					h := hpa(1, 10, resMetric(model.ResourceCPU, 50, 50))
					h.ScaleUpTolerance = pf(0.05) // tighter than default -> 1.08 now exceeds
					return h
				}(),
				Containers: []model.ContainerAnalysis{cpuContainer("app", 1080, mi(1000))},
			},
			opts: inPlaceOn, actionable: true,
			verdict: model.VerdictScaleOut, replicas: 5, predUtil: pi(54),
			deltaCPU: 680, deltaMem: 0,
		},
		{
			name: "5a_inplace_on_no_restart",
			cw: model.CollectedWorkload{
				Workload:   model.Workload{Kind: model.KindDeployment, Name: "s", Replicas: 4},
				HPA:        hpa(1, 10, resMetric(model.ResourceCPU, 80, 70)),
				Containers: []model.ContainerAnalysis{cpuContainer("app", 1000, mi(950))},
			},
			opts: Options{InPlaceAvailable: true}, actionable: true,
			verdict: model.VerdictSafe, replicas: 4, predUtil: pi(74),
			deltaCPU: -200, deltaMem: 0,
		},
		{
			name: "5b_inplace_off_restart",
			cw: model.CollectedWorkload{
				Workload:   model.Workload{Kind: model.KindDeployment, Name: "s", Replicas: 4},
				HPA:        hpa(1, 10, resMetric(model.ResourceCPU, 80, 70)),
				Containers: []model.ContainerAnalysis{cpuContainer("app", 1000, mi(950))},
			},
			opts: Options{InPlaceAvailable: false}, actionable: true,
			verdict: model.VerdictSafe, replicas: 4, predUtil: pi(74),
			flags:    []model.Flag{model.FlagRestart},
			deltaCPU: -200, deltaMem: 0,
		},
		{
			name: "6_vpa_mem_below_usage_oom",
			cw: model.CollectedWorkload{
				Workload: model.Workload{Kind: model.KindDeployment, Name: "cache", Replicas: 4},
				HPA:      hpa(1, 10, resMetric(model.ResourceCPU, 80, 50)),
				Containers: []model.ContainerAnalysis{{
					Name:                 "app",
					Requests:             model.Resources{CPUMilli: mi(1000), MemBytes: mi(mib512)},
					HasVPA:               true,
					VPA:                  model.VPARec{Target: model.Resources{CPUMilli: mi(900), MemBytes: mi(mib400)}},
					CurrentMemWorkingSet: mi(mib600), // 600Mi > 400Mi target -> OOM
				}},
			},
			opts: inPlaceOn, actionable: true,
			verdict: model.VerdictOOMRisk, replicas: 4, predUtil: nil,
			deltaCPU: -400, deltaMem: 4*mib400 - 4*mib512, // -469762048
		},
		{
			// Young VPA no longer raises LOW-CONF: recommendation confidence comes
			// from the measured usage spread (SPIKY), not the VPA's age/bounds.
			name: "7_young_vpa_no_lowconf",
			cw: model.CollectedWorkload{
				Workload:   model.Workload{Kind: model.KindDeployment, Name: "new", Replicas: 4},
				HPA:        hpa(1, 10, resMetric(model.ResourceCPU, 80, 70)),
				Containers: []model.ContainerAnalysis{cpuContainer("app", 1000, mi(950))},
				VPACreated: time.Date(2026, 6, 3, 0, 0, 0, 0, time.UTC).Add(-10 * time.Hour),
			},
			opts:       Options{InPlaceAvailable: true, Now: time.Date(2026, 6, 3, 0, 0, 0, 0, time.UTC)},
			actionable: true,
			verdict:    model.VerdictSafe, replicas: 4, predUtil: pi(74),
			flags:    nil,
			deltaCPU: -200, deltaMem: 0,
		},
		{
			name: "8a_decoupled_external",
			cw: model.CollectedWorkload{
				Workload: model.Workload{Kind: model.KindDeployment, Name: "q", Replicas: 4},
				HPA: hpa(1, 10, model.HPAMetric{
					SourceType: model.MetricExternal, TargetType: model.TargetAverageValue,
					Identifier: "queue_depth",
				}),
				Containers: []model.ContainerAnalysis{cpuContainer("app", 1000, mi(500))},
			},
			opts: inPlaceOn, actionable: true,
			verdict: model.VerdictDecoupled, replicas: 4, predUtil: nil,
			deltaCPU: -2000, deltaMem: 0,
		},
		{
			name: "8b_decoupled_resource_averagevalue",
			cw: model.CollectedWorkload{
				Workload: model.Workload{Kind: model.KindDeployment, Name: "q2", Replicas: 4},
				HPA: hpa(1, 10, model.HPAMetric{
					SourceType: model.MetricResource, TargetType: model.TargetAverageValue,
					ResourceName: model.ResourceCPU, Identifier: "cpu",
				}),
				Containers: []model.ContainerAnalysis{cpuContainer("app", 1000, mi(500))},
			},
			opts: inPlaceOn, actionable: true,
			verdict: model.VerdictDecoupled, replicas: 4, predUtil: nil,
			deltaCPU: -2000, deltaMem: 0,
		},
		{
			name: "9_no_hpa",
			cw: model.CollectedWorkload{
				Workload:   model.Workload{Kind: model.KindDeployment, Name: "batch", Replicas: 4},
				HPA:        model.HPAInfo{Present: false},
				Containers: []model.ContainerAnalysis{cpuContainer("app", 1000, mi(500))},
			},
			opts: inPlaceOn, actionable: true,
			verdict: model.VerdictNoHPA, replicas: 4, predUtil: nil,
			deltaCPU: -2000, deltaMem: 0,
		},
		{
			name: "10_hits_ceiling",
			cw: model.CollectedWorkload{
				Workload:   model.Workload{Kind: model.KindDeployment, Name: "hot", Replicas: 4},
				HPA:        hpa(1, 5, resMetric(model.ResourceCPU, 70, 60)),
				Containers: []model.ContainerAnalysis{cpuContainer("app", 1000, mi(250))},
			},
			opts: inPlaceOn, actionable: true,
			verdict: model.VerdictHitsCeiling, replicas: 5, predUtil: pi(240),
			deltaCPU: -2750, deltaMem: 0,
		},
		{
			name: "11_scale_in",
			cw: model.CollectedWorkload{
				Workload:   model.Workload{Kind: model.KindDeployment, Name: "over", Replicas: 6},
				HPA:        hpa(1, 10, resMetric(model.ResourceCPU, 80, 70)),
				Containers: []model.ContainerAnalysis{cpuContainer("app", 500, mi(1000))},
			},
			opts: inPlaceOn, actionable: true,
			verdict: model.VerdictScaleIn, replicas: 3, predUtil: pi(35),
			deltaCPU: 0, deltaMem: 0, // 6*500 -> 3*1000, footprint unchanged
		},
	}
}

func TestAnalyze(t *testing.T) {
	for _, tc := range cases() {
		t.Run(tc.name, func(t *testing.T) {
			got := Analyze(tc.cw, tc.opts)

			if got.Actionable != tc.actionable {
				t.Errorf("Actionable = %v, want %v", got.Actionable, tc.actionable)
			}
			if got.Verdict != tc.verdict {
				t.Errorf("Verdict = %q, want %q", got.Verdict, tc.verdict)
			}
			if got.PredictedReplicas != tc.replicas {
				t.Errorf("PredictedReplicas = %d, want %d", got.PredictedReplicas, tc.replicas)
			}
			if !samePtrInt32(got.PredictedUtilization, tc.predUtil) {
				t.Errorf("PredictedUtilization = %s, want %s", showI32(got.PredictedUtilization), showI32(tc.predUtil))
			}
			if !sameFlags(got.Flags, tc.flags) {
				t.Errorf("Flags = %v, want %v", got.Flags, tc.flags)
			}
			if got.FootprintDelta.CPUMilli != tc.deltaCPU {
				t.Errorf("Delta.CPUMilli = %d, want %d", got.FootprintDelta.CPUMilli, tc.deltaCPU)
			}
			if got.FootprintDelta.MemBytes != tc.deltaMem {
				t.Errorf("Delta.MemBytes = %d, want %d", got.FootprintDelta.MemBytes, tc.deltaMem)
			}
		})
	}
}

// TestNotActionable verifies a workload with no VPA recommendation is skipped.
func TestNotActionable(t *testing.T) {
	cw := model.CollectedWorkload{
		Workload:   model.Workload{Kind: model.KindDeployment, Name: "x", Replicas: 3},
		HPA:        hpa(1, 10, resMetric(model.ResourceCPU, 80, 50)),
		Containers: []model.ContainerAnalysis{{Name: "app", Requests: model.Resources{CPUMilli: mi(1000)}}},
	}
	got := Analyze(cw, Options{InPlaceAvailable: true})
	if got.Actionable {
		t.Fatalf("expected not actionable")
	}
	if got.Verdict != "" {
		t.Errorf("Verdict = %q, want empty", got.Verdict)
	}
}

// --- assertion helpers ------------------------------------------------------

func samePtrInt32(a, b *int32) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func showI32(p *int32) string {
	if p == nil {
		return "nil"
	}
	return strconv.Itoa(int(*p))
}

func sameFlags(a, b []model.Flag) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[model.Flag]int{}
	for _, f := range a {
		seen[f]++
	}
	for _, f := range b {
		seen[f]--
	}
	for _, v := range seen {
		if v != 0 {
			return false
		}
	}
	return true
}
