package render

import (
	"fmt"
	"io"
	"sort"

	"github.com/heinanca/truce/internal/model"
)

// adviceClass buckets a workload into a human action category.
type adviceClass int

const (
	classDoNow      adviceClass = iota // clean savings, no downside
	classWithCare                      // real savings but the HPA adds replicas
	classDoNotApply                    // OOM, ceiling, or footprint growth
)

// classify turns a verdict + footprint into an action category. Order matters:
// a trap (OOM / ceiling / growth) always wins over a savings framing.
func classify(a model.WorkloadAnalysis) adviceClass {
	grows := a.FootprintDelta.CPUMilli > 0 || a.FootprintDelta.MemBytes > 0
	switch {
	case a.Verdict == model.VerdictOOMRisk,
		a.Verdict == model.VerdictHitsCeiling,
		grows:
		return classDoNotApply
	case a.Verdict == model.VerdictScaleOut:
		return classWithCare
	default: // SAFE, SCALE-IN, NO HPA, DECOUPLED with net savings
		return classDoNow
	}
}

// renderAdvice prints plain-language conclusions and a prioritized action plan
// derived entirely from the model — no guessing. It is the view for someone who
// should not have to read flags and deltas to know what to do.
func renderAdvice(w io.Writer, r Report, rows []model.WorkloadAnalysis, p Palette) error {
	if len(rows) == 0 {
		fmt.Fprintln(w, p.Dim("No actionable workloads (need a workload with a VPA recommendation)."))
		return nil
	}

	var doNow, withCare, doNot []model.WorkloadAnalysis
	var savings model.Delta
	anyGitOps, anyLowConf := false, false
	for _, a := range rows {
		switch classify(a) {
		case classDoNow:
			doNow = append(doNow, a)
			savings = savings.Add(a.FootprintDelta)
		case classWithCare:
			withCare = append(withCare, a)
			savings = savings.Add(a.FootprintDelta)
		default:
			doNot = append(doNot, a)
		}
		if a.HasFlag(model.FlagGitOps) {
			anyGitOps = true
		}
		if a.HasFlag(model.FlagLowConf) {
			anyLowConf = true
		}
	}

	// --- Summary ---
	fmt.Fprintln(w, p.Bold("WHAT TO DO"))
	if r.SnapshotOnly {
		fmt.Fprintln(w, p.Yellow("  ⚠ snapshot basis — these conclusions can miss traffic spikes. Re-run with --prometheus for peak-aware advice."))
	}
	fmt.Fprintf(w, "  Apply now (clean savings): %d   ·   Apply with care (adds pods): %d   ·   DO NOT apply: %d\n",
		len(doNow), len(withCare), len(doNot))
	fmt.Fprintf(w, "  If you apply the safe + careful set: %s\n", deltaStr(savings, p))
	fmt.Fprintln(w)

	// --- Do now ---
	if len(doNow) > 0 {
		fmt.Fprintln(w, p.Green("✅ DO THESE FIRST — clean savings, no scale change"))
		for _, a := range sorted(doNow) {
			fmt.Fprintf(w, "  • %s: %s. Saves %s. Replicas %d→%d.\n",
				a.Workload.Name, changeDesc(a), savingsDesc(a.FootprintDelta), a.CurrentReplicas, a.PredictedReplicas)
		}
		fmt.Fprintln(w)
	}

	// --- With care ---
	if len(withCare) > 0 {
		fmt.Fprintln(w, p.Yellow("⚠ APPLY WITH CARE — saves money but the HPA adds pods at peak"))
		fmt.Fprintln(w, p.Dim("   Confirm your nodes have room and the HPA maxReplicas allows the predicted count."))
		for _, a := range sorted(withCare) {
			fmt.Fprintf(w, "  • %s: %s. Saves %s, but the HPA scales %d→%d pods.\n",
				a.Workload.Name, changeDesc(a), savingsDesc(a.FootprintDelta), a.CurrentReplicas, a.PredictedReplicas)
		}
		fmt.Fprintln(w)
	}

	// --- Do not apply ---
	if len(doNot) > 0 {
		fmt.Fprintln(w, p.Red("⛔ DO NOT APPLY — truce caught a trap a naive rightsizer would miss"))
		for _, a := range sorted(doNot) {
			fmt.Fprintf(w, "  • %s: %s\n", a.Workload.Name, trapReason(a))
		}
		fmt.Fprintln(w)
	}

	// --- How to apply ---
	renderHowTo(w, r, p, anyGitOps, anyLowConf)
	return nil
}

