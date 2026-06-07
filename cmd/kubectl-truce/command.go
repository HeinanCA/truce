package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"
	"k8s.io/cli-runtime/pkg/genericclioptions"

	"github.com/heinanca/truce/internal/collect"
	"github.com/heinanca/truce/internal/cost"
	"github.com/heinanca/truce/internal/detect"
	"github.com/heinanca/truce/internal/engine"
	"github.com/heinanca/truce/internal/model"
	"github.com/heinanca/truce/internal/promq"
	"github.com/heinanca/truce/internal/recommend"
	"github.com/heinanca/truce/internal/render"
	"github.com/heinanca/truce/internal/valuesfile"
)

// options holds the truce-specific flag values (kube flags live on ConfigFlags).
type options struct {
	configFlags   *genericclioptions.ConfigFlags
	allNamespaces bool
	output        string
	sortMode      string
	only          []string
	problemsOnly  bool
	failOn        []string
	tolerance     float64
	noColor       bool
	promURL       string
	window        string
	cpuQuantile   float64
	cpuHeadroom   float64
	memHeadroom   float64
	baseline      string
	setCPULimit   bool
	service       string
	valuesPath    string

	pricingFile string
	nodeCost    float64
	noPricing   bool
	cacheTTLHrs float64
}

// gateError signals that --fail-on matched; main maps it to a distinct exit code
// without printing a stack of operational-error text.
type gateError struct{ verdicts []model.Verdict }

func (e *gateError) Error() string {
	return fmt.Sprintf("fail-on: matched verdict(s) %v", e.verdicts)
}

// newRootCommand builds the cobra command tree.
func newRootCommand() *cobra.Command {
	o := &options{
		configFlags: genericclioptions.NewConfigFlags(true),
		output:      "table",
		tolerance:   engine.DefaultTolerance,
	}

	cmd := &cobra.Command{
		Use:   "kubectl-truce",
		Short: "HPA-aware Kubernetes rightsizing advisor (read-only)",
		Long: "truce predicts what your HorizontalPodAutoscaler will do if you apply a\n" +
			"VerticalPodAutoscaler recommendation, and shows the resulting footprint delta.\n" +
			"It is strictly read-only: it never writes, patches, or deletes.",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(c *cobra.Command, _ []string) error {
			return run(c.Context(), o, c.OutOrStdout())
		},
	}

	// Kube connection flags (--kubeconfig, --context, -n/--namespace, ...).
	o.configFlags.AddFlags(cmd.PersistentFlags())

	f := cmd.Flags()
	f.BoolVarP(&o.allNamespaces, "all-namespaces", "A", false, "scan all namespaces")
	f.StringVarP(&o.output, "output", "o", "table", "output format: recommend|advice|table|wide|json|diff")
	f.StringVar(&o.sortMode, "sort", "", "sort order: delta|name|verdict (default: problems first)")
	f.StringSliceVar(&o.only, "only", nil, "show only these verdicts (comma-separated)")
	f.BoolVar(&o.problemsOnly, "problems-only", false, "show only problem verdicts")
	f.StringSliceVar(&o.failOn, "fail-on", nil, "exit non-zero if any workload has these verdicts (CI gating)")
	f.Float64Var(&o.tolerance, "tolerance", engine.DefaultTolerance, "fallback HPA tolerance when not set on the HPA")
	f.BoolVar(&o.noColor, "no-color", false, "disable ANSI color")
	f.StringVar(&o.promURL, "prometheus", "", "Prometheus base URL for peak-aware verdicts (e.g. http://localhost:9090); without it, verdicts use the instantaneous snapshot")
	f.StringVar(&o.window, "window", "7d", "Prometheus look-back window for the usage spread (cpu p50/p95/max, mem p95/max)")
	f.Float64Var(&o.cpuQuantile, "cpu-quantile", 0.95, "quantile treated as cpu_p95 (memory p95 uses the same quantile; max always uses the max)")
	f.Float64Var(&o.cpuHeadroom, "cpu-headroom", 1.2, "multiplier over the CPU basis (cpu_max HPA-coupled, else cpu_p95) for the request")
	f.Float64Var(&o.memHeadroom, "mem-headroom", 1.25, "multiplier over mem_max for the memory request")
	f.StringVar(&o.baseline, "baseline", "p95", "re-prediction baseline: p95 or p50")
	f.BoolVar(&o.setCPULimit, "set-cpu-limit", false, "also recommend a CPU limit = ceil(cpu_max × 1.5) (default: leave unset for burst)")
	f.StringVar(&o.pricingFile, "pricing-file", "", "static instanceType→USD/hr map (YAML/JSON) for non-AWS clusters; AWS pricing is built in and auto-selected")
	f.Float64Var(&o.nodeCost, "node-cost", 0, "flat USD/node-hr static fallback when no per-type or AWS price is available")
	f.BoolVar(&o.noPricing, "no-pricing", false, "skip the AWS pricing backend (use static/PRICE-MISSING only)")
	f.Float64Var(&o.cacheTTLHrs, "pricing-cache-ttl", 24, "hours to cache AWS price lookups on disk")
	f.StringVar(&o.service, "service", "", "recommend HPA-aware, OOM-safe request values for a single workload by name")
	f.StringVar(&o.valuesPath, "values", "", "path to that service's values file; shows current→recommended as a diff (read-only)")

	return cmd
}

