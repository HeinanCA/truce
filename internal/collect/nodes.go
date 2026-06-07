package collect

import (
	corev1 "k8s.io/api/core/v1"

	"github.com/heinanca/truce/internal/model"
)

// Label keys for pricing-relevant node metadata. Standard keys first, with
// legacy/provider fallbacks so older clusters and EKS managed node groups still
// resolve.
const (
	labelInstanceType       = "node.kubernetes.io/instance-type"
	labelInstanceTypeLegacy = "beta.kubernetes.io/instance-type"
	labelRegion             = "topology.kubernetes.io/region"
	labelRegionLegacy       = "failure-domain.beta.kubernetes.io/region"
	labelZone               = "topology.kubernetes.io/zone"
	labelZoneLegacy         = "failure-domain.beta.kubernetes.io/zone"
	labelCapacityKarpenter  = "karpenter.sh/capacity-type"
	labelCapacityEKS        = "eks.amazonaws.com/capacityType"
	labelNodePool           = "karpenter.sh/nodepool"
	labelNodePoolLegacy     = "karpenter.sh/provisioner-name"
)

// NodeInfos extracts the pricing-relevant view of every node. It only reads
// labels and allocatable capacity already fetched by Scan — no extra cluster
// calls — keeping truce read-only.
func NodeInfos(nodes []corev1.Node) []model.NodeInfo {
	out := make([]model.NodeInfo, 0, len(nodes))
	for i := range nodes {
		n := &nodes[i]
		info := model.NodeInfo{
			Name:         n.Name,
			InstanceType: firstLabel(n.Labels, labelInstanceType, labelInstanceTypeLegacy),
			Region:       firstLabel(n.Labels, labelRegion, labelRegionLegacy),
			Zone:         firstLabel(n.Labels, labelZone, labelZoneLegacy),
			Capacity:     capacityType(n.Labels),
			NodePool:     firstLabel(n.Labels, labelNodePool, labelNodePoolLegacy),
		}
		if q, ok := n.Status.Allocatable[corev1.ResourceCPU]; ok {
			info.AllocCPUMilli = q.MilliValue()
		}
		if q, ok := n.Status.Allocatable[corev1.ResourceMemory]; ok {
			info.AllocMemBytes = q.Value()
		}
		out = append(out, info)
	}
	return out
}

// firstLabel returns the first non-empty label value among keys.
func firstLabel(labels map[string]string, keys ...string) string {
	for _, k := range keys {
		if v := labels[k]; v != "" {
			return v
		}
	}
	return ""
}

// capacityType normalizes the spot/on-demand signal across Karpenter and EKS
// label conventions.
func capacityType(labels map[string]string) model.CapacityType {
	switch v := firstLabel(labels, labelCapacityKarpenter, labelCapacityEKS); v {
	case "spot", "SPOT":
		return model.CapacitySpot
	case "on-demand", "ON_DEMAND":
		return model.CapacityOnDemand
	default:
		return model.CapacityUnknown
	}
}
