package cost

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/heinanca/truce/internal/model"
)

// HoursPerMonth is the standard 730-hour month used to annualize hourly prices.
const HoursPerMonth = 730.0

// PricedNode pairs a node with its resolved price.
type PricedNode struct {
	Node  model.NodeInfo
	Price model.NodeHourly
}

// PriceNodes resolves every node's price through the provider. This is the only
// I/O step; Estimate below is pure.
func PriceNodes(ctx context.Context, provider PriceProvider, nodes []model.NodeInfo) []PricedNode {
	out := make([]PricedNode, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, PricedNode{Node: n, Price: provider.NodeHourly(ctx, n)})
	}
	return out
}

// Estimate builds the cost report from priced nodes and the aggregate freed
// resources (positive milli-cores / bytes the recommendations give back). It is
// pure and deterministic. freedCPU/freedMem drive the consolidation headroom;
// the prices drive the dollar figure. When no node could be priced, Enabled is
// false but the node/resource savings are still reported.
func Estimate(priced []PricedNode, backend model.PriceSource, freedCPU, freedMem int64, now time.Time) model.CostReport {
	r := model.CostReport{
		Backend:       backend,
		FreedCPUMilli: freedCPU,
		FreedMemBytes: freedMem,
	}

	// Consolidation headroom: how many nodes the freed resources could retire.
	// Bounded by the dimension that frees the FEWEST nodes (you can only remove a
	// node when both its CPU and memory are freed elsewhere); the optimistic end
	// assumes perfect bin-packing on the looser dimension.
	avgCPU, avgMem := avgAlloc(priced)
	low, high := nodesSaved(freedCPU, freedMem, avgCPU, avgMem, len(priced))
	r.NodesSavedLow, r.NodesSavedHigh = low, high

	// Group into pools and price each.
	pools := map[string]*model.NodePoolCost{}
	var order []string
	var clusterUSDSum float64
	var clusterPriced int

	for _, pn := range priced {
		switch pn.Node.Capacity {
		case model.CapacitySpot:
			r.SpotNodes++
		case model.CapacityOnDemand:
			r.OnDemandNodes++
		}

		key := pn.Node.PoolKey()
		pc, ok := pools[key]
		if !ok {
			pc = &model.NodePoolCost{Name: key}
			pools[key] = pc
			order = append(order, key)
		}
		pc.NodeCount++
		switch pn.Node.Capacity {
		case model.CapacitySpot:
			pc.SpotCount++
		case model.CapacityOnDemand:
			pc.OnDemandCount++
		}
		addType(pc, pn.Node.InstanceType)

		if pn.Price.Missing {
			r.PriceMissing = true
			pc.PriceMissing = true
			addMissingType(pc, pn.Node.InstanceType)
			continue
		}
		// Accumulate blended price.
		pc.BlendedHourly += pn.Price.USDPerHour
		clusterUSDSum += pn.Price.USDPerHour
		clusterPriced++
		if pn.Price.Source == model.PriceAWSSpot {
			pc.Source = model.PriceAWSSpot
		} else if pc.Source == "" {
			pc.Source = pn.Price.Source
		}
		if pn.Price.AsOf.After(pc.AsOf) {
			pc.AsOf = pn.Price.AsOf
		}
	}

	r.Enabled = clusterPriced > 0
	clusterBlended := 0.0
	if clusterPriced > 0 {
		clusterBlended = clusterUSDSum / float64(clusterPriced)
	}

	total := len(priced)
	for _, key := range order {
		pc := pools[key]
		// Finish the per-pool blended average over its priced nodes.
		if n := pricedNodeCount(pc, priced); n > 0 {
			pc.BlendedHourly /= float64(n)
		}
		sort.Strings(pc.InstanceTypes)
		sort.Strings(pc.MissingTypes)

		// Attribute the cluster's saved-node range to this pool by node share, and
		// value it at the pool's blended rate.
		share := 0.0
		if total > 0 {
			share = float64(pc.NodeCount) / float64(total)
		}
		poolLow := int(math.Round(float64(low) * share))
		poolHigh := int(math.Round(float64(high) * share))
		pc.NodesSavedLow, pc.NodesSavedHigh = poolLow, poolHigh
		pc.MonthlyLow = float64(poolLow) * pc.BlendedHourly * HoursPerMonth
		pc.MonthlyHigh = float64(poolHigh) * pc.BlendedHourly * HoursPerMonth

		r.Pools = append(r.Pools, *pc)
		r.TotalMonthlyLow += pc.MonthlyLow
		r.TotalMonthlyHigh += pc.MonthlyHigh
	}

	// When pools rounded to zero but the cluster has headroom, fall back to a
	// cluster-blended figure so the bottom line isn't misleadingly $0.
	if r.Enabled && r.TotalMonthlyHigh == 0 && high > 0 {
		r.TotalMonthlyLow = float64(low) * clusterBlended * HoursPerMonth
		r.TotalMonthlyHigh = float64(high) * clusterBlended * HoursPerMonth
	}

	r.Note = note(r, now)
	return r
}