// run executes the full read-only pipeline: collect -> join -> diagnose ->
// detect -> engine -> render, then applies --fail-on gating.
func run(ctx context.Context, o *options, out io.Writer) error {
	only, err := parseVerdicts(o.only)
	if err != nil {
		return err
	}
	failOn, err := parseVerdicts(o.failOn)
	if err != nil {
		return err
	}
	if o.baseline != "p95" && o.baseline != "p50" {
		return fmt.Errorf("invalid --baseline %q (want p95 or p50)", o.baseline)
	}

	clients, err := collect.NewClients(o.configFlags)
	if err != nil {
		return err
	}

	scope, scopeLabel, err := o.resolveScope()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	raw, err := collect.Scan(ctx, clients, scope)
	if err != nil {
		return err
	}
	result := collect.Join(raw)
	diag := collect.Diagnose(raw, result)
	inplace := detect.InPlace(raw.ServerVersion, raw.ResizeSubresource, raw.Nodes, raw.Pods)

	// Optional peak-aware enrichment from Prometheus. Without it — or if
	// Prometheus is unreachable / returns nothing — verdicts rest on the HPA's
	// instantaneous snapshot, and the header says so honestly.
	workloads := result.Workloads
	promConfigured := o.promURL != ""
	promReachable := false
	if promConfigured {
		pc, err := promq.NewClient(o.promURL)
		if err != nil {
			return err
		}
		if perr := pc.Ping(ctx); perr != nil {
			// Don't fire dozens of doomed queries; fall back to snapshot loudly.
			fmt.Fprintf(os.Stderr, "truce: prometheus: %v — falling back to snapshot\n", perr)
		} else {
			promReachable = true
			popts := promq.DefaultOptions()
			popts.Window = o.window
			popts.CPUQuantile = o.cpuQuantile
			enriched, warns := promq.Enrich(ctx, pc, workloads, popts)
			workloads = enriched
			for _, wmsg := range warns {
				fmt.Fprintln(os.Stderr, "truce: prometheus:", wmsg)
			}
		}
	}

	cluster := model.ClusterStatus{
		ServerVersion:           raw.ServerVersion,
		InPlaceTier:             inplace.Tier,
		InPlaceConfirmedEnabled: inplace.ConfirmedEnabled,
		InPlaceInUse:            inplace.InUse,
		NodesNotReady:           inplace.NodesNotReady,
		VPAPresent:              result.VPAPresent,
		Scope:                   scopeLabel,
		SkippedBarePods:         result.SkippedBarePods,
	}

	engOpts := engine.Options{
		DefaultTolerance: o.tolerance,
		InPlaceAvailable: inplace.Available(),
		Now:              time.Now(),
	}

	rows := make([]model.WorkloadAnalysis, 0, len(workloads))
	for _, cw := range workloads {
		a := engine.Analyze(cw, engOpts)
		if a.Actionable {
			if managed, _ := detect.GitOps(cw.Workload.Annotations); managed {
				a.Flags = append(a.Flags, model.FlagGitOps)
			}
		}
		rows = append(rows, a)
	}

	// Single-service recommendation mode: compute HPA-aware, OOM-safe values for
	// one workload and (optionally) diff them against its values file.
	if o.service != "" {
		return recommendService(out, o, rows)
	}

	// Label the basis from what actually happened, not from intent — a SAFE
	// verdict computed on a snapshot must never be dressed up as peak-aware.
	basisLabel, snapshotOnly := basisStatus(rows, o, promConfigured, promReachable)

	// Estimate the dollar impact of the recommendations. Pricing is built in and
	// provider-pluggable; this never fails the run (PRICE-MISSING on no price).
	costReport := buildCostReport(ctx, o, collect.NodeInfos(raw.Nodes), rows)

	report := render.Report{
		Cluster:         cluster,
		Diagnostics:     diag,
		Workloads:       rows,
		UsageBasisLabel: basisLabel,
		SnapshotOnly:    snapshotOnly,
		Cost:            costReport,
	}
	ropts := render.Options{
		Format:       o.output,
		NoColor:      o.resolveNoColor(out),
		Sort:         render.SortMode(o.sortMode),
		Only:         only,
		ProblemsOnly: o.problemsOnly,
		Rec:          o.recConfig(),
	}
	if err := render.Render(out, report, ropts); err != nil {
		return err
	}

	return gateCheck(rows, failOn)
}

