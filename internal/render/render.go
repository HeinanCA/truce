package render

import (
	"fmt"
	"io"

	"github.com/heinanca/truce/internal/model"
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
}

// Options controls output format, color, ordering, and filtering.
type Options struct {
	Format       string // table | wide | json | diff
	NoColor      bool
	Sort         SortMode
	Only         []model.Verdict
	ProblemsOnly bool
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
		return renderDiff(w, rows, p)
	case "wide":
		renderHeader(w, r, p)
		return renderTable(w, rows, p, true)
	case "table", "":
		renderHeader(w, r, p)
		return renderTable(w, rows, p, false)
	default:
		return fmt.Errorf("unknown output format %q (want table|wide|json|diff)", opts.Format)
	}
}
