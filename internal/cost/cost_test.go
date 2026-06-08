package cost

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/heinanca/truce/internal/model"
)

func TestParseOnDemandUSD(t *testing.T) {
	doc := `{"product":{"sku":"X"},"terms":{"OnDemand":{"X.JRTCKXETXF":{"priceDimensions":` +
		`{"X.JRTCKXETXF.6YS6EN2CT7":{"unit":"Hrs","pricePerUnit":{"USD":"0.0960000000"}}}}}}}`
	v, ok := parseOnDemandUSD(doc)
	if !ok || v != 0.096 {
		t.Fatalf("parseOnDemandUSD = %v, %v; want 0.096, true", v, ok)
	}

	if _, ok := parseOnDemandUSD(`{"terms":{"OnDemand":{}}}`); ok {
		t.Error("empty OnDemand should not parse")
	}
	if _, ok := parseOnDemandUSD(`not json`); ok {
		t.Error("bad json should not parse")
	}
}

func node(pool, it string, cap model.CapacityType, cpuMilli, memBytes int64) model.NodeInfo {
	return model.NodeInfo{
		Name: it + "-x", InstanceType: it, Region: "us-east-1", Zone: "us-east-1a",
		Capacity: cap, NodePool: pool, AllocCPUMilli: cpuMilli, AllocMemBytes: memBytes,
	}
}

const gib16 = int64(16 * 1024 * 1024 * 1024)

func TestEstimate_BlendedMixAndSavings(t *testing.T) {
	now := time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)
	priced := []PricedNode{
		{node("default", "m5.large", model.CapacitySpot, 4000, gib16), model.NodeHourly{USDPerHour: 0.04, Source: model.PriceAWSSpot, AsOf: now}},
		{node("default", "m5.large", model.CapacitySpot, 4000, gib16), model.NodeHourly{USDPerHour: 0.04, Source: model.PriceAWSSpot, AsOf: now}},
		{node("default", "m5.large", model.CapacityOnDemand, 4000, gib16), model.NodeHourly{USDPerHour: 0.10, Source: model.PriceAWSOnDemand}},
		{node("default", "m5.large", model.CapacityOnDemand, 4000, gib16), model.NodeHourly{USDPerHour: 0.10, Source: model.PriceAWSOnDemand}},
	}
	// Free exactly two nodes' worth on both dimensions → low=high=2.
	r := Estimate(priced, model.PriceAWSOnDemand, 8000, 2*gib16, now)

	if !r.Enabled {
		t.Fatal("expected Enabled")
	}
	if r.SpotNodes != 2 || r.OnDemandNodes != 2 {
		t.Errorf("mix = %d spot / %d od, want 2/2", r.SpotNodes, r.OnDemandNodes)
	}
	if r.NodesSavedLow != 2 || r.NodesSavedHigh != 2 {
		t.Errorf("nodesSaved = %d–%d, want 2–2", r.NodesSavedLow, r.NodesSavedHigh)
	}
	if len(r.Pools) != 1 {
		t.Fatalf("want 1 pool, got %d", len(r.Pools))
	}
	pool := r.Pools[0]
	// Blended = (0.04+0.04+0.10+0.10)/4 = 0.07.
	if pool.BlendedHourly < 0.0699 || pool.BlendedHourly > 0.0701 {
		t.Errorf("blended = %v, want ~0.07", pool.BlendedHourly)
	}
	// Monthly = 2 nodes × 0.07 × 730 = 102.2.
	want := 2 * 0.07 * HoursPerMonth
	if r.TotalMonthlyHigh < want-0.5 || r.TotalMonthlyHigh > want+0.5 {
		t.Errorf("TotalMonthlyHigh = %v, want ~%v", r.TotalMonthlyHigh, want)
	}
	if pool.SpotCount != 2 || pool.OnDemandCount != 2 {
		t.Errorf("pool mix = %d/%d, want 2/2", pool.SpotCount, pool.OnDemandCount)
	}
}

