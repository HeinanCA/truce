package detect

// GitOps annotation keys that signal a workload is reconciled by a controller,
// so live edits (or an applied VPA rec) will be reverted to the Git source.
const (
	annoArgoTrackingID = "argocd.argoproj.io/tracking-id"
	annoArgoInstance   = "app.kubernetes.io/instance"
	annoFluxName       = "kustomize.toolkit.fluxcd.io/name"
)

// GitOps reports whether a workload is GitOps-managed and, if so, names the
// detected tool. The app.kubernetes.io/instance label is a weaker signal (Argo
// CD commonly sets it, but it is generic), so it is reported as "Argo CD/other"
// to stay honest about the inference.
func GitOps(annotations map[string]string) (managed bool, tool string) {
	if annotations == nil {
		return false, ""
	}
	if _, ok := annotations[annoArgoTrackingID]; ok {
		return true, "Argo CD"
	}
	if _, ok := annotations[annoFluxName]; ok {
		return true, "Flux"
	}
	if _, ok := annotations[annoArgoInstance]; ok {
		return true, "Argo CD/other"
	}
	return false, ""
}