// recommendService computes and prints the recommendation for one workload,
// optionally diffing against its values file. It does not write the file.
func recommendService(out io.Writer, o *options, rows []model.WorkloadAnalysis) error {
	var found *model.WorkloadAnalysis
	for i := range rows {
		if rows[i].Workload.Name == o.service {
			found = &rows[i]
			break
		}
	}
	if found == nil {
		return fmt.Errorf("service %q not found in scope — need a Deployment/StatefulSet/DaemonSet named %q (try -n <namespace> or -A)", o.service, o.service)
	}
	if !found.Actionable {
		return fmt.Errorf("service %q has no VPA recommendation, so there is nothing to recommend", o.service)
	}

	rec := recommend.ForWith(*found, o.recConfig())
	p := render.NewPalette(o.resolveNoColor(out))
	if found.UsageBasis != model.BasisPeak {
		fmt.Fprintln(out, p.Yellow("⚠ snapshot basis — pass --prometheus <url> for peak-aware values; these can miss traffic spikes.\n"))
	}
	render.RenderRecommendation(out, rec, p)

	if o.valuesPath != "" {
		tree, err := valuesfile.Load(o.valuesPath)
		if err != nil {
			return err
		}
		render.RenderValuesDiff(out, o.valuesPath, valuesfile.FindRequests(tree), rec, p)
	}
	return nil
}

// basisStatus derives the honest usage-basis label and snapshot-only flag from
// the actual per-row outcome: how many actionable workloads ended up with peak
// data versus falling back to the snapshot.
func basisStatus(rows []model.WorkloadAnalysis, o *options, configured, reachable bool) (label string, snapshotOnly bool) {
	peak, actionable := 0, 0
	for _, a := range rows {
		if !a.Actionable {
			continue
		}
		actionable++
		if a.UsageBasis == model.BasisPeak {
			peak++
		}
	}

	switch {
	case !configured:
		return "instantaneous snapshot (HPA status / metrics-server)", true
	case !reachable:
		return fmt.Sprintf("snapshot — Prometheus UNREACHABLE at %s; every verdict fell back to the instantaneous snapshot", o.promURL), true
	case peak == 0:
		return fmt.Sprintf("snapshot — Prometheus reachable at %s but returned no usable data (check metric names / pod-name match); verdicts fell back to the snapshot", o.promURL), true
	case peak < actionable:
		return fmt.Sprintf("Prometheus peak (P%.0f CPU / max memory over %s) for %d of %d workloads; the rest fell back to snapshot — see stderr warnings",
			o.cpuQuantile*100, o.window, peak, actionable), false
	default:
		return fmt.Sprintf("Prometheus peak — P%.0f CPU / max memory over %s", o.cpuQuantile*100, o.window), false
	}
}

