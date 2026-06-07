package render

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/heinanca/truce/internal/model"
	"github.com/heinanca/truce/internal/recommend"
	"github.com/heinanca/truce/internal/valuesfile"
)

// RenderRecommendation prints the per-service recommendation: truce's own
// peak-based request values (not the VPA target), the measured spread behind
// them, the HPA re-prediction with the new request, the footprint delta, the VPA
// cross-check, and a paste-ready snippet.
func RenderRecommendation(w io.Writer, rec recommend.Recommendation, p Palette) {
	fmt.Fprintln(w, p.Bold(fmt.Sprintf("RECOMMENDED VALUES — %s", rec.Service)))
	if rec.Contrast != "" {
		fmt.Fprintln(w, "  "+p.Dim(rec.Contrast))
	}

	// HPA re-prediction with the new request — the differentiator.
	pred := fmt.Sprintf("%d → %d replicas", rec.CurrentReplicas, rec.PredictedReplicas)
	if rec.HPAStillScales {
		fmt.Fprintln(w, p.Red("  HPA re-prediction (new request): "+pred+"  ⚠ STILL SCALES OUT"))
	} else {
		fmt.Fprintln(w, p.Green("  HPA re-prediction (new request): "+pred+"  (no scale-out)"))
	}
	fmt.Fprintf(w, "  Footprint Δ: %s\n\n", deltaStr(rec.FootprintDelta, p))

	for _, c := range rec.Containers {
		fmt.Fprintf(w, "  %s\n", p.Bold(c.Name))
		if c.CPURec != nil {
			fmt.Fprintf(w, "    cpu:    %s → %s   (%s)\n",
				cpuOrDash(c.CPUNow), recColor(cpuStr(*c.CPURec), c.CPUWhy, p), c.CPUWhy)
		}
		if c.CPULimit != nil {
			fmt.Fprintf(w, "    cpu limit: %s\n", cpuStr(*c.CPULimit))
		}
		if c.MemRec != nil {
			fmt.Fprintf(w, "    memory: %s → %s   (%s)\n",
				memOrDash(c.MemNow), recColor(memStr(*c.MemRec), c.MemWhy, p), c.MemWhy)
		}
		if c.MemLimit != nil {
			fmt.Fprintf(w, "    memory limit: %s\n", memStr(*c.MemLimit))
		}
		fmt.Fprintf(w, "    %s\n", p.Dim(spreadLine(c)))
		if c.VPACPU != nil || c.VPAMem != nil {
			fmt.Fprintf(w, "    %s\n", p.Dim("VPA cross-check: "+vpaLine(c)))
		}
		if len(c.Flags) > 0 {
			fmt.Fprintf(w, "    flags: %s\n", flagsStr(c.Flags, p))
		}
	}

	if rec.RaiseMaxTo > 0 {
		fmt.Fprintln(w, p.Yellow(fmt.Sprintf("\n  ⚠ Even peak-sized, the HPA pins at its ceiling — raise maxReplicas to ≥ %d.", rec.RaiseMaxTo)))
	}

	// Paste-ready snippet.
	fmt.Fprintln(w, p.Bold("\n  Paste into your values file under each container's resources:"))
	for _, c := range rec.Containers {
		fmt.Fprintf(w, "    # %s\n    resources:\n      requests:\n", c.Name)
		if c.CPURec != nil {
			fmt.Fprintf(w, "        cpu: \"%dm\"\n", *c.CPURec)
		}
		if c.MemRec != nil {
			fmt.Fprintf(w, "        memory: \"%s\"\n", memStr(*c.MemRec))
		}
		if c.CPULimit != nil || c.MemLimit != nil {
			fmt.Fprintln(w, "      limits:")
			if c.CPULimit != nil {
				fmt.Fprintf(w, "        cpu: \"%dm\"\n", *c.CPULimit)
			}
			if c.MemLimit != nil {
				fmt.Fprintf(w, "        memory: \"%s\"\n", memStr(*c.MemLimit))
			}
		}
	}
}

// renderRecommendTable prints truce's peak-based request for every actionable
// workload in one shot — the paste-ready list. Values are sized to the measured
// peak (HPA-coupled) or p95 (Burstable), never below the observed peak.
func renderRecommendTable(w io.Writer, r Report, rows []model.WorkloadAnalysis, cfg recommend.Config, p Palette) error {
	if len(rows) == 0 {
		fmt.Fprintln(w, p.Dim("No actionable workloads (need a VPA recommendation)."))
		return nil
	}
	if r.SnapshotOnly {
		fmt.Fprintln(w, p.Yellow("⚠ snapshot basis — no usage spread available, values HOLD at current. Pass --prometheus for real recommendations.\n"))
	}

	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "WORKLOAD\tCONTAINER\tCPU\tMEMORY\tCPU p95/max\tSPIKE\tREPLICAS\tVPA cpu\tFLAGS")
	for _, a := range rows {
		rec := recommend.ForWith(a, cfg)
		repl := fmt.Sprintf("%d→%d", rec.CurrentReplicas, rec.PredictedReplicas)
		for _, c := range rec.Containers {
			cpu := changeCell(c.CPUNow, c.CPURec, cpuStr, c.CPUWhy, p)
			mem := changeCell(c.MemNow, c.MemRec, memStr, c.MemWhy, p)
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				a.Workload.Name, c.Name, cpu, mem,
				p95MaxCell(c), spikeCell(c), repl, cpuPtr(c.VPACPU), flagsStr(c.Flags, p))
		}
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	fmt.Fprintln(w, p.Dim("\nCPU sized to cpu_max (HPA-coupled) or cpu_p95 (Burstable) + headroom; memory to mem_max + headroom. "+
		"REPLICAS = HPA re-predicted with the new request. HOLD = no change (no usage data)."))
	return nil
}

