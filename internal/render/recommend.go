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

// RenderRecommendation prints the per-service recommended values: the contrast
// against the raw VPA target, the current→recommended change per container with
// the reasoning, any maxReplicas guidance, and a paste-ready requests snippet.
func RenderRecommendation(w io.Writer, rec recommend.Recommendation, p Palette) {
	title := fmt.Sprintf("RECOMMENDED VALUES — %s", rec.Service)
	if rec.Provisional {
		title += "  (PROVISIONAL — VPA history < 48h)"
	}
	fmt.Fprintln(w, p.Bold(title))

	if rec.Contrast != "" {
		fmt.Fprintln(w, "  "+p.Dim(rec.Contrast))
	}
	fmt.Fprintln(w)

	for _, c := range rec.Containers {
		fmt.Fprintf(w, "  %s\n", p.Bold(c.Name))
		if c.CPURec != nil {
			fmt.Fprintf(w, "    cpu:    %s → %s   (%s)\n",
				cpuOrDash(c.CPUNow), recColor(cpuStr(*c.CPURec), c.CPUWhy, p), c.CPUWhy)
		}
		if c.MemRec != nil {
			fmt.Fprintf(w, "    memory: %s → %s   (%s)\n",
				memOrDash(c.MemNow), recColor(memStr(*c.MemRec), c.MemWhy, p), c.MemWhy)
		}
	}

	if rec.RaiseMaxTo > 0 {
		fmt.Fprintln(w, p.Yellow(fmt.Sprintf("\n  ⚠ This workload hits its HPA ceiling — raise maxReplicas to ≥ %d, "+
			"a fix neither VPA nor HPA will suggest.", rec.RaiseMaxTo)))
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
	}
}

// renderRecommendTable prints the safe recommended request for every actionable
// workload in one shot — the paste-ready list. Every value is floored at the
// observed peak, so nothing here can starve or OOM the workload.
func renderRecommendTable(w io.Writer, r Report, rows []model.WorkloadAnalysis, p Palette) error {
	if len(rows) == 0 {
		fmt.Fprintln(w, p.Dim("No actionable workloads (need a VPA recommendation)."))
		return nil
	}
	if r.SnapshotOnly {
		fmt.Fprintln(w, p.Yellow("⚠ snapshot basis — values shown HOLD at current (no cut without peak data). Pass --prometheus for real recommendations.\n"))
	}

	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "WORKLOAD\tCONTAINER\tCPU\tMEMORY\tWHY")
	for _, a := range rows {
		rec := recommend.For(a)
		for _, c := range rec.Containers {
			cpu := changeCell(c.CPUNow, c.CPURec, cpuStr, c.CPUWhy, p)
			mem := changeCell(c.MemNow, c.MemRec, memStr, c.MemWhy, p)
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
				a.Workload.Name, c.Name, cpu, mem, shortWhy(c.CPUWhy, c.MemWhy))
		}
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	fmt.Fprintln(w, p.Dim("\nEvery value is floored at the observed 7-day peak +15% — safe to apply. HOLD = no change (insufficient data)."))
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

// shortWhy picks the most informative one-liner for the row.
func shortWhy(cpuWhy, memWhy string) string {
	if strings.HasPrefix(cpuWhy, "holds the HPA") {
		return "↑ sized to hold HPA at target (stops scale-out)"
	}
	if strings.HasPrefix(cpuWhy, "HOLD") || strings.HasPrefix(memWhy, "HOLD") {
		return "HOLD — no peak data"
	}
	if strings.Contains(cpuWhy, "KEDA") {
		return "KEDA external — request safe"
	}
	return "floored at observed peak"
}

// RenderValuesDiff shows the service's committed values in the file next to the
// recommendation, as a PR-ready before/after. It pairs the file's
// resources.requests block(s) with the recommendation; a per-service file with
// one block and one container pairs directly.
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

	// Pair with the first container's recommendation (per-service file case).
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

// recColor greens an applied value, but yellows a HOLD (no change made).
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
