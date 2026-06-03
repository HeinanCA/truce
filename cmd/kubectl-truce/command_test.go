package main

import (
	"testing"

	"github.com/heinanca/truce/internal/model"
)

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
