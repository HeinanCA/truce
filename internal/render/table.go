package render

import (
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/heinanca/truce/internal/model"
)

// renderTable writes the workload table. When wide is true it appends the
// current utilization and effective tolerance columns and a per-container
// breakdown beneath each row.
func renderTable(w io.Writer, rows []model.WorkloadAnalysis, p Palette, wide bool) error {
	if len(rows) == 0 {
		fmt.Fprintln(w, p.Dim("No actionable workloads in scope (need a workload with both a VPA recommendation)."))
		return nil
	}

	fmt.Fprintln(w, p.Dim("Δ FOOTPRINT shows cpu / mem change if the rec is applied; green = savings, red = growth. Predictions are predicted, not confirmed."))
	fmt.Fprintln(w)

	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)

	if wide {
		fmt.Fprintln(tw, "WORKLOAD\tHPA\tNOW\tVPA REC\tPREDICT\tΔ FOOTPRINT\tVERDICT\tFLAGS\tUTIL\tTOL\tBASIS")
	} else {
		fmt.Fprintln(tw, "WORKLOAD\tHPA\tNOW\tVPA REC\tPREDICT\tΔ FOOTPRINT\tVERDICT\tFLAGS")
	}

	for _, a := range rows {
		oc, om, ohc, ohm := perReplicaOld(a.Containers)
		nc, nm, nhc, nhm := perReplicaNew(a.Containers)
		now := fmt.Sprintf("%d × %s", a.CurrentReplicas, resourceStr(oc, om, ohc, ohm))
		rec := resourceStr(nc, nm, nhc, nhm)
		predict := fmt.Sprintf("%d→%d", a.CurrentReplicas, a.PredictedReplicas)

		base := fmt.Sprintf("%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s",
			a.Workload.Key(), hpaCol(a), now, rec, predict,
			deltaStr(a.FootprintDelta, p), verdictStr(a.Verdict, p), flagsStr(a.Flags, p))

		if wide {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", base, utilCol(a), tolCol(a), basisCol(a))
			writeContainerRows(tw, a)
		} else {
			fmt.Fprintln(tw, base)
		}
	}
	if err := tw.Flush(); err != nil {
		return err
	}

	renderFooter(w, rows, p)
	return nil
}

// writeContainerRows prints an indented per-container breakdown for wide output.
func writeContainerRows(tw io.Writer, a model.WorkloadAnalysis) {
	for _, c := range a.Containers {
		oc, om := c.Requests.CPUMilli, c.Requests.MemBytes
		nowC := resourceStr(deref(oc), deref(om), oc != nil, om != nil)
		recC := "—"
		if c.HasVPA {
			tc, tm := c.VPA.Target.CPUMilli, c.VPA.Target.MemBytes
			recC = resourceStr(deref(tc), deref(tm), tc != nil, tm != nil)
		}
		fmt.Fprintf(tw, "  └ %s\t\t%s\t%s\t\t\t\t\t\t\t\n", c.Name, nowC, recC)
	}
}

// hpaCol renders the HPA / binding-metric cell.
func hpaCol(a model.WorkloadAnalysis) string {
	if !a.HPA.Present {
		return "—"
	}
	if a.BindingMetric != nil && a.BindingMetric.TargetUtilization != nil {
		return fmt.Sprintf("%s:%d%%", a.BindingMetric.Identifier, *a.BindingMetric.TargetUtilization)
	}
	if a.HPA.ManagedByKEDA {
		if len(a.HPA.KEDATriggers) > 0 {
			return "KEDA:" + a.HPA.KEDATriggers[0]
		}
		return "KEDA"
	}
	if a.Verdict == model.VerdictDecoupled {
		return "decoupled"
	}
	return "—"
}

// utilCol renders the predicted utilization for the binding metric (wide).
func utilCol(a model.WorkloadAnalysis) string {
	if a.PredictedUtilization == nil {
		return "—"
	}
	return fmt.Sprintf("→%d%%", *a.PredictedUtilization)
}

// tolCol renders the effective up/down tolerance used (wide).
func tolCol(a model.WorkloadAnalysis) string {
	return fmt.Sprintf("↑%.0f%% ↓%.0f%%", a.ToleranceUp*100, a.ToleranceDown*100)
}

// basisCol renders whether the row used a time-series peak or a snapshot (wide).
func basisCol(a model.WorkloadAnalysis) string {
	if a.UsageBasis == model.BasisPeak {
		return "peak"
	}
	return "snapshot"
}

func deref(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

// renderFooter prints the net footprint if all shown recs are applied, plus
// explicit backfire callouts (recs that grow the footprint).
func renderFooter(w io.Writer, rows []model.WorkloadAnalysis, p Palette) {
	var net model.Delta
	var backfires []model.WorkloadAnalysis
	for _, a := range rows {
		net = net.Add(a.FootprintDelta)
		if a.FootprintDelta.CPUMilli > 0 || a.FootprintDelta.MemBytes > 0 {
			backfires = append(backfires, a)
		}
	}

	fmt.Fprintln(w)
	fmt.Fprintf(w, "%s if all %d shown recs applied: %s\n",
		p.Bold("Net footprint Δ"), len(rows), deltaStr(net, p))

	if len(backfires) > 0 {
		fmt.Fprintln(w, p.Bold(p.Red(fmt.Sprintf("\n⚠ %d backfire(s) — a rec that grows footprint (HPA scales out past the savings):", len(backfires)))))
		for _, a := range backfires {
			fmt.Fprintf(w, "  %s %s: %s → %s (%s)\n",
				p.Red("•"), a.Workload.Key(), verdictStr(a.Verdict, p),
				fmt.Sprintf("%d→%d replicas", a.CurrentReplicas, a.PredictedReplicas),
				deltaStr(a.FootprintDelta, p))
		}
	}
}
