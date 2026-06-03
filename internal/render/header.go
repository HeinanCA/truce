package render

import (
	"fmt"
	"io"
	"strings"

	"github.com/heinanca/truce/internal/model"
)

// renderHeader prints the cluster-status banner: scope, server version, in-place
// resize status (layered, labeled inferred vs confirmed), VPA presence, and the
// honest capability diagnostics with remediation for anything missing.
func renderHeader(w io.Writer, r Report, p Palette) {
	c := r.Cluster
	fmt.Fprintln(w, p.Headerf("truce — HPA-aware rightsizing advisor (read-only)"))

	scope := c.Scope
	if scope == "" {
		scope = "current namespace"
	}
	fmt.Fprintf(w, "Scope:          %s\n", scope)
	fmt.Fprintf(w, "Server version: %s\n", orDash(c.ServerVersion))
	fmt.Fprintf(w, "In-place resize: %s\n", inPlaceLine(c, p))
	if len(c.NodesNotReady) > 0 {
		fmt.Fprintf(w, "  nodes not ready: %s\n", strings.Join(c.NodesNotReady, "; "))
	}
	if c.SkippedBarePods > 0 {
		fmt.Fprintf(w, "Skipped:        %d bare pod(s) with no controller owner\n", c.SkippedBarePods)
	}
	if r.UsageBasisLabel != "" {
		fmt.Fprintf(w, "Usage basis:    %s\n", r.UsageBasisLabel)
	}
	if r.SnapshotOnly {
		fmt.Fprintln(w, p.Yellow("  ⚠ snapshot only — verdicts use the HPA's instantaneous utilization. A SAFE or"))
		fmt.Fprintln(w, p.Yellow("    mild SCALE verdict measured at low traffic can understate spike-time scale-out"))
		fmt.Fprintln(w, p.Yellow("    and OOM risk. Pass --prometheus <url> for peak-aware (P95 CPU / max mem) verdicts."))
	}

	// Capability diagnostics — honest, with install guidance when missing.
	fmt.Fprintln(w, p.Bold("\nCapabilities:"))
	for _, comp := range r.Diagnostics.Components {
		mark := p.Green("✓")
		if !comp.Available {
			mark = p.Red("✗")
		}
		fmt.Fprintf(w, "  %s %s — %s\n", mark, comp.Name, comp.Detail)
		if !comp.Available {
			if comp.Impact != "" {
				fmt.Fprintf(w, "      impact: %s\n", comp.Impact)
			}
			if comp.Install != "" {
				fmt.Fprintf(w, "      fix:    %s\n", indentLines(comp.Install, "              "))
			}
		}
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, p.Dim("Δ FOOTPRINT shows cpu / mem change if the rec is applied; green = savings, red = growth. Predictions are predicted, not confirmed."))
	fmt.Fprintln(w)
}

// inPlaceLine summarizes the four detection layers in one honest line.
func inPlaceLine(c model.ClusterStatus, p Palette) string {
	tier := fmt.Sprintf("%s (inferred from version)", c.InPlaceTier)
	enabled := p.Red("not confirmed enabled")
	if c.InPlaceConfirmedEnabled {
		enabled = p.Green("enabled (confirmed via pods/resize)")
	}
	inUse := "no in-use evidence"
	if c.InPlaceInUse {
		inUse = "in use (confirmed on pods)"
	}
	avail := p.Red("RESTART required to apply recs")
	if c.InPlaceAvailable() {
		avail = p.Green("in-place apply available")
	}
	return fmt.Sprintf("%s · %s · %s · %s", tier, enabled, inUse, avail)
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// indentLines prefixes every line after the first with pad, so multi-line
// install hints stay aligned under "fix:".
func indentLines(s, pad string) string {
	lines := strings.Split(s, "\n")
	for i := 1; i < len(lines); i++ {
		lines[i] = pad + lines[i]
	}
	return strings.Join(lines, "\n")
}
