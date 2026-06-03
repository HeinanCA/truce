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
		Containers:           []model.ContainerAnalysis{{Name: "app", Requests: model.Resources{CPUMilli: mi(1000)}, HasVPA: true, VPA: model.VPARec{Target: model.Resources{CPUMilli: mi(500)}}}},
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
	for _, want := range []string{"kind: Deployment", "name: web", "cpu: \"500m\"", "kubectl -n prod patch deployment web", "verdict=SCALE-OUT"} {
		if !strings.Contains(out, want) {
			t.Errorf("diff output missing %q\n---\n%s", want, out)
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
