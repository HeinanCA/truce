package detect

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/heinanca/truce/internal/model"
)

func TestInferTier(t *testing.T) {
	tests := []struct {
		version string
		want    model.InPlaceTier
	}{
		{"v1.26.5", model.InPlaceNone},
		{"v1.27.0", model.InPlaceAlpha},
		{"v1.32.3", model.InPlaceAlpha},
		{"v1.33.0", model.InPlaceBeta},
		{"v1.34.1-gke.100", model.InPlaceBeta},
		{"v1.35.0", model.InPlaceGA},
		{"v1.40.0", model.InPlaceGA},
		{"garbage", model.InPlaceNone},
		{"", model.InPlaceNone},
	}
	for _, tt := range tests {
		if got := InferTier(tt.version); got != tt.want {
			t.Errorf("InferTier(%q) = %q, want %q", tt.version, got, tt.want)
		}
	}
}

func node(name, kubelet, runtime string) corev1.Node {
	n := corev1.Node{}
	n.Name = name
	n.Status.NodeInfo.KubeletVersion = kubelet
	n.Status.NodeInfo.ContainerRuntimeVersion = runtime
	return n
}

func TestNodesNotReady(t *testing.T) {
	nodes := []corev1.Node{
		node("ok-1", "v1.34.0", "containerd://1.7.0"),
		node("old-1", "v1.31.0", "containerd://1.6.0"),
		node("ok-2", "v1.33.0", "containerd://1.7.2"),
		node("bad-ver", "weird", "cri-o://1.30"),
	}
	got := nodesNotReady(nodes)
	if len(got) != 2 {
		t.Fatalf("nodesNotReady len = %d (%v), want 2", len(got), got)
	}
	// old-1 (1.31 < 33) and bad-ver (unparseable) should be flagged.
	if got[0][:5] != "old-1" || got[1][:7] != "bad-ver" {
		t.Errorf("flagged nodes = %v, want old-1 and bad-ver", got)
	}
}

func TestPodsShowResize(t *testing.T) {
	none := []corev1.Pod{{}}
	if podsShowResize(none) {
		t.Error("empty pod should show no resize evidence")
	}

	withAlloc := corev1.Pod{}
	withAlloc.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name:               "app",
		AllocatedResources: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m")},
	}}
	if !podsShowResize([]corev1.Pod{withAlloc}) {
		t.Error("pod with allocated resources should show resize evidence")
	}

	withCond := corev1.Pod{}
	withCond.Status.Conditions = []corev1.PodCondition{{Type: "PodResizePending"}}
	if !podsShowResize([]corev1.Pod{withCond}) {
		t.Error("pod with PodResize condition should show resize evidence")
	}

	withResize := corev1.Pod{}
	withResize.Status.Resize = corev1.PodResizeStatusInProgress
	if !podsShowResize([]corev1.Pod{withResize}) {
		t.Error("pod with deprecated resize status should show resize evidence")
	}
}

func TestInPlaceAvailable(t *testing.T) {
	// Confirmed enabled, all nodes ready -> available.
	s := InPlace("v1.35.0", true,
		[]corev1.Node{node("n", "v1.35.0", "containerd://2.0")}, nil)
	if !s.Available() {
		t.Errorf("expected available; got %+v", s)
	}
	// Enabled but a stale node -> not available.
	s2 := InPlace("v1.35.0", true,
		[]corev1.Node{node("n", "v1.30.0", "containerd://1.5")}, nil)
	if s2.Available() {
		t.Errorf("stale node should make in-place unavailable; got %+v", s2)
	}
	// Subresource absent -> not available regardless of version.
	s3 := InPlace("v1.35.0", false,
		[]corev1.Node{node("n", "v1.35.0", "containerd://2.0")}, nil)
	if s3.Available() {
		t.Errorf("absent subresource should make in-place unavailable; got %+v", s3)
	}
}

func TestGitOps(t *testing.T) {
	tests := []struct {
		name string
		anno map[string]string
		want bool
		tool string
	}{
		{"none", nil, false, ""},
		{"argo-tracking", map[string]string{annoArgoTrackingID: "app:ns/Deploy"}, true, "Argo CD"},
		{"flux", map[string]string{annoFluxName: "my-app"}, true, "Flux"},
		{"argo-instance", map[string]string{annoArgoInstance: "my-app"}, true, "Argo CD/other"},
		{"unrelated", map[string]string{"foo": "bar"}, false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, tool := GitOps(tt.anno)
			if got != tt.want || tool != tt.tool {
				t.Errorf("GitOps() = (%v, %q), want (%v, %q)", got, tool, tt.want, tt.tool)
			}
		})
	}
}
