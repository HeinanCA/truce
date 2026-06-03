package model

// InPlaceTier is the in-place pod resize capability inferred from the server
// version alone. It is an inference, not a confirmation: ConfirmedEnabled and
// InUse on ClusterStatus carry the evidence-backed signals.
type InPlaceTier string

const (
	// InPlaceNone: server < 1.27, no in-place resize support.
	InPlaceNone InPlaceTier = "none"
	// InPlaceAlpha: 1.27-1.32, alpha and off by default.
	InPlaceAlpha InPlaceTier = "alpha"
	// InPlaceBeta: 1.33-1.34, beta and on by default.
	InPlaceBeta InPlaceTier = "beta"
	// InPlaceGA: >= 1.35, generally available.
	InPlaceGA InPlaceTier = "GA"
)

// ClusterStatus is the cluster-wide context shown in the output header. Fields
// are split between inferred (Tier) and confirmed (ConfirmedEnabled, InUse)
// signals so the renderer can label honestly.
type ClusterStatus struct {
	ServerVersion string

	// InPlaceTier is inferred from ServerVersion.
	InPlaceTier InPlaceTier
	// InPlaceConfirmedEnabled is true when the pods/resize subresource was found
	// via discovery — direct evidence the feature is enabled.
	InPlaceConfirmedEnabled bool
	// InPlaceInUse is true when at least one pod showed allocated-resources or
	// resize conditions — evidence the feature is actually exercised.
	InPlaceInUse bool
	// NodesNotReady names nodes whose runtime/kubelet are too old for in-place
	// resize even if the API supports it.
	NodesNotReady []string

	// VPAPresent is true when the VPA CRD is installed and at least one VPA was
	// read.
	VPAPresent bool

	// Scope describes the query scope for the header (e.g. a namespace name or
	// "all namespaces").
	Scope string

	// SkippedBarePods counts pods skipped because they had no controller owner.
	SkippedBarePods int
}

// InPlaceAvailable reports whether in-place resize can be relied upon: confirmed
// enabled by discovery, with no nodes flagged as too old. When false, applying a
// recommendation restarts the pod (the RESTART flag).
func (c ClusterStatus) InPlaceAvailable() bool {
	return c.InPlaceConfirmedEnabled && len(c.NodesNotReady) == 0
}
