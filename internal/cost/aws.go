package cost

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/pricing"
	ptypes "github.com/aws/aws-sdk-go-v2/service/pricing/types"

	"github.com/heinanca/truce/internal/model"
)

// awsProvider prices nodes from AWS read-only APIs: the EC2 Price List API for
// on-demand (stable) and the spot price history for spot (variable, per-AZ).
// Results are cached on disk so repeated runs and repeated instance types do not
// refetch. It is the auto-selected default for AWS clusters.
type awsProvider struct {
	pricing *pricing.Client
	ec2     *ec2.Client
	cache   *diskCache
	now     time.Time
}

// newAWSProvider loads the default AWS config (env, shared config, IRSA, or
// instance role) and confirms credentials resolve. The pricing endpoint lives in
// us-east-1; spot queries override the region per call. Returns an error so the
// selector can fall back to static/missing when AWS is not actually configured.
func newAWSProvider(ctx context.Context, cfg Config) (*awsProvider, error) {
	awscfg, err := config.LoadDefaultConfig(ctx, config.WithRegion("us-east-1"))
	if err != nil {
		return nil, fmt.Errorf("loading AWS config: %w", err)
	}
	if _, err := awscfg.Credentials.Retrieve(ctx); err != nil {
		return nil, fmt.Errorf("aws credentials did not resolve: %w", err)
	}
	return &awsProvider{
		pricing: pricing.NewFromConfig(awscfg),
		ec2:     ec2.NewFromConfig(awscfg),
		cache:   newDiskCache(cfg.CacheDir, cfg.ttl(), cfg.now()),
		now:     cfg.now(),
	}, nil
}

func (p *awsProvider) Backend() model.PriceSource { return model.PriceAWSOnDemand }

// NodeHourly prices a node by its capacity type: spot via the spot history,
// everything else via on-demand. A failed lookup returns Missing rather than an
// error so one un-priceable type never fails the whole run.
func (p *awsProvider) NodeHourly(ctx context.Context, node model.NodeInfo) model.NodeHourly {
	if node.Capacity == model.CapacitySpot && node.Zone != "" {
		return p.spot(ctx, node)
	}
	return p.onDemand(ctx, node)
}

func missing() model.NodeHourly {
	return model.NodeHourly{Source: model.PriceMissing, Missing: true}
}

// onDemand resolves the stable on-demand hourly price (cached).
func (p *awsProvider) onDemand(ctx context.Context, node model.NodeInfo) model.NodeHourly {
	if node.InstanceType == "" || node.Region == "" {
		return missing()
	}
	key := "ondemand:" + node.InstanceType + ":" + node.Region
	if e, ok := p.cache.get(key); ok {
		return model.NodeHourly{USDPerHour: e.USDPerHour, Source: model.PriceAWSOnDemand}
	}
	usd, err := p.fetchOnDemand(ctx, node.InstanceType, node.Region)
	if err != nil || usd <= 0 {
		return missing()
	}
	p.cache.put(key, cacheEntry{USDPerHour: usd, Source: string(model.PriceAWSOnDemand), Observed: p.now})
	return model.NodeHourly{USDPerHour: usd, Source: model.PriceAWSOnDemand}
}

// spot resolves the latest spot price for the node's AZ (cached, dated).
func (p *awsProvider) spot(ctx context.Context, node model.NodeInfo) model.NodeHourly {
	key := "spot:" + node.InstanceType + ":" + node.Zone
	if e, ok := p.cache.get(key); ok {
		return model.NodeHourly{USDPerHour: e.USDPerHour, Source: model.PriceAWSSpot, AsOf: e.Observed}
	}
	usd, asOf, err := p.fetchSpot(ctx, node.InstanceType, node.Zone, node.Region)
	if err != nil || usd <= 0 {
		// No spot price (rare type / AZ) — fall back to on-demand rather than miss.
		return p.onDemand(ctx, node)
	}
	p.cache.put(key, cacheEntry{USDPerHour: usd, Source: string(model.PriceAWSSpot), Observed: asOf})
	return model.NodeHourly{USDPerHour: usd, Source: model.PriceAWSSpot, AsOf: asOf}
}

