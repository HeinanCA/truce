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

// PoolKey groups nodes into a NodePool for the cost block: the Karpenter
// NodePool when labeled, otherwise the instance type so even un-pooled clusters
// get a meaningful grouping.
func (n NodeInfo) PoolKey() string {
	if n.NodePool != "" {
		return n.NodePool
	}
	if n.InstanceType != "" {
		return n.InstanceType
	}
	return "unknown"
}
