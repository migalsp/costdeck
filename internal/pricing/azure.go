package pricing

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// AzurePricer reads pay-as-you-go Linux VM prices from the public Azure Retail Prices
// API. It needs no credentials and caches every answer for the life of the process.
type AzurePricer struct {
	HTTP    *http.Client
	BaseURL string

	mu    sync.Mutex
	cache map[string]float64
}

// NewAzurePricer returns a pricer for the public endpoint.
func NewAzurePricer() *AzurePricer {
	return &AzurePricer{HTTP: &http.Client{Timeout: 20 * time.Second}, BaseURL: "https://prices.azure.com"}
}

type azurePrice struct {
	RetailPrice   float64 `json:"retailPrice"`
	UnitOfMeasure string  `json:"unitOfMeasure"`
	ProductName   string  `json:"productName"`
	SkuName       string  `json:"skuName"`
	Type          string  `json:"type"`
}

// HourlyPrice implements NodePricer. region is an ARM region ("westeurope") and
// instanceType a VM size ("Standard_D4s_v5"), as AKS puts them in node labels.
func (p *AzurePricer) HourlyPrice(ctx context.Context, region, instanceType string) (float64, error) {
	key := region + "/" + instanceType
	p.mu.Lock()
	if v, ok := p.cache[key]; ok {
		p.mu.Unlock()
		return v, nil
	}
	p.mu.Unlock()

	filter := fmt.Sprintf("serviceName eq 'Virtual Machines' and armRegionName eq '%s' and armSkuName eq '%s' and priceType eq 'Consumption'",
		odataEscape(region), odataEscape(instanceType))
	next := p.BaseURL + "/api/retail/prices?$filter=" + url.QueryEscape(filter)
	best := 0.0
	for pages := 0; next != "" && pages < 10; pages++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, next, nil)
		if err != nil {
			return 0, err
		}
		resp, err := p.HTTP.Do(req)
		if err != nil {
			return 0, err
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		_ = resp.Body.Close()
		if err != nil {
			return 0, err
		}
		if resp.StatusCode != http.StatusOK {
			return 0, fmt.Errorf("azure retail prices: HTTP %d", resp.StatusCode)
		}
		var page struct {
			Items        []azurePrice `json:"Items"`
			NextPageLink string       `json:"NextPageLink"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return 0, err
		}
		for _, it := range page.Items {
			// Linux pay-as-you-go only: Windows licences, Spot and low-priority capacity
			// are priced differently.
			if it.Type != "Consumption" || it.UnitOfMeasure != "1 Hour" || it.RetailPrice <= 0 ||
				strings.Contains(it.ProductName, "Windows") ||
				strings.Contains(it.SkuName, "Spot") || strings.Contains(it.SkuName, "Low Priority") {
				continue
			}
			if best == 0 || it.RetailPrice < best {
				best = it.RetailPrice
			}
		}
		next = page.NextPageLink
	}
	if best == 0 {
		return 0, fmt.Errorf("no pay-as-you-go Linux price for %s in %s", instanceType, region)
	}
	p.mu.Lock()
	if p.cache == nil {
		p.cache = map[string]float64{}
	}
	p.cache[key] = best
	p.mu.Unlock()
	return best, nil
}

// odataEscape doubles single quotes, the only escape OData string literals need.
func odataEscape(s string) string { return strings.ReplaceAll(s, "'", "''") }
