// Package detect derives capability signals from collected cluster data: the
// in-place pod-resize status (layered and honestly labeled inferred vs
// confirmed) and GitOps ownership. It reads Kubernetes structs the collect layer
// already fetched but makes no cluster calls of its own.
package detect

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"

	"github.com/heinanca/truce/internal/model"
)

// inPlaceReadyMinor is the k8s minor version at which in-place resize is on by
// default (1.33, beta). A node whose kubelet trails this may restart the pod on
// resize even when the API server advertises the feature, so it is flagged as
// not ready. This is a readiness heuristic, surfaced as such — not a guarantee.
const inPlaceReadyMinor = 33

// InPlaceStatus is the layered in-place-resize finding. Tier is inferred from
// the server version; ConfirmedEnabled and InUse carry evidence.
type InPlaceStatus struct {
	Tier             model.InPlaceTier
	ConfirmedEnabled bool     // pods/resize subresource served
	InUse            bool     // a pod shows allocated resources or a resize condition
	NodesNotReady    []string // nodes whose kubelet trails inPlaceReadyMinor
}

// Available reports whether in-place resize can be relied upon: confirmed
// enabled by discovery, with every node ready. When false, applying a
// recommendation restarts the pod.
func (s InPlaceStatus) Available() bool {
	return s.ConfirmedEnabled && len(s.NodesNotReady) == 0
}

// InPlace assembles the in-place status from the server version (tier,
// inferred), the pods/resize discovery result (confirmed enabled), node
// info (readiness), and pod status (confirmed in-use).
func InPlace(serverVersion string, resizeSubresource bool, nodes []corev1.Node, pods []corev1.Pod) InPlaceStatus {
	s := InPlaceStatus{
		Tier:             InferTier(serverVersion),
		ConfirmedEnabled: resizeSubresource,
		InUse:            podsShowResize(pods),
		NodesNotReady:    nodesNotReady(nodes),
	}
	return s
}

var versionRE = regexp.MustCompile(`^v?(\d+)\.(\d+)`)

// parseMajorMinor extracts the major and minor version from a GitVersion string
// such as "v1.34.2" or "v1.33.0-gke.100".
func parseMajorMinor(gitVersion string) (major, minor int, ok bool) {
	m := versionRE.FindStringSubmatch(strings.TrimSpace(gitVersion))
	if m == nil {
		return 0, 0, false
	}
	major, _ = strconv.Atoi(m[1])
	minor, _ = strconv.Atoi(m[2])
	return major, minor, true
}

// InferTier maps a server version to the in-place-resize capability tier. This
// is an inference from the version alone; it does not confirm the feature is
// enabled (see InPlaceStatus.ConfirmedEnabled).
func InferTier(serverVersion string) model.InPlaceTier {
	major, minor, ok := parseMajorMinor(serverVersion)
	if !ok {
		return model.InPlaceNone
	}
	if major > 1 {
		return model.InPlaceGA
	}
	if major < 1 {
		return model.InPlaceNone
	}
	switch {
	case minor < 27:
		return model.InPlaceNone
	case minor <= 32:
		return model.InPlaceAlpha
	case minor <= 34:
		return model.InPlaceBeta
	default:
		return model.InPlaceGA
	}
}

// nodesNotReady returns names of nodes whose kubelet minor version trails the
// in-place-ready threshold, annotated with the observed kubelet and runtime.
func nodesNotReady(nodes []corev1.Node) []string {
	var out []string
	for _, n := range nodes {
		kubelet := n.Status.NodeInfo.KubeletVersion
		_, minor, ok := parseMajorMinor(kubelet)
		if !ok || minor < inPlaceReadyMinor {
			out = append(out, fmt.Sprintf("%s (kubelet %s, runtime %s)",
				n.Name, kubelet, n.Status.NodeInfo.ContainerRuntimeVersion))
		}
	}
	return out
}

// podsShowResize reports whether any pod carries evidence the in-place feature
// is exercised: allocated resources set by the kubelet, a non-empty deprecated
// resize status, or a PodResize* condition.
func podsShowResize(pods []corev1.Pod) bool {
	for i := range pods {
		p := &pods[i]
		if p.Status.Resize != "" {
			return true
		}
		for _, cs := range p.Status.ContainerStatuses {
			if len(cs.AllocatedResources) > 0 {
				return true
			}
		}
		for _, c := range p.Status.Conditions {
			if strings.HasPrefix(string(c.Type), "PodResize") {
				return true
			}
		}
	}
	return false
}
