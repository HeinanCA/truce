package cost

import (
	"context"
	"time"

	"github.com/heinanca/truce/internal/model"
)

//go:generate go run ./gen

// listProvider prices nodes from the built-in AWS public on-demand list price: a
// us-east-1 baseline per instance type, scaled by a per-region multiplier. The data
// is generated at build time (see gen/main.go) and embedded as Go source in
// listprice_data.go, so this backend needs no network or credentials. It is the
// last-resort fallback behind the live AWS and static backends.
//
// It prices on-demand list rates only — never spot (variable, per-AZ) and never
// account-specific rates. Prices are stable but dated (listPriceGeneratedOn) so the
// renderer can flag them as offline estimates.
type listProvider struct {
	perType     map[string]float64 // instanceType -> us-east-1 USD/node-hr
	regionMult  map[string]float64 // region -> multiplier relative to us-east-1
	generatedOn time.Time          // when the embedded table was generated
}

// newListProvider builds the provider from the embedded generated data.
func newListProvider() *listProvider {
	on, _ := time.Parse("2006-01", listPriceGeneratedOn)
	return &listProvider{
		perType:     listPriceUSEast1,
		regionMult:  listRegionMultiplier,
		generatedOn: on,
	}
}

// NodeHourly resolves the offline list price for a node. An instance type absent
// from the table returns Missing (one un-priceable type never fails the run). An
// unknown or empty region falls back to the us-east-1 baseline (multiplier 1.0).
func (p *listProvider) NodeHourly(_ context.Context, node model.NodeInfo) model.NodeHourly {
	base, ok := p.perType[node.InstanceType]
	if !ok || base <= 0 {
		return missing()
	}
	mult := 1.0
	if m, ok := p.regionMult[node.Region]; ok && m > 0 {
		mult = m
	}
	return model.NodeHourly{
		USDPerHour: base * mult,
		Source:     model.PriceListOffline,
		AsOf:       p.generatedOn,
	}
}

func (p *listProvider) Backend() model.PriceSource { return model.PriceListOffline }