// buildCostReport prices the cluster's nodes and translates the recommendations'
// freed resources into a dollar estimate. It is read-only: node metadata comes
// from the already-collected nodes, and only read-only AWS APIs are called.
func buildCostReport(ctx context.Context, o *options, infos []model.NodeInfo, rows []model.WorkloadAnalysis) model.CostReport {
	now := time.Now()
	cfg := cost.Config{
		PricingFile: o.pricingFile,
		NodeCost:    o.nodeCost,
		CacheDir:    pricingCacheDir(),
		CacheTTL:    time.Duration(o.cacheTTLHrs * float64(time.Hour)),
		DisableAWS:  o.noPricing,
		Now:         now,
	}
	provider := cost.SelectProvider(ctx, infos, cfg)
	priced := cost.PriceNodes(ctx, provider, infos)

	var freedCPU, freedMem int64
	for _, a := range rows {
		if !a.Actionable {
			continue
		}
		d := recommend.ForWith(a, o.recConfig()).FootprintDelta
		if d.CPUMilli < 0 {
			freedCPU += -d.CPUMilli
		}
		if d.MemBytes < 0 {
			freedMem += -d.MemBytes
		}
	}
	return cost.Estimate(priced, provider.Backend(), freedCPU, freedMem, now)
}

// pricingCacheDir returns the on-disk cache directory for AWS price lookups,
// under the user cache dir; "" disables caching if it cannot be resolved.
func pricingCacheDir() string {
	base, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return base + "/truce/pricing"
}

// recConfig builds the recommendation sizing config from the flags.
func (o *options) recConfig() recommend.Config {
	return recommend.Config{
		Window:      o.window,
		CPUHeadroom: o.cpuHeadroom,
		MemHeadroom: o.memHeadroom,
		Baseline:    o.baseline,
		SetCPULimit: o.setCPULimit,
		Tolerance:   o.tolerance,
	}
}

// resolveScope determines the namespace scope and a human label for the header.
func (o *options) resolveScope() (collect.Scope, string, error) {
	if o.allNamespaces {
		return collect.Scope{AllNamespaces: true}, "all namespaces", nil
	}
	ns := ""
	if o.configFlags.Namespace != nil {
		ns = *o.configFlags.Namespace
	}
	if ns == "" {
		// Fall back to the kubeconfig's current namespace.
		resolved, _, err := o.configFlags.ToRawKubeConfigLoader().Namespace()
		if err != nil {
			return collect.Scope{}, "", fmt.Errorf("resolving namespace: %w", err)
		}
		ns = resolved
	}
	return collect.Scope{Namespace: ns}, "namespace " + ns, nil
}

// resolveNoColor disables color on --no-color, NO_COLOR env, or a non-terminal
// writer.
func (o *options) resolveNoColor(out io.Writer) bool {
	if o.noColor || os.Getenv("NO_COLOR") != "" {
		return true
	}
	if f, ok := out.(*os.File); ok {
		return !term.IsTerminal(int(f.Fd()))
	}
	return true
}

// gateCheck returns a gateError when any actionable workload carries a verdict
// in the fail-on set.
func gateCheck(rows []model.WorkloadAnalysis, failOn []model.Verdict) error {
	if len(failOn) == 0 {
		return nil
	}
	want := map[model.Verdict]bool{}
	for _, v := range failOn {
		want[v] = true
	}
	var hit []model.Verdict
	seen := map[model.Verdict]bool{}
	for _, a := range rows {
		if a.Actionable && want[a.Verdict] && !seen[a.Verdict] {
			seen[a.Verdict] = true
			hit = append(hit, a.Verdict)
		}
	}
	if len(hit) > 0 {
		return &gateError{verdicts: hit}
	}
	return nil
}
