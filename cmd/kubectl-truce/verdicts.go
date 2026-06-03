package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/heinanca/truce/internal/model"
)

// verdictAliases maps normalized CLI tokens to verdicts, so users can write
// "scale-out", "scaleout", or "out" for --only / --fail-on.
var verdictAliases = map[string]model.Verdict{
	"safe":        model.VerdictSafe,
	"scaleout":    model.VerdictScaleOut,
	"out":         model.VerdictScaleOut,
	"scalein":     model.VerdictScaleIn,
	"in":          model.VerdictScaleIn,
	"hitsceiling": model.VerdictHitsCeiling,
	"ceiling":     model.VerdictHitsCeiling,
	"oom":         model.VerdictOOMRisk,
	"oomrisk":     model.VerdictOOMRisk,
	"decoupled":   model.VerdictDecoupled,
	"nohpa":       model.VerdictNoHPA,
}

// normalizeVerdict lowercases and strips spaces/dashes for alias matching.
func normalizeVerdict(s string) string {
	r := strings.ToLower(strings.TrimSpace(s))
	r = strings.ReplaceAll(r, "-", "")
	r = strings.ReplaceAll(r, " ", "")
	return r
}

// parseVerdicts converts CLI tokens to verdicts, erroring on any unknown token
// with the list of valid choices.
func parseVerdicts(tokens []string) ([]model.Verdict, error) {
	var out []model.Verdict
	for _, t := range tokens {
		if t == "" {
			continue
		}
		v, ok := verdictAliases[normalizeVerdict(t)]
		if !ok {
			return nil, fmt.Errorf("unknown verdict %q (valid: %s)", t, validVerdicts())
		}
		out = append(out, v)
	}
	return out, nil
}

// validVerdicts lists the canonical alias tokens for error messages.
func validVerdicts() string {
	keys := make([]string, 0, len(verdictAliases))
	for k := range verdictAliases {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ", ")
}