// changeDesc describes the per-replica request change in plain terms.
func changeDesc(a model.WorkloadAnalysis) string {
	oc, om, ohc, ohm := perReplicaOld(a.Containers)
	nc, nm, nhc, nhm := perReplicaNew(a.Containers)
	parts := ""
	if ohc || nhc {
		parts += fmt.Sprintf("CPU %s→%s", cpuStr(oc), cpuStr(nc))
	}
	if ohm || nhm {
		if parts != "" {
			parts += ", "
		}
		parts += fmt.Sprintf("memory %s→%s", memStr(om), memStr(nm))
	}
	if parts == "" {
		return "no request change"
	}
	return parts
}

// savingsDesc renders the magnitude of a (negative) delta as a positive saving.
func savingsDesc(d model.Delta) string {
	cpu := cpuStr(abs64(d.CPUMilli))
	mem := memStr(abs64(d.MemBytes))
	return cpu + " CPU + " + mem + " memory"
}

// trapReason explains, in one sentence, why a workload must not be applied.
func trapReason(a model.WorkloadAnalysis) string {
	switch {
	case a.Verdict == model.VerdictOOMRisk:
		return "the memory target is below the workload's real peak usage → applying it will OOM-kill the pods. Leave memory alone."
	case a.Verdict == model.VerdictHitsCeiling && a.FootprintDelta.MemBytes > 0:
		return fmt.Sprintf("the CPU cut drives the HPA to its ceiling (%d→%d pods) and ADDS %s of memory — a net loss. Reject.",
			a.CurrentReplicas, a.PredictedReplicas, memStr(a.FootprintDelta.MemBytes))
	case a.Verdict == model.VerdictHitsCeiling:
		return fmt.Sprintf("the request cut pins the HPA at its max (%d→%d pods) with no headroom left. Raise maxReplicas first or skip.",
			a.CurrentReplicas, a.PredictedReplicas)
	default: // footprint growth without ceiling
		return fmt.Sprintf("applying it grows the footprint (%s) because the HPA scales out past the per-pod savings.",
			plainDelta(a.FootprintDelta))
	}
}

// renderHowTo prints the apply path, tailored to GitOps and confidence.
func renderHowTo(w io.Writer, r Report, p Palette, gitops, lowConf bool) {
	fmt.Fprintln(w, p.Bold("🔁 HOW TO APPLY"))
	if gitops {
		fmt.Fprintln(w, "  These workloads are GitOps-managed (Argo CD / Flux) — editing the live cluster")
		fmt.Fprintln(w, "  gets reverted. Apply through your Git source instead:")
		fmt.Fprintln(w, "    1. Run `truce -o diff` to get the exact resource-request patch per workload.")
		fmt.Fprintln(w, "    2. Edit resources.requests in your Helm values / kustomize, not the cluster.")
		fmt.Fprintln(w, "    3. Open a PR and let your GitOps controller sync it.")
	} else {
		fmt.Fprintln(w, "    1. Run `truce -o diff` to get the exact resource-request patch per workload.")
		fmt.Fprintln(w, "    2. Apply it (e.g. `kubectl apply -f` / `kubectl patch`).")
	}
	if lowConf {
		fmt.Fprintln(w, p.Yellow("  Hold LOW-CONF workloads until their VPA has >48h of history — the numbers will still move."))
	}
	if !r.SnapshotOnly {
		fmt.Fprintln(w, p.Dim("  Basis: "+r.UsageBasisLabel))
	}
}

func sorted(rows []model.WorkloadAnalysis) []model.WorkloadAnalysis {
	out := make([]model.WorkloadAnalysis, len(rows))
	copy(out, rows)
	sort.SliceStable(out, func(i, j int) bool {
		return absInt64(out[i].FootprintDelta.CPUMilli) > absInt64(out[j].FootprintDelta.CPUMilli)
	})
	return out
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
