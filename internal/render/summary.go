package render

import (
	"fmt"
	"io"

	"github.com/heinanca/truce/internal/model"
)

// renderSummary is the default, money-first human view: it leads with the dollar
// savings, then a three-bucket action plan (safe / careful / do-not) in plain words
// and plain units (cores + GB), so a non-DevOps reader knows the bottom line and what
// to do without parsing flags, deltas, or millicores. The dense engineering tables
// stay one flag away (-o table / -o recommend / -o diff). It reuses the same
// classifier as -o advice (classify, trapReason) so the buckets never disagree.
func renderSummary(w io.Writer, r Report, rows []model.WorkloadAnalysis, p Palette) error {
	if len(rows) == 0 {
		fmt.Fprintln(w, p.Dim("No actionable workloads (need a workload with a VPA recommendation)."))
		return nil
	}

	var doNow, withCare, doNot []model.WorkloadAnalysis
	var savings model.Delta
	anyGitOps, anyLowConf := false, false
	for _, a := range rows {
		switch classify(a) {
		case classDoNow:
			doNow = append(doNow, a)
			savings = savings.Add(a.FootprintDelta)
		case classWithCare:
			withCare = append(withCare, a)
			savings = savings.Add(a.FootprintDelta)
		default:
			doNot = append(doNot, a)
		}
		if a.HasFlag(model.FlagGitOps) {
			anyGitOps = true
		}
		if a.HasFlag(model.FlagLowConf) {
			anyLowConf = true
		}
	}

	renderSavings(w, r.Cost, p)
	if r.SnapshotOnly {
		fmt.Fprintln(w, p.Yellow("  ⚠ Measured at one moment, not over time — may understate traffic spikes. Re-run with --prometheus for peak-aware advice."))
	}
	fmt.Fprintln(w)

	if len(doNow) > 0 {
		fmt.Fprintln(w, p.Green(fmt.Sprintf("SAFE TO APPLY NOW — %d service(s) (clean savings, nothing else changes)", len(doNow))))
		for _, a := range sorted(doNow) {
			fmt.Fprintf(w, "  • %s  %s\n", a.Workload.Name, plainChange(a))
		}
		fmt.Fprintln(w)
	}

	if len(withCare) > 0 {
		fmt.Fprintln(w, p.Yellow(fmt.Sprintf("APPLY WITH CARE — %d service(s) (saves money, but the autoscaler adds pods at peak)", len(withCare))))
		for _, a := range sorted(withCare) {
			fmt.Fprintf(w, "  • %s  %s — autoscaler may go %d→%d pods; needs node room\n",
				a.Workload.Name, plainChange(a), a.CurrentReplicas, a.PredictedReplicas)
		}
		fmt.Fprintln(w)
	}

	if len(doNot) > 0 {
		fmt.Fprintln(w, p.Red(fmt.Sprintf("DO NOT APPLY — %d service(s) (would crash or cost more)", len(doNot))))
		for _, a := range sorted(doNot) {
			fmt.Fprintf(w, "  • %s  %s\n", a.Workload.Name, plainReason(a))
		}
		fmt.Fprintln(w)
	}

	renderHowTo(w, r, p, anyGitOps, anyLowConf)
	fmt.Fprintln(w, p.Dim("\nFull engineering detail: truce -o table · sizing: truce -o recommend · patches: truce -o diff"))
	return nil
}

// renderSavings prints the dollar headline and freed-resource line from the cost
// report. When pricing is unavailable it shows the resource/node savings and says so.
func renderSavings(w io.Writer, c model.CostReport, p Palette) {
	fmt.Fprintln(w, p.Bold("SAVINGS"))
	freed := fmt.Sprintf("Frees %s CPU + %s", coresStr(c.FreedCPUMilli), gbStr(c.FreedMemBytes))
	if c.NodesSavedHigh > 0 {
		freed += fmt.Sprintf(" → retire %s of %d nodes", savedRange(c.NodesSavedLow, c.NodesSavedHigh), totalNodes(c))
	}
	if !c.Enabled {
		fmt.Fprintf(w, "  %s.\n", freed)
		fmt.Fprintln(w, p.Dim("  No $ figure — node pricing unavailable (PRICE-MISSING)."))
		return
	}
	headline := fmt.Sprintf("Up to %s/month", money(c.TotalMonthlyHigh))
	if c.TotalMonthlyLow != c.TotalMonthlyHigh {
		headline += fmt.Sprintf("  (range %s–%s)", money(c.TotalMonthlyLow), money(c.TotalMonthlyHigh))
	}
	fmt.Fprintln(w, p.Bold("  "+headline)+".")
	fmt.Fprintf(w, "  %s.\n", freed)
}

// plainChange describes the per-replica request change in plain units, showing only
// the dimensions that change (mirrors changeDesc but in cores/GB).
func plainChange(a model.WorkloadAnalysis) string {
	oc, om, ohc, ohm := perReplicaOld(a.Containers)
	nc, nm, nhc, nhm := perReplicaNew(a.Containers)
	parts := ""
	if ohm || nhm {
		parts = fmt.Sprintf("memory %s → %s", gbStr(om), gbStr(nm))
	}
	if ohc || nhc {
		if parts != "" {
			parts += ",  "
		}
		parts += fmt.Sprintf("CPU %s → %s", coresStr(oc), coresStr(nc))
	}
	if parts == "" {
		return "no request change"
	}
	return parts
}

// plainReason explains, in one plain-unit sentence, why a workload must not be applied
// (mirrors trapReason's branch logic with GB instead of Gi).
func plainReason(a model.WorkloadAnalysis) string {
	switch {
	case a.Verdict == model.VerdictOOMRisk:
		return "memory cut is below the workload's real peak usage → would crash pods (OOM). Leave memory alone."
	case a.Verdict == model.VerdictHitsCeiling && a.FootprintDelta.MemBytes > 0:
		return fmt.Sprintf("cutting CPU pins the autoscaler at its max (%d→%d pods) and ADDS %s of memory — a net loss.",
			a.CurrentReplicas, a.PredictedReplicas, gbStr(a.FootprintDelta.MemBytes))
	case a.Verdict == model.VerdictHitsCeiling:
		return fmt.Sprintf("the cut pins the autoscaler at its max (%d→%d pods) with no headroom left. Raise the pod ceiling first or skip.",
			a.CurrentReplicas, a.PredictedReplicas)
	default: // footprint growth without ceiling
		return fmt.Sprintf("applying it grows the footprint because the autoscaler scales out (%d→%d pods) past the per-pod savings.",
			a.CurrentReplicas, a.PredictedReplicas)
	}
}
