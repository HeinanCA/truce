package render

import (
	"fmt"
	"io"

	"github.com/heinanca/truce/internal/model"
)

// renderDiff emits an apply-ready strategic-merge patch per workload, setting
// each container's resource requests to the VPA target. truce never applies
// these itself — it prints them for the user to review and apply. GitOps and
// restart caveats are emitted as comments so a blind apply cannot hide them.
func renderDiff(w io.Writer, rows []model.WorkloadAnalysis, p Palette) error {
	if len(rows) == 0 {
		fmt.Fprintln(w, p.Dim("# No actionable workloads to patch."))
		return nil
	}

	for _, a := range rows {
		fmt.Fprintf(w, "# ── %s  verdict=%s  Δ=%s\n",
			a.Workload.Key(), a.Verdict, plainDelta(a.FootprintDelta))

		if a.HasFlag(model.FlagGitOps) {
			fmt.Fprintln(w, p.Yellow("# WARNING: GitOps-managed — a live apply will be reverted by the controller. Commit this to your Git source instead."))
		}
		if a.HasFlag(model.FlagRestart) {
			fmt.Fprintln(w, p.Yellow("# WARNING: in-place resize unavailable — applying this RESTARTS the pods."))
		}
		if a.Verdict == model.VerdictOOMRisk {
			fmt.Fprintln(w, p.Red("# WARNING: OOM RISK — the memory target is below current usage; applying may OOM-kill the pod."))
		}
		if a.Verdict == model.VerdictScaleOut || a.Verdict == model.VerdictHitsCeiling {
			fmt.Fprintf(w, "# NOTE: predicted HPA reaction %d→%d replicas after applying.\n", a.CurrentReplicas, a.PredictedReplicas)
		}

		writePatch(w, a)
		fmt.Fprintf(w, "# apply: kubectl -n %s patch %s %s --patch-file <above>\n\n",
			a.Workload.Namespace, kindLower(a.Workload.Kind), a.Workload.Name)
	}
	return nil
}

// writePatch prints the strategic-merge patch body for a workload's containers.
func writePatch(w io.Writer, a model.WorkloadAnalysis) {
	fmt.Fprintf(w, "apiVersion: apps/v1\nkind: %s\nmetadata:\n  name: %s\n  namespace: %s\nspec:\n  template:\n    spec:\n      containers:\n",
		a.Workload.Kind, a.Workload.Name, a.Workload.Namespace)
	for _, c := range a.Containers {
		if !c.HasVPA {
			continue
		}
		fmt.Fprintf(w, "        - name: %s\n          resources:\n            requests:\n", c.Name)
		if v, ok := c.VPA.Target.CPU(); ok {
			fmt.Fprintf(w, "              cpu: \"%dm\"\n", v)
		}
		if v, ok := c.VPA.Target.Mem(); ok {
			fmt.Fprintf(w, "              memory: \"%s\"\n", memStr(v))
		}
	}
}

// plainDelta renders a delta without color, for comment lines.
func plainDelta(d model.Delta) string {
	return signedCPU(d.CPUMilli) + " / " + signedMem(d.MemBytes)
}

func kindLower(k model.WorkloadKind) string {
	switch k {
	case model.KindDeployment:
		return "deployment"
	case model.KindStatefulSet:
		return "statefulset"
	case model.KindDaemonSet:
		return "daemonset"
	default:
		return string(k)
	}
}
