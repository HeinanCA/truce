package collect

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/heinanca/truce/internal/model"
)

func TestNodeInfos(t *testing.T) {
	nodes := []corev1.Node{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "ip-10-0-1-5",
				Labels: map[string]string{
					labelInstanceType:      "m5.large",
					labelRegion:            "us-east-1",
					labelZone:              "us-east-1a",
					labelCapacityKarpenter: "spot",
					labelNodePool:          "default",
				},
			},
			Status: corev1.NodeStatus{Allocatable: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("2"),
				corev1.ResourceMemory: resource.MustParse("8Gi"),
			}},
		},
		{
			// EKS-style capacity label + legacy region/zone; no NodePool → grouped by type.
			ObjectMeta: metav1.ObjectMeta{
				Name: "ip-10-0-2-9",
				Labels: map[string]string{
					labelInstanceTypeLegacy: "c5.xlarge",
					labelRegionLegacy:       "eu-central-1",
					labelZoneLegacy:         "eu-central-1b",
					labelCapacityEKS:        "ON_DEMAND",
				},
			},
		},
	}

	got := NodeInfos(nodes)
	if len(got) != 2 {
		t.Fatalf("want 2, got %d", len(got))
	}

	a := got[0]
	if a.InstanceType != "m5.large" || a.Region != "us-east-1" || a.Zone != "us-east-1a" {
		t.Errorf("node a topology wrong: %+v", a)
	}
	if a.Capacity != model.CapacitySpot {
		t.Errorf("node a capacity = %q, want spot", a.Capacity)
	}
	if a.NodePool != "default" || a.PoolKey() != "default" {
		t.Errorf("node a pool = %q", a.NodePool)
	}
	if a.AllocCPUMilli != 2000 || a.AllocMemBytes != 8*1024*1024*1024 {
		t.Errorf("node a alloc = %d/%d", a.AllocCPUMilli, a.AllocMemBytes)
	}

	b := got[1]
	if b.InstanceType != "c5.xlarge" || b.Region != "eu-central-1" {
		t.Errorf("node b legacy labels wrong: %+v", b)
	}
	if b.Capacity != model.CapacityOnDemand {
		t.Errorf("node b capacity = %q, want on-demand", b.Capacity)
	}
	if b.PoolKey() != "c5.xlarge" {
		t.Errorf("node b PoolKey = %q, want instance type fallback", b.PoolKey())
	}
}
