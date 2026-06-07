package model

// CapacityType is a node's purchasing model, read from the
// karpenter.sh/capacity-type label (or equivalents).
type CapacityType string

const (
	CapacityOnDemand CapacityType = "on-demand"
	CapacitySpot     CapacityType = "spot"
	CapacityUnknown  CapacityType = "unknown"
)

// NodeInfo is the pricing-relevant view of a node, extracted from its labels and
// allocatable capacity. It is pure (no Kubernetes types) so the cost engine can
// price and group nodes without a cluster dependency.
type NodeInfo struct {
	Name         string
	InstanceType string       // node.kubernetes.io/instance-type
	Region       string       // topology.kubernetes.io/region
	Zone         string       // topology.kubernetes.io/zone
	Capacity     CapacityType // karpenter.sh/capacity-type
	NodePool     string       // karpenter.sh/nodepool (or "" → grouped by instance type)

	AllocCPUMilli int64 // allocatable CPU, milli-cores
	AllocMemBytes int64 // allocatable memory, bytes
}

// PoolKey groups nodes for the cost block by their real instance type — the
// actual node shape, and what determines price. A single Karpenter NodePool
// (e.g. "default") launches many heterogeneous types, so grouping by NodePool
// name would collapse them all into one meaningless row; grouping by instance
// type shows the real fleet. NodePool is the fallback only when the type label
// is missing.
func (n NodeInfo) PoolKey() string {
	if n.InstanceType != "" {
		return n.InstanceType
	}
	if n.NodePool != "" {
		return n.NodePool
	}
	return "unknown"
}