// changeCell renders "now→rec", yellow when it's a HOLD (no change).
func changeCell(now, rec *int64, fmtFn func(int64) string, why string, p Palette) string {
	if rec == nil {
		return "—"
	}
	nowStr := "—"
	if now != nil {
		nowStr = fmtFn(*now)
	}
	recStr := fmtFn(*rec)
	if strings.HasPrefix(why, "HOLD") {
		return nowStr + "→" + p.Yellow(recStr)
	}
	return nowStr + "→" + p.Green(recStr)
}

// p95MaxCell renders "p95/max" for CPU, "—" when unknown.
func p95MaxCell(c recommend.ContainerRec) string {
	if c.CPUP95 == nil && c.CPUMax == nil {
		return "—"
	}
	return cpuPtr(c.CPUP95) + "/" + cpuPtr(c.CPUMax)
}

// spikeCell renders the spikiness ratio, "—" when unknown.
func spikeCell(c recommend.ContainerRec) string {
	if c.Spikiness <= 0 {
		return "—"
	}
	return fmt.Sprintf("%.1f×", c.Spikiness)
}

// spreadLine renders the measured CPU/memory spread for the single-service view.
func spreadLine(c recommend.ContainerRec) string {
	cpu := fmt.Sprintf("cpu p50/p95/max %s/%s/%s", cpuPtr(c.CPUP50), cpuPtr(c.CPUP95), cpuPtr(c.CPUMax))
	mem := "mem max " + memPtr(c.MemMax)
	spike := ""
	if c.Spikiness > 0 {
		spike = fmt.Sprintf(", spikiness %.1f×", c.Spikiness)
	}
	return cpu + ", " + mem + spike
}

// vpaLine renders the VPA target for cross-check.
func vpaLine(c recommend.ContainerRec) string {
	return "cpu " + cpuPtr(c.VPACPU) + " / mem " + memPtr(c.VPAMem)
}

func cpuPtr(p *int64) string {
	if p == nil {
		return "—"
	}
	return cpuStr(*p)
}

func memPtr(p *int64) string {
	if p == nil {
		return "—"
	}
	return memStr(*p)
}

// RenderValuesDiff shows the service's committed values in the file next to the
// recommendation, as a PR-ready before/after.
func RenderValuesDiff(w io.Writer, path string, blocks []valuesfile.Block, rec recommend.Recommendation, p Palette) {
	fmt.Fprintln(w, p.Bold(fmt.Sprintf("\n📄 %s", path)))

	if len(blocks) == 0 {
		fmt.Fprintln(w, p.Yellow("  No resources.requests block found in this file — paste the snippet above"))
		fmt.Fprintln(w, p.Yellow("  under the service's container resources manually."))
		return
	}
	if len(blocks) > 1 {
		fmt.Fprintln(w, p.Yellow(fmt.Sprintf("  %d resources.requests blocks found; showing the first. Targeted edits:", len(blocks))))
		for _, b := range blocks {
			loc := b.Path
			if loc == "" {
				loc = "(root)"
			}
			fmt.Fprintf(w, "    - %s (cpu=%s memory=%s)\n", loc, dash(b.CPU), dash(b.Mem))
		}
	}

	b := blocks[0]
	loc := b.Path
	if loc == "" {
		loc = "(root)"
	}
	fmt.Fprintf(w, "  at %s:\n", loc)

	if len(rec.Containers) == 0 {
		return
	}
	c := rec.Containers[0]
	if c.CPURec != nil {
		fmt.Fprintf(w, "    cpu:    %s → %s\n", dash(b.CPU), p.Green(cpuStr(*c.CPURec)))
	}
	if c.MemRec != nil {
		fmt.Fprintf(w, "    memory: %s → %s\n", dash(b.Mem), p.Green(memStr(*c.MemRec)))
	}
}

func dash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// recColor greens an applied value, yellows a HOLD (no change made).
func recColor(val, why string, p Palette) string {
	if strings.HasPrefix(why, "HOLD") {
		return p.Yellow(val)
	}
	return p.Green(val)
}

func cpuOrDash(p *int64) string {
	if p == nil {
		return "—"
	}
	return cpuStr(*p)
}

func memOrDash(p *int64) string {
	if p == nil {
		return "—"
	}
	return memStr(*p)
}