// avgAlloc returns the mean allocatable CPU (milli) and memory (bytes) across
// nodes that report each dimension.
func avgAlloc(priced []PricedNode) (cpu, mem float64) {
	var cpuSum, memSum float64
	var cpuN, memN int
	for _, pn := range priced {
		if pn.Node.AllocCPUMilli > 0 {
			cpuSum += float64(pn.Node.AllocCPUMilli)
			cpuN++
		}
		if pn.Node.AllocMemBytes > 0 {
			memSum += float64(pn.Node.AllocMemBytes)
			memN++
		}
	}
	if cpuN > 0 {
		cpu = cpuSum / float64(cpuN)
	}
	if memN > 0 {
		mem = memSum / float64(memN)
	}
	return cpu, mem
}

// nodesSaved computes the low/high consolidation range from freed resources.
func nodesSaved(freedCPU, freedMem int64, avgCPU, avgMem float64, total int) (low, high int) {
	var ratios []float64
	if avgCPU > 0 && freedCPU > 0 {
		ratios = append(ratios, float64(freedCPU)/avgCPU)
	}
	if avgMem > 0 && freedMem > 0 {
		ratios = append(ratios, float64(freedMem)/avgMem)
	}
	if len(ratios) == 0 {
		return 0, 0
	}
	lo, hi := ratios[0], ratios[0]
	for _, x := range ratios[1:] {
		lo = math.Min(lo, x)
		hi = math.Max(hi, x)
	}
	low = int(math.Floor(lo))
	high = int(math.Floor(hi))
	if total > 0 {
		if low > total {
			low = total
		}
		if high > total {
			high = total
		}
	}
	if low < 0 {
		low = 0
	}
	if high < low {
		high = low
	}
	return low, high
}

// note builds the honest caveat line behind the dollar figure.
func note(r model.CostReport, now time.Time) string {
	if !r.Enabled {
		return "no node prices resolved — showing node/resource savings only (PRICE-MISSING). " +
			"Provide --pricing-file or --node-cost, or grant pricing:GetProducts / ec2:DescribeSpotPriceHistory."
	}
	var parts []string
	switch {
	case r.SpotNodes > 0 && r.OnDemandNodes > 0:
		parts = append(parts, fmt.Sprintf("blended across %d spot + %d on-demand nodes", r.SpotNodes, r.OnDemandNodes))
	case r.SpotNodes > 0:
		parts = append(parts, fmt.Sprintf("%d spot nodes", r.SpotNodes))
	}
	if r.SpotNodes > 0 {
		parts = append(parts, "spot is variable and per-AZ, priced at current ("+now.Format("2006-01-02")+")")
	}
	if r.PriceMissing {
		parts = append(parts, "some instance types could not be priced (PRICE-MISSING) and are excluded")
	}
	parts = append(parts, "assumes consolidation of freed capacity")
	return strings.Join(parts, "; ")
}

func addType(pc *model.NodePoolCost, t string) {
	if t == "" {
		return
	}
	for _, x := range pc.InstanceTypes {
		if x == t {
			return
		}
	}
	pc.InstanceTypes = append(pc.InstanceTypes, t)
}

func addMissingType(pc *model.NodePoolCost, t string) {
	if t == "" {
		t = "unknown"
	}
	for _, x := range pc.MissingTypes {
		if x == t {
			return
		}
	}
	pc.MissingTypes = append(pc.MissingTypes, t)
}

// pricedNodeCount counts a pool's nodes that resolved a price (for the blend).
func pricedNodeCount(pc *model.NodePoolCost, priced []PricedNode) int {
	n := 0
	for _, pn := range priced {
		if pn.Node.PoolKey() == pc.Name && !pn.Price.Missing {
			n++
		}
	}
	return n
}