// TestEstimate_CostWeightedAttribution is the regression for the count-share bug:
// a small but expensive on-demand pool must still get savings attributed (it was
// being rounded to $0 and showed a blank SAVE/MONTH while the cheap spot pool took
// all the credit).
func TestEstimate_CostWeightedAttribution(t *testing.T) {
	now := time.Date(2026, 6, 7, 0, 0, 0, 0, time.UTC)
	priced := []PricedNode{
		// 1 pricey on-demand node in its own pool (1/5 of nodes, but 1/2 of cost).
		{node("c8i", "c8i.4xlarge", model.CapacityOnDemand, 4000, gib16), model.NodeHourly{USDPerHour: 0.40, Source: model.PriceAWSOnDemand}},
		// 4 cheap spot nodes.
		{node("default", "m5.large", model.CapacitySpot, 4000, gib16), model.NodeHourly{USDPerHour: 0.10, Source: model.PriceAWSSpot, AsOf: now}},
		{node("default", "m5.large", model.CapacitySpot, 4000, gib16), model.NodeHourly{USDPerHour: 0.10, Source: model.PriceAWSSpot, AsOf: now}},
		{node("default", "m5.large", model.CapacitySpot, 4000, gib16), model.NodeHourly{USDPerHour: 0.10, Source: model.PriceAWSSpot, AsOf: now}},
		{node("default", "m5.large", model.CapacitySpot, 4000, gib16), model.NodeHourly{USDPerHour: 0.10, Source: model.PriceAWSSpot, AsOf: now}},
	}
	// Free 2 nodes' worth on both dimensions → low=high=2.
	r := Estimate(priced, model.PriceAWSOnDemand, 8000, 2*gib16, now)

	var c8i, def model.NodePoolCost
	for _, p := range r.Pools {
		switch p.Name {
		case "c8i.4xlarge":
			c8i = p
		case "m5.large":
			def = p
		}
	}
	// The expensive 1-node pool must NOT be zeroed.
	if c8i.MonthlyHigh <= 0 {
		t.Fatalf("c8i pool savings zeroed (the bug): %+v", c8i)
	}
	// clusterUSDSum = 0.40 + 4×0.10 = 0.80; c8i weight = 0.40/0.80 = 0.5.
	// clusterBlended = 0.80/5 = 0.16; clusterMonthly = 2×0.16×730 = 233.6.
	wantTotal := 2 * (0.80 / 5) * HoursPerMonth
	if r.TotalMonthlyHigh < wantTotal-0.5 || r.TotalMonthlyHigh > wantTotal+0.5 {
		t.Errorf("TotalMonthlyHigh = %v, want ~%v", r.TotalMonthlyHigh, wantTotal)
	}
	// c8i (½ the cost) gets ~½ the savings despite being 1 of 5 nodes.
	if c8i.MonthlyHigh < def.MonthlyHigh-0.5 || c8i.MonthlyHigh > def.MonthlyHigh+0.5 {
		t.Errorf("expected c8i ≈ default savings (cost-weighted): c8i=%v default=%v", c8i.MonthlyHigh, def.MonthlyHigh)
	}
	// Pools sum to the cluster total (no rounding loss).
	if sum := c8i.MonthlyHigh + def.MonthlyHigh; sum < r.TotalMonthlyHigh-0.01 || sum > r.TotalMonthlyHigh+0.01 {
		t.Errorf("pool sum %v != total %v", sum, r.TotalMonthlyHigh)
	}
}

func TestEstimate_PriceMissingStillReportsSavings(t *testing.T) {
	now := time.Date(2026, 6, 4, 0, 0, 0, 0, time.UTC)
	priced := []PricedNode{
		{node("p", "weird.type", model.CapacityOnDemand, 4000, gib16), model.NodeHourly{Source: model.PriceMissing, Missing: true}},
		{node("p", "weird.type", model.CapacityOnDemand, 4000, gib16), model.NodeHourly{Source: model.PriceMissing, Missing: true}},
	}
	r := Estimate(priced, model.PriceMissing, 4000, gib16, now)
	if r.Enabled {
		t.Error("Enabled should be false when nothing priced")
	}
	if !r.PriceMissing {
		t.Error("PriceMissing should be true")
	}
	if r.NodesSavedHigh < 1 {
		t.Errorf("should still report node savings, got %d", r.NodesSavedHigh)
	}
	if r.FreedCPUMilli != 4000 {
		t.Errorf("FreedCPUMilli = %d, want 4000", r.FreedCPUMilli)
	}
}

