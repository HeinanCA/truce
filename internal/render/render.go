package render

import (
	"fmt"
	"io"

	"github.com/heinanca/truce/internal/model"
	"github.com/heinanca/truce/internal/recommend"
)

// Report is everything the renderer needs: the cluster header context and the
// analyzed workloads.
type Report struct {
	Cluster     model.ClusterStatus
	Diagnostics model.Diagnostics
	Workloads   []model.WorkloadAnalysis

	// UsageBasisLabel describes the data behind the verdicts (e.g. the Prometheus
	// peak window, or the snapshot fallback) for the header.
	UsageBasisLabel string
	// SnapshotOnly is true when no time-series source was queried, so the header
	// warns that SAFE/SCALE verdicts may understate traffic spikes.
	SnapshotOnly bool

	// Cost is the estimated dollar impact of the recommendations. Cost.Enabled is
	// false when no node price could be resolved (PRICE-MISSING).
	Cost model.CostReport
}

// Options controls output format, color, ordering, and filtering.
type Options struct {
	Format       string // table | wide | json | diff
	NoColor      bool
	Sort         SortMode
	Only         []model.Verdict
	ProblemsOnly bool

	// Rec carries the sizing config for the recommend table.
	Rec recommend.Config
}

// Render writes the report to w in the requested format. Filtering and sorting
// are applied uniformly first, so every format reflects the same row selection.
func Render(w io.Writer, r Report, opts Options) error {
	rows := Sort(Filter(r.Workloads, opts.Only, opts.ProblemsOnly), opts.Sort)
	p := NewPalette(opts.NoColor)

	switch opts.Format {
	case "json":
		return renderJSON(w, r, rows)
	case "diff":
		renderHeader(w, r, p)
		return renderDiff(w, rows, opts.Rec, p)
	case "advice":
		renderHeader(w, r, p)
		if err := renderAdvice(w, r, rows, p); err != nil {
			return err
		}
		renderCost(w, r.Cost, p)
		return nil
	case "recommend":
		renderHeader(w, r, p)
		if err := renderRecommendTable(w, r, rows, opts.Rec, p); err != nil {
			return err
		}
		renderCost(w, r.Cost, p)
		return nil
	case "wide":
		renderHeader(w, r, p)
		if err := renderTable(w, rows, p, true); err != nil {
			return err
		}
		renderCost(w, r.Cost, p)
		return nil
	case "table", "":
		renderHeader(w, r, p)
		if err := renderTable(w, rows, p, false); err != nil {
			return err
		}
		renderCost(w, r.Cost, p)
		return nil
	default:
		return fmt.Errorf("unknown output format %q (want advice|recommend|table|wide|json|diff)", opts.Format)
	}
}
