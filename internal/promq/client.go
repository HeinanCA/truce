package promq

import (
	"context"
	"fmt"
	"time"

	promapi "github.com/prometheus/client_golang/api"
	promv1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"
)

// Client is a thin read-only wrapper over the Prometheus HTTP API. It performs
// instant queries only (the time aggregation lives inside the PromQL).
type Client struct {
	api promv1.API
}

// Options configures the peak queries.
type Options struct {
	// Window is the PromQL range, e.g. "7d".
	Window string
	// CPUQuantile is the quantile for CPU peak (e.g. 0.95).
	CPUQuantile float64
	// CPUMetric / MemMetric are the cAdvisor metric names (overridable for
	// non-standard setups).
	CPUMetric string
	MemMetric string
}

// DefaultOptions returns the standard cAdvisor metric names and a 7-day P95 CPU
// window.
func DefaultOptions() Options {
	return Options{
		Window:      "7d",
		CPUQuantile: 0.95,
		CPUMetric:   "container_cpu_usage_seconds_total",
		MemMetric:   "container_memory_working_set_bytes",
	}
}

// NewClient builds a Prometheus client for the given base URL (e.g.
// http://localhost:9090 after a port-forward).
func NewClient(address string) (*Client, error) {
	c, err := promapi.NewClient(promapi.Config{Address: address})
	if err != nil {
		return nil, fmt.Errorf("building Prometheus client: %w", err)
	}
	return &Client{api: promv1.NewAPI(c)}, nil
}

// queryScalar runs an instant query expected to yield a single scalar value
// (one-element vector). It returns ok=false when the result is empty (no data
// for the series), which the caller treats as "leave the snapshot in place".
func (c *Client) queryScalar(ctx context.Context, q string) (float64, bool, error) {
	val, _, err := c.api.Query(ctx, q, time.Now())
	if err != nil {
		return 0, false, fmt.Errorf("prometheus query %q: %w", q, err)
	}
	vec, ok := val.(model.Vector)
	if !ok || len(vec) == 0 {
		return 0, false, nil
	}
	// Take the max across any returned series (defensive: queries collapse to one).
	max := float64(vec[0].Value)
	for _, s := range vec[1:] {
		if float64(s.Value) > max {
			max = float64(s.Value)
		}
	}
	return max, true, nil
}
