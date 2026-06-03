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

// Ping does one cheap query to confirm Prometheus is reachable, so the caller
// can skip enrichment (and avoid dozens of slow timeouts) and label the run
// honestly when it is not.
func (c *Client) Ping(ctx context.Context) error {
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, _, err := c.api.Query(cctx, "vector(1)", time.Now()); err != nil {
		return fmt.Errorf("prometheus unreachable: %w", err)
	}
	return nil
}

// queryScalar runs an instant query expected to yield a single scalar value
// (one-element vector). It returns ok=false when the result is empty (no data
// for the series), which the caller treats as "leave the snapshot in place".
func (c *Client) queryScalar(ctx context.Context, q string) (float64, bool, error) {
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	val, _, err := c.api.Query(cctx, q, time.Now())
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
