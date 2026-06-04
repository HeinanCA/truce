package cost

import (
	"context"
	"fmt"
	"os"

	"sigs.k8s.io/yaml"

	"github.com/heinanca/truce/internal/model"
)

// staticProvider prices nodes from a user-supplied map of instanceType -> USD/hr
// (--pricing-file) or a single flat rate (--node-cost). It is the path for
// non-AWS users, who would otherwise have no built-in price source.
type staticProvider struct {
	perType  map[string]float64 // instanceType -> USD/node-hr
	flatRate float64            // USD/node-hr when no per-type entry matches
}

// staticFile is the on-disk schema for --pricing-file. Either form is accepted:
//
//	prices:
//	  m5.large: 0.096
//	  m5.xlarge: 0.192
//	default: 0.10
type staticFile struct {
	Prices  map[string]float64 `json:"prices"`
	Default float64            `json:"default"`
}

func newStaticProvider(cfg Config) (*staticProvider, error) {
	p := &staticProvider{perType: map[string]float64{}, flatRate: cfg.NodeCost}
	if cfg.PricingFile != "" {
		data, err := os.ReadFile(cfg.PricingFile)
		if err != nil {
			return nil, fmt.Errorf("reading --pricing-file: %w", err)
		}
		var f staticFile
		if err := yaml.Unmarshal(data, &f); err != nil {
			return nil, fmt.Errorf("parsing --pricing-file %s: %w", cfg.PricingFile, err)
		}
		for k, v := range f.Prices {
			p.perType[k] = v
		}
		if p.flatRate == 0 {
			p.flatRate = f.Default
		}
	}
	if len(p.perType) == 0 && p.flatRate == 0 {
		return nil, fmt.Errorf("static pricing requested but no prices found (set --node-cost or a non-empty --pricing-file)")
	}
	return p, nil
}

func (p *staticProvider) NodeHourly(_ context.Context, node model.NodeInfo) model.NodeHourly {
	if v, ok := p.perType[node.InstanceType]; ok {
		return model.NodeHourly{USDPerHour: v, Source: model.PriceStatic}
	}
	if p.flatRate > 0 {
		return model.NodeHourly{USDPerHour: p.flatRate, Source: model.PriceStatic}
	}
	return model.NodeHourly{Source: model.PriceMissing, Missing: true}
}

func (p *staticProvider) Backend() model.PriceSource { return model.PriceStatic }
