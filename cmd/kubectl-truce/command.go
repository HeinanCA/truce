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
	"github.com/heinanca/truce/internal/detect"
	"github.com/heinanca/truce/internal/engine"
	"github.com/heinanca/truce/internal/model"
	"github.com/heinanca/truce/internal/render"
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
	f.StringVarP(&o.output, "output", "o", "table", "output format: table|wide|json|diff")
	f.StringVar(&o.sortMode, "sort", "", "sort order: delta|name|verdict (default: problems first)")
	f.StringSliceVar(&o.only, "only", nil, "show only these verdicts (comma-separated)")
	f.BoolVar(&o.problemsOnly, "problems-only", false, "show only problem verdicts")
	f.StringSliceVar(&o.failOn, "fail-on", nil, "exit non-zero if any workload has these verdicts (CI gating)")
	f.Float64Var(&o.tolerance, "tolerance", engine.DefaultTolerance, "fallback HPA tolerance when not set on the HPA")
	f.BoolVar(&o.noColor, "no-color", false, "disable ANSI color")

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

	rows := make([]model.WorkloadAnalysis, 0, len(result.Workloads))
	for _, cw := range result.Workloads {
		a := engine.Analyze(cw, engOpts)
		if a.Actionable {
			if managed, _ := detect.GitOps(cw.Workload.Annotations); managed {
				a.Flags = append(a.Flags, model.FlagGitOps)
			}
		}
		rows = append(rows, a)
	}

	report := render.Report{Cluster: cluster, Diagnostics: diag, Workloads: rows}
	ropts := render.Options{
		Format:       o.output,
		NoColor:      o.resolveNoColor(out),
		Sort:         render.SortMode(o.sortMode),
		Only:         only,
		ProblemsOnly: o.problemsOnly,
	}
	if err := render.Render(out, report, ropts); err != nil {
		return err
	}

	return gateCheck(rows, failOn)
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
