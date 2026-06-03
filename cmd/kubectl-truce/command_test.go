package main

import (
	"strings"
	"testing"

	"github.com/heinanca/truce/internal/model"
)

func TestBasisStatus(t *testing.T) {
	o := &options{promURL: "http://localhost:9090", promWindow: "7d", cpuQuantile: 0.95}
	peakRow := model.WorkloadAnalysis{Actionable: true, UsageBasis: model.BasisPeak}
	snapRow := model.WorkloadAnalysis{Actionable: true, UsageBasis: model.BasisSnapshot}
	rows := []model.WorkloadAnalysis{peakRow, snapRow}

	tests := []struct {
		name         string
		rows         []model.WorkloadAnalysis
		configured   bool
		reachable    bool
		wantSnap     bool
		wantContains string
	}{
		{"not configured", rows, false, false, true, "snapshot"},
		{"unreachable", []model.WorkloadAnalysis{snapRow, snapRow}, true, false, true, "UNREACHABLE"},
		{"reachable no data", []model.WorkloadAnalysis{snapRow, snapRow}, true, true, true, "no usable data"},
		{"partial", rows, true, true, false, "1 of 2"},
		{"all peak", []model.WorkloadAnalysis{peakRow, peakRow}, true, true, false, "Prometheus peak —"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			label, snap := basisStatus(tt.rows, o, tt.configured, tt.reachable)
			if snap != tt.wantSnap {
				t.Errorf("snapshotOnly = %v, want %v (label: %s)", snap, tt.wantSnap, label)
			}
			if !strings.Contains(label, tt.wantContains) {
				t.Errorf("label %q missing %q", label, tt.wantContains)
			}
		})
	}
}

func TestParseVerdicts(t *testing.T) {
	got, err := parseVerdicts([]string{"scale-out", "OOM", "no-hpa"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []model.Verdict{model.VerdictScaleOut, model.VerdictOOMRisk, model.VerdictNoHPA}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got[%d]=%q, want %q", i, got[i], want[i])
		}
	}

	if _, err := parseVerdicts([]string{"bogus"}); err == nil {
		t.Error("expected error for unknown verdict")
	}
}

func TestGateCheck(t *testing.T) {
	rows := []model.WorkloadAnalysis{
		{Actionable: true, Verdict: model.VerdictSafe},
		{Actionable: true, Verdict: model.VerdictScaleOut},
		{Actionable: false, Verdict: model.VerdictOOMRisk}, // ignored: not actionable
	}

	if err := gateCheck(rows, nil); err != nil {
		t.Errorf("no fail-on should not error: %v", err)
	}
	if err := gateCheck(rows, []model.Verdict{model.VerdictScaleIn}); err != nil {
		t.Errorf("non-matching fail-on should not error: %v", err)
	}
	err := gateCheck(rows, []model.Verdict{model.VerdictScaleOut})
	if err == nil {
		t.Fatal("expected gateError for matching SCALE-OUT")
	}
	if _, ok := err.(*gateError); !ok {
		t.Errorf("expected *gateError, got %T", err)
	}

	// Not-actionable OOM RISK row must NOT trip the gate.
	if err := gateCheck(rows, []model.Verdict{model.VerdictOOMRisk}); err != nil {
		t.Errorf("non-actionable row should not trip gate: %v", err)
	}
}
