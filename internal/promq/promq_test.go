package promq

import (
	"regexp"
	"strings"
	"testing"

	"github.com/heinanca/truce/internal/model"
)

// mustFullMatch compiles a Prometheus-style fully-anchored regex (Prometheus
// wraps label matchers in ^(?:...)$ implicitly).
func mustFullMatch(t *testing.T, rx string) *regexp.Regexp {
	t.Helper()
	re, err := regexp.Compile("^(?:" + rx + ")$")
	if err != nil {
		t.Fatalf("bad regex %q: %v", rx, err)
	}
	return re
}

func TestPodRegex(t *testing.T) {
	tests := []struct {
		kind  model.WorkloadKind
		name  string
		match []string
		nope  []string
	}{
		{
			model.KindDeployment, "api",
			[]string{"api-7d9f8b6c4-abcde", "api-abc123-x9y8z"},
			[]string{"api-gateway-7d9f8b6c4-abcde", "apiserver-1", "api"},
		},
		{
			model.KindStatefulSet, "pg",
			[]string{"pg-0", "pg-12"},
			[]string{"pg-0-abcde", "pgbouncer-0"},
		},
		{
			model.KindDaemonSet, "node-exp",
			[]string{"node-exp-abcde"},
			[]string{"node-exp-7d9f8-abcde"},
		},
	}
	for _, tt := range tests {
		rx := podRegex(model.Workload{Kind: tt.kind, Name: tt.name})
		// Prometheus anchors the whole string; emulate with ^(?:...)$.
		re := mustFullMatch(t, rx)
		for _, m := range tt.match {
			if !re.MatchString(m) {
				t.Errorf("%s %q: regex %q should match pod %q", tt.kind, tt.name, rx, m)
			}
		}
		for _, n := range tt.nope {
			if re.MatchString(n) {
				t.Errorf("%s %q: regex %q should NOT match pod %q", tt.kind, tt.name, rx, n)
			}
		}
	}
}

func TestQueryBuilders(t *testing.T) {
	o := DefaultOptions()
	cpu := cpuPeakUsageQuery(o, "prod", "api-.*", `,container!="",container!="POD"`)
	for _, want := range []string{"quantile_over_time(0.95,", "container_cpu_usage_seconds_total", `namespace="prod"`, `pod=~"api-.*"`, "[7d:5m]"} {
		if !strings.Contains(cpu, want) {
			t.Errorf("cpu query missing %q:\n%s", want, cpu)
		}
	}
	mem := memPeakMaxQuery(o, "prod", "api-.*", "app")
	for _, want := range []string{"max_over_time(max(", "container_memory_working_set_bytes", `container="app"`, "[7d:1m]"} {
		if !strings.Contains(mem, want) {
			t.Errorf("mem query missing %q:\n%s", want, mem)
		}
	}
}

func TestConsideredRequestSum(t *testing.T) {
	cs := []model.ContainerAnalysis{
		{Name: "app", Requests: model.Resources{CPUMilli: model.Int64(1000)}},
		{Name: "sidecar", Requests: model.Resources{CPUMilli: model.Int64(200)}},
	}
	// Resource (pod-level): sums all containers.
	res := &model.HPAMetric{SourceType: model.MetricResource, ResourceName: model.ResourceCPU}
	if v, ok := consideredRequestSum(cs, res); !ok || v != 1200 {
		t.Errorf("Resource sum = %d, %v; want 1200, true", v, ok)
	}
	// ContainerResource: only the named container.
	ctr := &model.HPAMetric{SourceType: model.MetricContainerResource, ResourceName: model.ResourceCPU, ContainerName: "app"}
	if v, ok := consideredRequestSum(cs, ctr); !ok || v != 1000 {
		t.Errorf("ContainerResource = %d, %v; want 1000, true", v, ok)
	}
	// Missing request on a considered container -> not ok.
	bad := []model.ContainerAnalysis{{Name: "app"}}
	if _, ok := consideredRequestSum(bad, res); ok {
		t.Error("missing request should yield ok=false")
	}
}
