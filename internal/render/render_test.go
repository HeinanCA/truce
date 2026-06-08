package render

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/heinanca/truce/internal/model"
)

func mi(v int64) *int64 { return model.Int64(v) }
func pi(v int32) *int32 { return &v }

func sampleReport() Report {
	scaleOut := model.WorkloadAnalysis{
		Workload:             model.Workload{Kind: model.KindDeployment, Namespace: "prod", Name: "web", Replicas: 4},
		HPA:                  model.HPAInfo{Present: true, MinReplicas: 1, MaxReplicas: 10},
		Containers:           []model.ContainerAnalysis{{Name: "app", Requests: model.Resources{CPUMilli: mi(1000)}, HasVPA: true, VPA: model.VPARec{Target: model.Resources{CPUMilli: mi(500)}}, Spread: model.Spread{CPUP50: mi(300), CPUP95: mi(400), CPUMax: mi(500)}}},
		Actionable:           true,
		Verdict:              model.VerdictScaleOut,
		CurrentReplicas:      4,
		PredictedReplicas:    7,
		BindingMetric:        &model.HPAMetric{Identifier: "cpu", TargetUtilization: pi(70)},
		PredictedUtilization: pi(120),
		ToleranceUp:          0.10, ToleranceDown: 0.10,
		FootprintDelta: model.Delta{CPUMilli: -500},
	}
	backfire := scaleOut
	backfire.Workload.Name = "api"
	backfire.FootprintDelta = model.Delta{CPUMilli: 100} // growth
	notActionable := model.WorkloadAnalysis{
		Workload: model.Workload{Kind: model.KindDeployment, Namespace: "prod", Name: "skip"},
	}
	return Report{
		Cluster: model.ClusterStatus{
			ServerVersion: "v1.35.0", InPlaceTier: model.InPlaceGA,
			InPlaceConfirmedEnabled: true, Scope: "all namespaces",
		},
		Diagnostics: model.Diagnostics{Components: []model.ComponentStatus{
			{Name: "VPA CRD", Available: true, Detail: "installed"},
			{Name: "metrics-server", Available: false, Detail: "not served", Impact: "OOM check disabled", Install: "kubectl apply -f ..."},
		}},
		Workloads: []model.WorkloadAnalysis{scaleOut, backfire, notActionable},
	}
}

func renderTo(t *testing.T, format string) string {
	t.Helper()
	var b bytes.Buffer
	if err := Render(&b, sampleReport(), Options{Format: format, NoColor: true}); err != nil {
		t.Fatalf("Render(%s) error: %v", format, err)
	}
	return b.String()
}

func TestRenderTable(t *testing.T) {
	out := renderTo(t, "table")
	for _, want := range []string{"WORKLOAD", "Deployment/prod/web", "SCALE-OUT", "4→7", "Net footprint", "backfire", "metrics-server", "OOM check disabled"} {
		if !strings.Contains(out, want) {
			t.Errorf("table output missing %q\n---\n%s", want, out)
		}
	}
	if strings.Contains(out, "prod/skip") {
		t.Error("non-actionable workload should be filtered out")
	}
	if strings.Contains(out, "\033[") {
		t.Error("NoColor output should contain no ANSI codes")
	}
}

func TestRenderWide(t *testing.T) {
	out := renderTo(t, "wide")
	for _, want := range []string{"UTIL", "TOL", "→120%", "↑10%", "└ app"} {
		if !strings.Contains(out, want) {
			t.Errorf("wide output missing %q\n---\n%s", want, out)
		}
	}
}

func TestRenderJSON(t *testing.T) {
	out := renderTo(t, "json")
	var parsed jsonReport
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("json invalid: %v\n%s", err, out)
	}
	if len(parsed.Workloads) != 2 {
		t.Errorf("expected 2 actionable workloads, got %d", len(parsed.Workloads))
	}
	if parsed.Cluster.ServerVersion != "v1.35.0" {
		t.Errorf("cluster version not serialized: %+v", parsed.Cluster)
	}
}

func TestRenderDiff(t *testing.T) {
	out := renderTo(t, "diff")
	// No coupled CPU HPA metric on the fixture → sized to cpu_p95 (400) × 1.2 = 480m,
	// truce's own number — never the raw VPA target.
	for _, want := range []string{"kind: Deployment", "name: web", "cpu: \"480m\"", "kubectl -n prod patch deployment web", "verdict=SCALE-OUT"} {
		if !strings.Contains(out, want) {
			t.Errorf("diff output missing %q\n---\n%s", want, out)
		}
	}
}

func TestRenderSummaryIsDefault(t *testing.T) {
	// Empty format must route to the summary view (the new default), not the table.
	out := renderTo(t, "")
	for _, want := range []string{"SAVINGS", "APPLY WITH CARE", "DO NOT APPLY", "Full engineering detail"} {
		if !strings.Contains(out, want) {
			t.Errorf("default(summary) output missing %q\n---\n%s", want, out)
		}
	}
	// The dense engineering table must NOT be the default body.
	if strings.Contains(out, "Δ FOOTPRINT") {
		t.Error("summary should not render the dense table header")
	}
	// Plain units only in the summary body — no millicores/Gi jargon.
	if strings.Contains(out, "500m") || strings.Contains(out, "Gi") {
		t.Errorf("summary should use plain units, found millicores/Gi\n---\n%s", out)
	}
}

func TestRenderSummaryBuckets(t *testing.T) {
	r := sampleReport()
	// web is SCALE-OUT with savings → APPLY WITH CARE; api is SCALE-OUT but grows → DO NOT.
	r.Cost = model.CostReport{Enabled: true, TotalMonthlyLow: 156, TotalMonthlyHigh: 468, FreedCPUMilli: 22090, FreedMemBytes: 35 * 1024 * 1024 * 1024, NodesSavedLow: 1, NodesSavedHigh: 3, Pools: []model.NodePoolCost{{NodeCount: 10}}}
	var b bytes.Buffer
	if err := Render(&b, r, Options{Format: "summary", NoColor: true}); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{"Up to $468/month", "range $156–$468", "retire 1–3 of 10 nodes", "GB"} {
		if !strings.Contains(out, want) {
			t.Errorf("summary missing %q\n---\n%s", want, out)
		}
	}
}

func TestUnknownFormat(t *testing.T) {
	var b bytes.Buffer
	if err := Render(&b, sampleReport(), Options{Format: "xml"}); err == nil {
		t.Error("expected error for unknown format")
	}
}

func TestFilterOnly(t *testing.T) {
	rows := Filter(sampleReport().Workloads, []model.Verdict{model.VerdictScaleOut}, false)
	if len(rows) != 2 { // both web and api are SCALE-OUT
		t.Errorf("only-filter got %d rows, want 2", len(rows))
	}
}
