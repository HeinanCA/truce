package render

import (
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/heinanca/truce/internal/model"
)

// renderCost prints the cost block: a headline cluster figure, the honest
// caveat, the consolidation headroom, and a per-NodePool breakdown. When no
// price could be resolved it still reports node/resource savings and flags
// PRICE-MISSING — the dollar figure is the only thing that disappears.
func renderCost(w io.Writer, c model.CostReport, p Palette) {
	// Nothing to say if there are no nodes and no savings at all.
	if len(c.Pools) == 0 && c.NodesSavedHigh == 0 && c.FreedCPUMilli == 0 && c.FreedMemBytes == 0 {
		return
	}

	fmt.Fprintln(w, p.Bold("\nCOST (estimated)"))

	if !c.Enabled {
		fmt.Fprintln(w, p.Yellow("  PRICE-MISSING — no node prices resolved; showing resource/node savings only."))
		fmt.Fprintf(w, "  Freed if applied: %s CPU + %s memory.\n", cpuStr(c.FreedCPUMilli), memStr(c.FreedMemBytes))
		if c.NodesSavedHigh > 0 {
			fmt.Fprintf(w, "  Could retire %s of %d nodes (consolidation).\n", savedRange(c.NodesSavedLow, c.NodesSavedHigh), totalNodes(c))
		}
		fmt.Fprintln(w, p.Dim("  "+c.Note))
		return
	}

	// Headline: the optimistic end, explicitly framed as an estimate.
	fmt.Fprintln(w, p.Bold(fmt.Sprintf("  up to %s/month", money(c.TotalMonthlyHigh)))+
		p.Dim("  (estimated, assumes consolidation; spot priced at current)"))
	if c.TotalMonthlyLow != c.TotalMonthlyHigh {
		fmt.Fprintf(w, "  range %s–%s/month\n", money(c.TotalMonthlyLow), money(c.TotalMonthlyHigh))
	}
	fmt.Fprintf(w, "  Freed: %s CPU + %s memory  →  retire %s of %d nodes\n",
		cpuStr(c.FreedCPUMilli), memStr(c.FreedMemBytes), savedRange(c.NodesSavedLow, c.NodesSavedHigh), totalNodes(c))
	fmt.Fprintln(w, p.Dim("  "+c.Note))

	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "  NODEPOOL\tNODES\tMIX\t$/NODE-HR\tSAVE/MONTH")
	for _, pool := range c.Pools {
		fmt.Fprintf(tw, "  %s\t%d\t%s\t%s\t%s\n",
			pool.Name, pool.NodeCount, mixCol(pool), nodeHrCol(pool, p), monthCol(pool))
	}
	if err := tw.Flush(); err != nil {
		return
	}
}

// mixCol renders the spot/on-demand capacity mix for a pool.
func mixCol(pool model.NodePoolCost) string {
	switch {
	case pool.SpotCount > 0 && pool.OnDemandCount > 0:
		return fmt.Sprintf("%d spot/%d od", pool.SpotCount, pool.OnDemandCount)
	case pool.SpotCount > 0:
		return fmt.Sprintf("%d spot", pool.SpotCount)
	case pool.OnDemandCount > 0:
		return fmt.Sprintf("%d on-demand", pool.OnDemandCount)
	default:
		return "—"
	}
}

// nodeHrCol renders the blended hourly rate with its source.
func nodeHrCol(pool model.NodePoolCost, p Palette) string {
	if pool.PriceMissing && pool.BlendedHourly == 0 {
		return p.Yellow("PRICE-MISSING")
	}
	src := sourceLabel(pool.Source)
	cell := fmt.Sprintf("$%.4f (%s)", pool.BlendedHourly, src)
	if pool.PriceMissing {
		cell += p.Yellow(" *partial")
	}
	return cell
}

// monthCol renders the per-pool monthly savings range.
func monthCol(pool model.NodePoolCost) string {
	if pool.MonthlyHigh == 0 {
		return "—"
	}
	if pool.MonthlyLow == pool.MonthlyHigh {
		return money(pool.MonthlyHigh)
	}
	return money(pool.MonthlyLow) + "–" + money(pool.MonthlyHigh)
}

func sourceLabel(s model.PriceSource) string {
	switch s {
	case model.PriceAWSOnDemand:
		return "on-demand"
	case model.PriceAWSSpot:
		return "spot"
	case model.PriceStatic:
		return "static"
	default:
		return string(s)
	}
}

// money formats a USD figure: whole dollars over $100, two decimals under.
func money(v float64) string {
	if v >= 100 {
		return fmt.Sprintf("$%.0f", v)
	}
	return fmt.Sprintf("$%.2f", v)
}

func savedRange(low, high int) string {
	if low == high {
		return fmt.Sprintf("%d", high)
	}
	return fmt.Sprintf("%d–%d", low, high)
}

func totalNodes(c model.CostReport) int {
	n := 0
	for _, pool := range c.Pools {
		n += pool.NodeCount
	}
	return n
}
