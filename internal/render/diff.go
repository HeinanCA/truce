package render

import (
	"fmt"
	"io"

	"github.com/heinanca/truce/internal/model"
	"github.com/heinanca/truce/internal/recommend"
)

// renderDiff emits an apply-ready strategic-merge patch per workload, setting
// each container's resource requests to truce's peak-based recommendation (NOT
// the raw VPA target, which would re-introduce the scale-out trap). truce never
// applies these itself — it prints them for review. GitOps and restart caveats
// are emitted as comments so a blind apply cannot hide them.
func renderDiff(w io.Writer, rows []model.WorkloadAnalysis, cfg recommend.Config, p Palette) error {
	if len(rows) == 0 {
		fmt.Fprintln(w, p.Dim("# No actionable workloads to patch."))
		return nil
	}

	for _, a := range rows {
		rec := recommend.ForWith(a, cfg)
		fmt.Fprintf(w, "# ── %s  verdict=%s  Δ=%s\n",
			a.Workload.Key(), a.Verdict, plainDelta(rec.FootprintDelta))

		if a.HasFlag(model.FlagGitOps) {
			fmt.Fprintln(w, p.Yellow("# WARNING: GitOps-managed — a live apply will be reverted by the controller. Commit this to your Git source instead."))
		}
		if a.HasFlag(model.FlagRestart) {
			fmt.Fprintln(w, p.Yellow("# WARNING: in-place resize unavailable — applying this RESTARTS the pods."))
		}
		if rec.HPAStillScales {
			fmt.Fprintf(w, "%s\n", p.Red(fmt.Sprintf("# WARNING: even peak-sized, the HPA re-predicts %d→%d replicas — raise maxReplicas (≥ %d).",
				rec.CurrentReplicas, rec.PredictedReplicas, rec.RaiseMaxTo)))
		} else {
			fmt.Fprintf(w, "# NOTE: HPA re-predicted %d→%d replicas with this request (no scale-out).\n", rec.CurrentReplicas, rec.PredictedReplicas)
		}

		writePatch(w, a, rec)
		fmt.Fprintf(w, "# apply: kubectl -n %s patch %s %s --patch-file <above>\n\n",
			a.Workload.Namespace, kindLower(a.Workload.Kind), a.Workload.Name)
	}
	return nil
}

// writePatch prints the strategic-merge patch body, setting each container's
// requests to truce's recommendation. HOLD recommendations (no usage data) are
// emitted unchanged, so a patch never silently cuts a request below real usage.
func writePatch(w io.Writer, a model.WorkloadAnalysis, rec recommend.Recommendation) {
	fmt.Fprintf(w, "apiVersion: apps/v1\nkind: %s\nmetadata:\n  name: %s\n  namespace: %s\nspec:\n  template:\n    spec:\n      containers:\n",
		a.Workload.Kind, a.Workload.Name, a.Workload.Namespace)
	for _, c := range rec.Containers {
		if c.CPURec == nil && c.MemRec == nil {
			continue
		}
		fmt.Fprintf(w, "        - name: %s\n          resources:\n            requests:\n", c.Name)
		if c.CPURec != nil {
			fmt.Fprintf(w, "              cpu: \"%dm\"\n", *c.CPURec)
		}
		if c.MemRec != nil {
			fmt.Fprintf(w, "              memory: \"%s\"\n", memStr(*c.MemRec))
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
