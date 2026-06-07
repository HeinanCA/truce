package render

import (
	"encoding/json"
	"io"

	"github.com/heinanca/truce/internal/model"
)

// jsonReport is the JSON envelope: the full analyzed model, so machine
// consumers (CI, dashboards) get every field truce computed.
type jsonReport struct {
	Cluster     model.ClusterStatus      `json:"cluster"`
	Diagnostics model.Diagnostics        `json:"diagnostics"`
	Workloads   []model.WorkloadAnalysis `json:"workloads"`
	Cost        model.CostReport         `json:"cost"`
}

// renderJSON emits the filtered report as indented JSON.
func renderJSON(w io.Writer, r Report, rows []model.WorkloadAnalysis) error {
	out := jsonReport{
		Cluster:     r.Cluster,
		Diagnostics: r.Diagnostics,
		Workloads:   rows,
		Cost:        r.Cost,
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}
