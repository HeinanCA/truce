// Package cost prices cluster nodes so truce can translate rightsizing savings
// into a dollar figure. Pricing is built in and provider-pluggable:
//
//   - AWS nodes + resolvable AWS credentials  -> the aws backend (default),
//     which reads the EC2 Price List API for on-demand and the spot price
//     history for spot, with an on-disk cache.
//   - else a static backend driven by --pricing-file / --node-cost.
//   - else no price: node and resource savings are still reported and the run is
//     flagged PRICE-MISSING.
//
// The pricing core (grouping, blending, consolidation math) is pure and unit
// tested; only the aws backend performs I/O, and only against read-only AWS
// APIs (pricing:GetProducts, ec2:DescribeSpotPriceHistory).
package cost

import (
	"context"
	"regexp"
	"time"

	"github.com/heinanca/truce/internal/model"
)

// PriceProvider resolves a node's hourly price. Implementations must be safe to
// call once per node; the aws backend caches so repeated types don't refetch.
type PriceProvider interface {
	// NodeHourly returns the node's price with provenance. It returns a result
	// with Missing=true (never a fatal error) when this provider cannot price the
	// node, so one un-priceable type never fails the whole run.
	NodeHourly(ctx context.Context, node model.NodeInfo) model.NodeHourly
	// Backend names the selected backend for honest labeling.
	Backend() model.PriceSource
}

// Config carries the pricing flags and tunables.
type Config struct {
	PricingFile string        // path to a static instanceType->USD/hr map (YAML/JSON)
	NodeCost    float64       // flat USD/node-hr static fallback
	CacheDir    string        // on-disk cache directory for AWS lookups
	CacheTTL    time.Duration // cache freshness (default 24h)
	DisableAWS  bool          // force the static/missing path (tests, air-gapped)
	Now         time.Time     // reference time for dating spot prices (injected)
}

func (c Config) ttl() time.Duration {
	if c.CacheTTL > 0 {
		return c.CacheTTL
	}
	return 24 * time.Hour
}

func (c Config) now() time.Time {
	if !c.Now.IsZero() {
		return c.Now
	}
	return time.Now()
}

// SelectProvider auto-selects the backend per the rules above. It never returns
// nil: with no AWS and no static config it returns the missing provider so the
// caller still gets node/resource savings and a PRICE-MISSING flag.
func SelectProvider(ctx context.Context, nodes []model.NodeInfo, cfg Config) PriceProvider {
	if !cfg.DisableAWS && looksAWS(nodes) {
		if p, err := newAWSProvider(ctx, cfg); err == nil {
			return p
		}
		// Credentials/config did not resolve — fall through to static/missing.
	}
	if cfg.PricingFile != "" || cfg.NodeCost > 0 {
		if p, err := newStaticProvider(cfg); err == nil {
			return p
		}
	}
	return missingProvider{}
}

// awsRegion matches an AWS region code (e.g. us-east-1, eu-central-1), used to
// decide whether the nodes look like AWS before attempting the aws backend.
var awsRegion = regexp.MustCompile(`^[a-z]{2}(-gov)?-[a-z]+-\d$`)

// looksAWS reports whether any node carries an AWS-shaped region and an instance
// type — the precondition for trying the aws backend.
func looksAWS(nodes []model.NodeInfo) bool {
	for _, n := range nodes {
		if n.InstanceType != "" && awsRegion.MatchString(n.Region) {
			return true
		}
	}
	return false
}

// missingProvider prices nothing; every node comes back Missing.
type missingProvider struct{}

func (missingProvider) NodeHourly(context.Context, model.NodeInfo) model.NodeHourly {
	return model.NodeHourly{Source: model.PriceMissing, Missing: true}
}
func (missingProvider) Backend() model.PriceSource { return model.PriceMissing }
