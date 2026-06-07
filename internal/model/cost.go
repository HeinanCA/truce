package model

import "time"

// PriceSource names where a price came from, so output can label it honestly:
// on-demand is stable, spot is variable and must be dated, static is the user's
// own number, missing means no price could be resolved.
type PriceSource string

const (
	PriceAWSOnDemand PriceSource = "aws-ondemand"
	PriceAWSSpot     PriceSource = "aws-spot"
	PriceStatic      PriceSource = "static"
	PriceMissing     PriceSource = "missing"
)

// Variable reports whether a source's price moves over time (spot), so the
// renderer dates it.
func (s PriceSource) Variable() bool { return s == PriceAWSSpot }

// NodeHourly is one node's resolved hourly price with provenance. Missing is
// true when no backend could price the node's instance type.
type NodeHourly struct {
	USDPerHour float64
	Source     PriceSource
	AsOf       time.Time // when the price was observed (dated for spot); zero for stable
	Missing    bool
}

// NodePoolCost summarizes pricing and consolidation headroom for one node pool.
type NodePoolCost struct {
	Name          string
	InstanceTypes []string
	NodeCount     int
	SpotCount     int
	OnDemandCount int

	BlendedHourly float64     // USD/node-hr blended across the pool's priced nodes
	Source        PriceSource // representative source for the pool
	AsOf          time.Time   // newest observation behind the blend (for spot dating)

	PriceMissing bool     // at least one node's type could not be priced
	MissingTypes []string // the instance types that failed to price

	// Consolidation headroom from the aggregate rightsizing savings, attributed
	// proportionally to this pool by node count.
	NodesSavedLow  int
	NodesSavedHigh int
	MonthlyLow     float64
	MonthlyHigh    float64
}

// CostReport is the whole cost block. Enabled is false when no price could be
// resolved for any node (output then shows node/resource savings only and flags
// PRICE-MISSING).
type CostReport struct {
	Enabled bool
	Backend PriceSource // the auto-selected backend (aws-*, static, or missing)

	Pools []NodePoolCost

	TotalMonthlyLow  float64
	TotalMonthlyHigh float64

	SpotNodes     int
	OnDemandNodes int
	PriceMissing  bool // some node/type could not be priced

	// FreedCPUMilli / FreedMemBytes are the aggregate per-cluster resources the
	// recommendations free up (always available, even when Enabled is false).
	FreedCPUMilli  int64
	FreedMemBytes  int64
	NodesSavedLow  int
	NodesSavedHigh int

	Note string // honest caveat line (spot mix, consolidation assumption, ...)
}
