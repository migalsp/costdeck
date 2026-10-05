package pricing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	awspricing "github.com/aws/aws-sdk-go-v2/service/pricing"
	"github.com/aws/aws-sdk-go-v2/service/pricing/types"
)

// awsPriceListRegion hosts the Price List API endpoint.
const awsPriceListRegion = "us-east-1"

// GetProductsAPI is the part of the Price List client CostDeck uses.
type GetProductsAPI interface {
	GetProducts(ctx context.Context, in *awspricing.GetProductsInput, optFns ...func(*awspricing.Options)) (*awspricing.GetProductsOutput, error)
}

// AWSPricer reads on-demand Linux prices from the AWS Price List API. It needs the
// pricing:GetProducts permission and caches every answer for the life of the process.
type AWSPricer struct {
	API GetProductsAPI

	mu    sync.Mutex
	cache map[string]float64
}

// NewAWSPricer builds a pricer from an AWS config (credentials from the CostDeckConfig
// secret or the pod identity).
func NewAWSPricer(cfg aws.Config) *AWSPricer {
	cfg.Region = awsPriceListRegion
	return &AWSPricer{API: awspricing.NewFromConfig(cfg)}
}

// HourlyPrice implements NodePricer.
func (p *AWSPricer) HourlyPrice(ctx context.Context, region, instanceType string) (float64, error) {
	key := region + "/" + instanceType
	p.mu.Lock()
	if price, ok := p.cache[key]; ok {
		p.mu.Unlock()
		return price, nil
	}
	p.mu.Unlock()

	filter := func(field, value string) types.Filter {
		return types.Filter{Type: types.FilterTypeTermMatch, Field: aws.String(field), Value: aws.String(value)}
	}
	out, err := p.API.GetProducts(ctx, &awspricing.GetProductsInput{
		ServiceCode: aws.String("AmazonEC2"),
		Filters: []types.Filter{
			filter("instanceType", instanceType),
			filter("regionCode", region),
			filter("operatingSystem", "Linux"),
			filter("tenancy", "Shared"),
			filter("preInstalledSw", "NA"),
			filter("capacitystatus", "Used"),
		},
		MaxResults: aws.Int32(10),
	})
	if err != nil {
		return 0, err
	}
	price, err := onDemandUSD(out.PriceList)
	if err != nil {
		return 0, err
	}
	p.mu.Lock()
	if p.cache == nil {
		p.cache = map[string]float64{}
	}
	p.cache[key] = price
	p.mu.Unlock()
	return price, nil
}

// onDemandUSD extracts the hourly on-demand USD price from Price List product documents.
func onDemandUSD(priceList []string) (float64, error) {
	for _, doc := range priceList {
		var product struct {
			Terms struct {
				OnDemand map[string]struct {
					PriceDimensions map[string]struct {
						Unit         string            `json:"unit"`
						PricePerUnit map[string]string `json:"pricePerUnit"`
					} `json:"priceDimensions"`
				} `json:"OnDemand"`
			} `json:"terms"`
		}
		if err := json.Unmarshal([]byte(doc), &product); err != nil {
			continue
		}
		for _, term := range product.Terms.OnDemand {
			for _, dim := range term.PriceDimensions {
				if dim.Unit != "Hrs" {
					continue
				}
				if price, err := strconv.ParseFloat(dim.PricePerUnit["USD"], 64); err == nil && price > 0 {
					return price, nil
				}
			}
		}
	}
	if len(priceList) == 0 {
		return 0, errors.New("the Price List API has no matching product")
	}
	return 0, fmt.Errorf("no hourly on-demand USD price in %d product(s)", len(priceList))
}
