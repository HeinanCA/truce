package render

import (
	"fmt"
	"io"

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
				cpuOrDash(c.CPUNow), p.Green(cpuStr(*c.CPURec)), c.CPUWhy)
		}
		if c.MemRec != nil {
			fmt.Fprintf(w, "    memory: %s → %s   (%s)\n",
				memOrDash(c.MemNow), p.Green(memStr(*c.MemRec)), c.MemWhy)
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
