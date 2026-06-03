package collect

import (
	"fmt"

	"github.com/heinanca/truce/internal/model"
)

// Diagnose produces an honest capability report from a scan: which inputs truce
// actually found, and for anything missing, the impact plus remediation. It
// never claims a capability is present without evidence.
func Diagnose(raw *RawCluster, res CollectResult) model.Diagnostics {
	var comps []model.ComponentStatus

	// VPA CRD — the core input. Without recommendations truce has nothing to
	// advise on, so this is the most consequential missing component.
	switch {
	case !raw.VPACRDInstalled:
		comps = append(comps, model.ComponentStatus{
			Name:      "VPA CRD (autoscaling.k8s.io/v1)",
			Available: false,
			Detail:    detailFromErr("verticalpodautoscalers not served", raw.VPAErr),
			Impact:    "No VPA recommendations to read — truce cannot advise. This is the core input.",
			Install:   "Install the Vertical Pod Autoscaler:\n    git clone https://github.com/kubernetes/autoscaler\n    ./autoscaler/vertical-pod-autoscaler/hack/vpa-up.sh\n  (Managed clusters may offer it as an add-on instead — check your provider.)",
		})
	case !res.VPAPresent:
		comps = append(comps, model.ComponentStatus{
			Name:      "VPA CRD (autoscaling.k8s.io/v1)",
			Available: true,
			Detail:    "CRD installed, but no VerticalPodAutoscaler targets a scanned workload",
			Impact:    "Nothing to advise in this scope. Create a VPA in recommendation mode (updateMode: \"Off\") for the workloads you want analyzed.",
		})
	default:
		comps = append(comps, model.ComponentStatus{
			Name:      "VPA CRD (autoscaling.k8s.io/v1)",
			Available: true,
			Detail:    "installed; recommendations matched",
		})
	}

	// metrics-server — needed only for the OOM-risk check. The HPA utilization
	// basis comes from HPA status, so prediction itself is unaffected.
	if raw.MetricsErr != nil {
		comps = append(comps, model.ComponentStatus{
			Name:      "metrics-server (metrics.k8s.io)",
			Available: false,
			Detail:    detailFromErr("PodMetrics not served", raw.MetricsErr),
			Impact:    "OOM-risk detection disabled (no live working-set). HPA prediction is unaffected — it reads the HPA's own status, not metrics-server.",
			Install:   "Install metrics-server:\n    kubectl apply -f https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/components.yaml\n  (Already bundled on many managed clusters — verify before installing.)",
		})
	} else {
		comps = append(comps, model.ComponentStatus{
			Name:      "metrics-server (metrics.k8s.io)",
			Available: true,
			Detail:    fmt.Sprintf("serving (%d pod metric sets read)", len(raw.PodMetrics)),
		})
	}

	// Server version — drives the in-place-resize tier inference (step 4).
	if raw.VersionErr != nil || raw.ServerVersion == "" {
		comps = append(comps, model.ComponentStatus{
			Name:      "server version (discovery)",
			Available: false,
			Detail:    detailFromErr("discovery ServerVersion failed", raw.VersionErr),
			Impact:    "In-place-resize capability tier cannot be inferred; RESTART risk reported conservatively.",
			Install:   "Verify cluster connectivity and RBAC for the discovery endpoint (no install needed).",
		})
	} else {
		comps = append(comps, model.ComponentStatus{
			Name:      "server version (discovery)",
			Available: true,
			Detail:    raw.ServerVersion,
		})
	}

	// HPAs — informational, not an install gap. Absence simply means every
	// workload evaluates as NO HPA (plain rightsizing).
	comps = append(comps, model.ComponentStatus{
		Name:      "HorizontalPodAutoscalers (autoscaling/v2)",
		Available: true,
		Detail:    fmt.Sprintf("%d found in scope", len(raw.HPAs)),
	})

	return model.Diagnostics{Components: comps}
}

// detailFromErr renders a short observed-detail string, appending the error when
// present.
func detailFromErr(base string, err error) string {
	if err == nil {
		return base
	}
	return fmt.Sprintf("%s: %v", base, err)
}
