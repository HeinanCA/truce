package render

import (
	"sort"

	"github.com/heinanca/truce/internal/model"
)

// SortMode selects row ordering.
type SortMode string

const (
	// SortDefault orders problems first, then by descending footprint magnitude.
	SortDefault SortMode = ""
	SortDelta   SortMode = "delta"
	SortName    SortMode = "name"
	SortVerdict SortMode = "verdict"
)

// verdictRank gives a stable severity ordering for the "verdict" sort and the
// default problems-first tiebreak.
func verdictRank(v model.Verdict) int {
	switch v {
	case model.VerdictOOMRisk:
		return 0
	case model.VerdictHitsCeiling:
		return 1
	case model.VerdictScaleOut:
		return 2
	case model.VerdictScaleIn:
		return 3
	case model.VerdictSafe:
		return 4
	case model.VerdictNoHPA:
		return 5
	case model.VerdictDecoupled:
		return 6
	default:
		return 7
	}
}

func absInt64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// Filter applies the --only and --problems-only selectors. A nil/empty only set
// keeps all verdicts. Non-actionable rows are always dropped (nothing to advise).
func Filter(rows []model.WorkloadAnalysis, only []model.Verdict, problemsOnly bool) []model.WorkloadAnalysis {
	onlySet := map[model.Verdict]bool{}
	for _, v := range only {
		onlySet[v] = true
	}
	out := rows[:0:0]
	for _, r := range rows {
		if !r.Actionable {
			continue
		}
		if problemsOnly && !r.Verdict.IsProblem() {
			continue
		}
		if len(onlySet) > 0 && !onlySet[r.Verdict] {
			continue
		}
		out = append(out, r)
	}
	return out
}

// Sort orders rows per the mode, returning a new slice (input untouched).
func Sort(rows []model.WorkloadAnalysis, mode SortMode) []model.WorkloadAnalysis {
	out := make([]model.WorkloadAnalysis, len(rows))
	copy(out, rows)

	less := func(i, j int) bool {
		a, b := out[i], out[j]
		switch mode {
		case SortName:
			return a.Workload.Key() < b.Workload.Key()
		case SortDelta:
			return absInt64(a.FootprintDelta.CPUMilli) > absInt64(b.FootprintDelta.CPUMilli)
		case SortVerdict:
			if ra, rb := verdictRank(a.Verdict), verdictRank(b.Verdict); ra != rb {
				return ra < rb
			}
			return a.Workload.Key() < b.Workload.Key()
		default: // SortDefault: problems first, then footprint magnitude, then name.
			pa, pb := a.Verdict.IsProblem(), b.Verdict.IsProblem()
			if pa != pb {
				return pa
			}
			ca, cb := absInt64(a.FootprintDelta.CPUMilli), absInt64(b.FootprintDelta.CPUMilli)
			if ca != cb {
				return ca > cb
			}
			return a.Workload.Key() < b.Workload.Key()
		}
	}
	sort.SliceStable(out, less)
	return out
}
