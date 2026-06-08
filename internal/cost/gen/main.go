// Command gen regenerates internal/cost/listprice_data.go from the AWS public
// Price List bulk API. It is not part of the truce binary; run it with:
//
//	go generate ./internal/cost
//
// It fetches the EC2 on-demand list price (Linux / Shared tenancy / Used /
// preInstalledSw=NA) per instance type for us-east-1 (the baseline) and, for every
// other region, derives a single multiplier = median over a basket of common types
// of regionPrice/usEast1Price. The result is a us-east-1 price map plus a per-region
// multiplier map — small enough to embed as Go source, accurate to a few percent
// across regions. Spot and account-specific rates are intentionally out of scope.
package main

import (
	"encoding/json"
	"fmt"
	"go/format"
	"io"
	"net/http"
	"os"
	"sort"
	"time"
)

const (
	host       = "https://pricing.us-east-1.amazonaws.com"
	regionIdx  = host + "/offers/v1.0/aws/AmazonEC2/current/region_index.json"
	baseline   = "us-east-1"
	outputPath = "listprice_data.go"
)

// basket is the set of common current-gen on-demand types used to derive a region's
// multiplier. They exist in nearly every region, so the median ratio is stable.
var basket = []string{
	"m5.large", "m5.xlarge", "m6i.large", "m6i.xlarge", "m7i.large",
	"c5.large", "c5.xlarge", "c6i.large", "c7i.large",
	"r5.large", "r5.xlarge", "r6i.large", "r7i.large",
	"t3.medium", "t3.large", "t3.xlarge",
}

// regionIndex is the shape of region_index.json.
type regionIndex struct {
	Regions map[string]struct {
		CurrentVersionURL string `json:"currentVersionUrl"`
	} `json:"regions"`
}

// offerFile is the minimal shape of a region's EC2 offer index.json needed to read
// on-demand Linux/Shared/Used/NA prices keyed by instance type.
type offerFile struct {
	Products map[string]struct {
		ProductFamily string `json:"productFamily"`
		Attributes    struct {
			InstanceType    string `json:"instanceType"`
			OperatingSystem string `json:"operatingSystem"`
			Tenancy         string `json:"tenancy"`
			PreInstalledSw  string `json:"preInstalledSw"`
			CapacityStatus  string `json:"capacitystatus"`
		} `json:"attributes"`
	} `json:"products"`
	Terms struct {
		OnDemand map[string]map[string]struct {
			PriceDimensions map[string]struct {
				PricePerUnit struct {
					USD string `json:"USD"`
				} `json:"pricePerUnit"`
			} `json:"priceDimensions"`
		} `json:"OnDemand"`
	} `json:"terms"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "gen:", err)
		os.Exit(1)
	}
}

func run() error {
	var ri regionIndex
	if err := getJSON(regionIdx, &ri); err != nil {
		return fmt.Errorf("region index: %w", err)
	}

	base, ok := ri.Regions[baseline]
	if !ok {
		return fmt.Errorf("baseline region %s missing from index", baseline)
	}
	usEast1, err := regionPrices(host + base.CurrentVersionURL)
	if err != nil {
		return fmt.Errorf("baseline prices: %w", err)
	}
	if len(usEast1) == 0 {
		return fmt.Errorf("no baseline prices parsed")
	}

	mult := map[string]float64{baseline: 1.0}
	regions := make([]string, 0, len(ri.Regions))
	for r := range ri.Regions {
		regions = append(regions, r)
	}
	sort.Strings(regions)

	for _, r := range regions {
		if r == baseline {
			continue
		}
		prices, err := regionPrices(host + ri.Regions[r].CurrentVersionURL)
		if err != nil {
			fmt.Fprintf(os.Stderr, "gen: skip %s: %v\n", r, err)
			continue
		}
		if m, ok := multiplier(usEast1, prices); ok {
			mult[r] = m
		}
	}

	return emit(usEast1, mult, time.Now().UTC().Format("2006-01"))
}

// regionPrices parses one region's offer file into instanceType -> USD/hr for the
// on-demand Linux / Shared / Used / NA SKUs.
func regionPrices(url string) (map[string]float64, error) {
	var f offerFile
	if err := getJSON(url, &f); err != nil {
		return nil, err
	}
	out := map[string]float64{}
	for sku, p := range f.Products {
		a := p.Attributes
		if p.ProductFamily != "Compute Instance" || a.InstanceType == "" {
			continue
		}
		if a.OperatingSystem != "Linux" || a.Tenancy != "Shared" ||
			a.PreInstalledSw != "NA" || a.CapacityStatus != "Used" {
			continue
		}
		usd, ok := onDemandUSD(f, sku)
		if !ok || usd <= 0 {
			continue
		}
		// Keep the lowest valid price if a type maps to multiple SKUs.
		if cur, seen := out[a.InstanceType]; !seen || usd < cur {
			out[a.InstanceType] = usd
		}
	}
	return out, nil
}

// onDemandUSD walks terms.OnDemand for a SKU to its first positive USD rate.
func onDemandUSD(f offerFile, sku string) (float64, bool) {
	for term, t := range f.Terms.OnDemand {
		if len(term) < len(sku) || term[:len(sku)] != sku {
			continue
		}
		for _, dim := range t {
			for _, d := range dim.PriceDimensions {
				var v float64
				if _, err := fmt.Sscanf(d.PricePerUnit.USD, "%g", &v); err == nil && v > 0 {
					return v, true
				}
			}
		}
	}
	return 0, false
}

// multiplier is the median of regionPrice/usEast1Price over the basket types present
// in both maps. Median is robust to per-family variance in regional pricing.
func multiplier(usEast1, region map[string]float64) (float64, bool) {
	var ratios []float64
	for _, t := range basket {
		b, ok1 := usEast1[t]
		r, ok2 := region[t]
		if ok1 && ok2 && b > 0 {
			ratios = append(ratios, r/b)
		}
	}
	if len(ratios) == 0 {
		return 0, false
	}
	sort.Float64s(ratios)
	mid := len(ratios) / 2
	if len(ratios)%2 == 1 {
		return ratios[mid], true
	}
	return (ratios[mid-1] + ratios[mid]) / 2, true
}

func getJSON(url string, v any) error {
	resp, err := http.Get(url) //nolint:gosec // public AWS price list URLs
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

// emit writes listprice_data.go (gofmt-formatted) with the two maps and the date.
func emit(usEast1, mult map[string]float64, on string) error {
	var b []byte
	b = append(b, []byte("// Code generated by internal/cost/gen. DO NOT EDIT.\n\n")...)
	b = append(b, []byte("package cost\n\n")...)
	b = append(b, []byte(fmt.Sprintf("var listPriceGeneratedOn = %q\n\n", on))...)

	b = append(b, []byte("var listPriceUSEast1 = map[string]float64{\n")...)
	b = appendSortedMap(b, usEast1, 4)
	b = append(b, []byte("}\n\n")...)

	b = append(b, []byte("var listRegionMultiplier = map[string]float64{\n")...)
	b = appendSortedMap(b, mult, 4)
	b = append(b, []byte("}\n")...)

	formatted, err := format.Source(b)
	if err != nil {
		return fmt.Errorf("gofmt: %w", err)
	}
	return os.WriteFile(outputPath, formatted, 0o644)
}

func appendSortedMap(b []byte, m map[string]float64, decimals int) []byte {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		b = append(b, []byte(fmt.Sprintf("\t%q: %.*f,\n", k, decimals, m[k]))...)
	}
	return b
}