// fetchOnDemand calls pricing:GetProducts and parses the Linux/shared/used
// on-demand USD rate for the instance type in the region.
func (p *awsProvider) fetchOnDemand(ctx context.Context, instanceType, region string) (float64, error) {
	f := func(field, value string) ptypes.Filter {
		return ptypes.Filter{Type: ptypes.FilterTypeTermMatch, Field: aws.String(field), Value: aws.String(value)}
	}
	out, err := p.pricing.GetProducts(ctx, &pricing.GetProductsInput{
		ServiceCode: aws.String("AmazonEC2"),
		Filters: []ptypes.Filter{
			f("instanceType", instanceType),
			f("regionCode", region),
			f("operatingSystem", "Linux"),
			f("tenancy", "Shared"),
			f("preInstalledSw", "NA"),
			f("capacitystatus", "Used"),
		},
		FormatVersion: aws.String("aws_v1"),
		MaxResults:    aws.Int32(10),
	})
	if err != nil {
		return 0, fmt.Errorf("pricing GetProducts %s/%s: %w", instanceType, region, err)
	}
	for _, doc := range out.PriceList {
		if usd, ok := parseOnDemandUSD(doc); ok {
			return usd, nil
		}
	}
	return 0, fmt.Errorf("no on-demand price found for %s in %s", instanceType, region)
}

// onDemandDoc is the minimal shape of a Price List API product document needed
// to read the on-demand USD rate.
type onDemandDoc struct {
	Terms struct {
		OnDemand map[string]struct {
			PriceDimensions map[string]struct {
				PricePerUnit struct {
					USD string `json:"USD"`
				} `json:"pricePerUnit"`
			} `json:"priceDimensions"`
		} `json:"OnDemand"`
	} `json:"terms"`
}

// parseOnDemandUSD walks a product document to the first on-demand USD rate.
func parseOnDemandUSD(doc string) (float64, bool) {
	var d onDemandDoc
	if err := json.Unmarshal([]byte(doc), &d); err != nil {
		return 0, false
	}
	for _, term := range d.Terms.OnDemand {
		for _, dim := range term.PriceDimensions {
			if dim.PricePerUnit.USD == "" {
				continue
			}
			if v, err := strconv.ParseFloat(dim.PricePerUnit.USD, 64); err == nil && v > 0 {
				return v, true
			}
		}
	}
	return 0, false
}

// fetchSpot calls ec2:DescribeSpotPriceHistory for the type+AZ and returns the
// most recent price and its timestamp.
func (p *awsProvider) fetchSpot(ctx context.Context, instanceType, zone, region string) (float64, time.Time, error) {
	out, err := p.ec2.DescribeSpotPriceHistory(ctx, &ec2.DescribeSpotPriceHistoryInput{
		InstanceTypes:       []ec2types.InstanceType{ec2types.InstanceType(instanceType)},
		AvailabilityZone:    aws.String(zone),
		ProductDescriptions: []string{"Linux/UNIX"},
		StartTime:           aws.Time(p.now.Add(-6 * time.Hour)),
		MaxResults:          aws.Int32(10),
	}, func(o *ec2.Options) {
		if region != "" {
			o.Region = region
		}
	})
	if err != nil {
		return 0, time.Time{}, fmt.Errorf("ec2 DescribeSpotPriceHistory %s/%s: %w", instanceType, zone, err)
	}
	var bestUSD float64
	var bestAt time.Time
	for _, sp := range out.SpotPriceHistory {
		if sp.SpotPrice == nil || sp.Timestamp == nil {
			continue
		}
		v, perr := strconv.ParseFloat(*sp.SpotPrice, 64)
		if perr != nil || v <= 0 {
			continue
		}
		if sp.Timestamp.After(bestAt) {
			bestAt = *sp.Timestamp
			bestUSD = v
		}
	}
	if bestUSD <= 0 {
		return 0, time.Time{}, fmt.Errorf("no spot price for %s in %s", instanceType, zone)
	}
	return bestUSD, bestAt, nil
}