func TestNodesSaved_BoundedByTighterDimension(t *testing.T) {
	// CPU frees 4 nodes, memory frees 1 → conservative low=1, optimistic high=4.
	low, high := nodesSaved(16000, gib16, 4000, float64(gib16), 10)
	if low != 1 || high != 4 {
		t.Errorf("nodesSaved = %d–%d, want 1–4", low, high)
	}
	// Clamp to total node count.
	low, high = nodesSaved(1_000_000, 1_000_000*gib16, 4000, float64(gib16), 3)
	if high != 3 {
		t.Errorf("high = %d, want clamped to 3", high)
	}
}

func TestStaticProvider(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "prices.yaml")
	if err := os.WriteFile(path, []byte("prices:\n  m5.large: 0.096\ndefault: 0.05\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := newStaticProvider(Config{PricingFile: path})
	if err != nil {
		t.Fatalf("newStaticProvider: %v", err)
	}
	if h := p.NodeHourly(context.Background(), model.NodeInfo{InstanceType: "m5.large"}); h.USDPerHour != 0.096 || h.Source != model.PriceStatic {
		t.Errorf("per-type = %+v, want 0.096 static", h)
	}
	// Unknown type falls back to default.
	if h := p.NodeHourly(context.Background(), model.NodeInfo{InstanceType: "x.unknown"}); h.USDPerHour != 0.05 {
		t.Errorf("default = %+v, want 0.05", h)
	}

	// node-cost flat rate only.
	fp, err := newStaticProvider(Config{NodeCost: 0.2})
	if err != nil {
		t.Fatalf("flat: %v", err)
	}
	if h := fp.NodeHourly(context.Background(), model.NodeInfo{InstanceType: "anything"}); h.USDPerHour != 0.2 {
		t.Errorf("flat = %+v, want 0.2", h)
	}

	// No prices at all is an error.
	if _, err := newStaticProvider(Config{}); err == nil {
		t.Error("expected error with no static prices")
	}
}

func TestSelectProvider(t *testing.T) {
	ctx := context.Background()
	awsNodes := []model.NodeInfo{node("p", "m5.large", model.CapacityOnDemand, 4000, gib16)}
	nonAWS := []model.NodeInfo{{InstanceType: "n1-standard", Region: "europe-west1"}}

	// No primary + list disabled → missing.
	if p := SelectProvider(ctx, nonAWS, Config{DisableListPrices: true}); p.Backend() != model.PriceMissing {
		t.Errorf("backend = %v, want missing", p.Backend())
	}
	// No primary + list enabled (default) → offline list backend.
	if p := SelectProvider(ctx, nonAWS, Config{}); p.Backend() != model.PriceListOffline {
		t.Errorf("backend = %v, want list-offline", p.Backend())
	}
	// Static config → static primary (wins the Backend label even with list layered).
	if p := SelectProvider(ctx, nonAWS, Config{NodeCost: 0.1}); p.Backend() != model.PriceStatic {
		t.Errorf("backend = %v, want static", p.Backend())
	}
	// AWS-looking nodes but AWS disabled + static config → static (no network).
	if p := SelectProvider(ctx, awsNodes, Config{DisableAWS: true, NodeCost: 0.1}); p.Backend() != model.PriceStatic {
		t.Errorf("backend = %v, want static when AWS disabled", p.Backend())
	}
}

func TestListProvider(t *testing.T) {
	ctx := context.Background()
	p := newListProvider()

	// Known type, us-east-1 baseline (multiplier 1.0).
	h := p.NodeHourly(ctx, model.NodeInfo{InstanceType: "m5.large", Region: "us-east-1"})
	if h.Missing || h.Source != model.PriceListOffline || h.USDPerHour <= 0 {
		t.Fatalf("baseline = %+v, want priced list-offline", h)
	}
	base := h.USDPerHour
	if h.AsOf.IsZero() {
		t.Error("list price should carry a generated-on date")
	}

	// Region multiplier applied (eu-central-1 > us-east-1).
	eu := p.NodeHourly(ctx, model.NodeInfo{InstanceType: "m5.large", Region: "eu-central-1"})
	if eu.USDPerHour <= base {
		t.Errorf("eu-central-1 %.4f should exceed baseline %.4f", eu.USDPerHour, base)
	}

	// Unknown region falls back to baseline (multiplier 1.0).
	unk := p.NodeHourly(ctx, model.NodeInfo{InstanceType: "m5.large", Region: "moon-1"})
	if unk.USDPerHour != base {
		t.Errorf("unknown region = %.4f, want baseline %.4f", unk.USDPerHour, base)
	}

	// Unknown type misses.
	if h := p.NodeHourly(ctx, model.NodeInfo{InstanceType: "weird.type", Region: "us-east-1"}); !h.Missing {
		t.Errorf("unknown type = %+v, want Missing", h)
	}
}

func TestFallbackProvider(t *testing.T) {
	ctx := context.Background()
	primary := &staticProvider{perType: map[string]float64{"m5.large": 0.05}}
	f := fallbackProvider{primary: primary, secondary: newListProvider()}

	// Primary hit passes through (and keeps the primary's Backend label).
	if h := f.NodeHourly(ctx, model.NodeInfo{InstanceType: "m5.large", Region: "us-east-1"}); h.Source != model.PriceStatic || h.USDPerHour != 0.05 {
		t.Errorf("primary hit = %+v, want 0.05 static", h)
	}
	if f.Backend() != model.PriceStatic {
		t.Errorf("backend = %v, want primary (static)", f.Backend())
	}

	// Primary miss → secondary fills from the offline list.
	if h := f.NodeHourly(ctx, model.NodeInfo{InstanceType: "c5.large", Region: "us-east-1"}); h.Missing || h.Source != model.PriceListOffline {
		t.Errorf("primary miss = %+v, want list-offline fill", h)
	}

	// Both miss → Missing.
	if h := f.NodeHourly(ctx, model.NodeInfo{InstanceType: "weird.type", Region: "us-east-1"}); !h.Missing {
		t.Errorf("both miss = %+v, want Missing", h)
	}
}

func TestEstimate_ListOfflineNoteAndEnabled(t *testing.T) {
	now := time.Date(2026, 6, 4, 0, 0, 0, 0, time.UTC)
	asOf := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	priced := []PricedNode{
		{node("p", "m5.large", model.CapacityOnDemand, 4000, gib16), model.NodeHourly{USDPerHour: 0.096, Source: model.PriceListOffline, AsOf: asOf}},
		{node("p", "m5.large", model.CapacityOnDemand, 4000, gib16), model.NodeHourly{USDPerHour: 0.096, Source: model.PriceListOffline, AsOf: asOf}},
	}
	r := Estimate(priced, model.PriceAWSOnDemand, 8000, 2*gib16, now)
	if !r.Enabled {
		t.Fatal("list-priced nodes should enable the dollar figure")
	}
	if r.ListPriceAsOf.IsZero() {
		t.Error("ListPriceAsOf should be set from list-priced nodes")
	}
	if want := "built-in AWS list prices"; !strings.Contains(r.Note, want) {
		t.Errorf("note %q should mention %q", r.Note, want)
	}
}

func TestDiskCache(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)
	c := newDiskCache(dir, 24*time.Hour, now)

	c.put("ondemand:m5.large:us-east-1", cacheEntry{USDPerHour: 0.096, Source: "aws-ondemand", Observed: now})
	if e, ok := c.get("ondemand:m5.large:us-east-1"); !ok || e.USDPerHour != 0.096 {
		t.Errorf("get = %+v, %v; want 0.096, true", e, ok)
	}
	// Stale entry past TTL.
	stale := newDiskCache(dir, 24*time.Hour, now.Add(48*time.Hour))
	if _, ok := stale.get("ondemand:m5.large:us-east-1"); ok {
		t.Error("entry past TTL should be a miss")
	}
	// Missing key.
	if _, ok := c.get("nope"); ok {
		t.Error("missing key should be a miss")
	}
}
