package scaling

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer"
	cetypes "github.com/aws/aws-sdk-go-v2/service/costexplorer/types"
)

// The cloud bill per UTC day, for reconciling list-price estimates with what was paid.
// Every source returns the cluster's compute cost after discounts: amortized Reserved
// Instances and Savings Plans, spot prices and credits.

// DailyCost maps a UTC date (YYYY-MM-DD) to the billed amount.
type DailyCost map[string]float64

// Billing source names, shown in the status.
const (
	BillingAWS   = "AWS Cost Explorer"
	BillingAzure = "Azure Cost Management"
	BillingGCP   = "Google Cloud billing export"
)

// usEast1 is where AWS serves its global billing and pricing APIs.
const usEast1 = "us-east-1"

// AWSComputeService is the Cost Explorer service of EC2 instances.
const AWSComputeService = "Amazon Elastic Compute Cloud - Compute"

// DailyComputeCost reads the amortized EC2 instance cost per day of the instances that
// carry the tag, from Cost Explorer. from and to are inclusive UTC days.
func (p *AWSProvider) DailyComputeCost(ctx context.Context, tagKey, tagValue string, from, to time.Time) (DailyCost, string, error) {
	cfg := p.cfg.Copy()
	cfg.Region = usEast1 // Cost Explorer is served from us-east-1 only
	ce := costexplorer.NewFromConfig(cfg)
	out, currency := DailyCost{}, ""
	var token *string
	for range 50 {
		res, err := ce.GetCostAndUsage(ctx, &costexplorer.GetCostAndUsageInput{
			TimePeriod:  &cetypes.DateInterval{Start: aws.String(from.Format(time.DateOnly)), End: aws.String(to.AddDate(0, 0, 1).Format(time.DateOnly))},
			Granularity: cetypes.GranularityDaily,
			Metrics:     []string{"AmortizedCost"},
			Filter: &cetypes.Expression{And: []cetypes.Expression{
				{Dimensions: &cetypes.DimensionValues{Key: cetypes.DimensionService, Values: []string{AWSComputeService}}},
				{Tags: &cetypes.TagValues{Key: aws.String(tagKey), Values: []string{tagValue}}},
			}},
			NextPageToken: token,
		})
		if err != nil {
			return nil, "", fmt.Errorf("cost explorer: %w", err)
		}
		for _, r := range res.ResultsByTime {
			m, ok := r.Total["AmortizedCost"]
			if !ok || r.TimePeriod == nil || r.TimePeriod.Start == nil || m.Amount == nil {
				continue
			}
			amount, err := strconv.ParseFloat(*m.Amount, 64)
			if err != nil {
				continue
			}
			out[*r.TimePeriod.Start] += amount
			if m.Unit != nil {
				currency = *m.Unit
			}
		}
		if res.NextPageToken == nil || *res.NextPageToken == "" {
			return out, currency, nil
		}
		token = res.NextPageToken
	}
	return out, currency, errors.New("cost explorer returned too many pages")
}

var azureResourceGroup = regexp.MustCompile(`^[-\w._()]{1,90}$`)

// DailyComputeCost reads the amortized virtual machine cost per day of one resource group
// from Cost Management. from and to are inclusive UTC days.
func (p *AzureProvider) DailyComputeCost(ctx context.Context, resourceGroup string, from, to time.Time) (DailyCost, string, error) {
	if !azureResourceGroup.MatchString(resourceGroup) {
		return nil, "", fmt.Errorf("%q is not a resource group name", resourceGroup)
	}
	body := map[string]any{
		"type":      "AmortizedCost",
		"timeframe": "Custom",
		"timePeriod": map[string]string{
			"from": from.Format("2006-01-02T00:00:00Z"),
			"to":   to.Format("2006-01-02T23:59:59Z"),
		},
		"dataset": map[string]any{
			"granularity": "Daily",
			"aggregation": map[string]any{"totalCost": map[string]string{"name": "Cost", "function": "Sum"}},
			"filter": map[string]any{"dimensions": map[string]any{
				"name": "MeterCategory", "operator": "In", "values": []string{"Virtual Machines"},
			}},
		},
	}
	next := fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s/providers/Microsoft.CostManagement/query?api-version=2023-11-01",
		p.base, url.PathEscape(p.subscription), url.PathEscape(resourceGroup))
	out, currency := DailyCost{}, ""
	for page := 0; next != "" && page < 50; page++ {
		var res struct {
			Properties struct {
				NextLink string `json:"nextLink"`
				Columns  []struct {
					Name string `json:"name"`
				} `json:"columns"`
				Rows [][]any `json:"rows"`
			} `json:"properties"`
		}
		if err := p.rest.do(ctx, http.MethodPost, next, body, &res); err != nil {
			return nil, "", fmt.Errorf("cost management: %w", err)
		}
		col := map[string]int{}
		for i, c := range res.Properties.Columns {
			col[strings.ToLower(c.Name)] = i
		}
		costIdx, okCost := col["cost"]
		dateIdx, okDate := col["usagedate"]
		curIdx, okCur := col["currency"]
		if !okCost || !okDate {
			return nil, "", errors.New("cost management answered without Cost and UsageDate columns")
		}
		for _, row := range res.Properties.Rows {
			if len(row) <= max(costIdx, dateIdx) {
				continue
			}
			amount, okA := row[costIdx].(float64)
			date, okD := row[dateIdx].(float64) // YYYYMMDD as a number
			if !okA || !okD {
				continue
			}
			d := strconv.Itoa(int(date))
			if len(d) == 8 {
				out[d[:4]+"-"+d[4:6]+"-"+d[6:]] += amount
			}
			if okCur && len(row) > curIdx {
				if c, ok := row[curIdx].(string); ok {
					currency = c
				}
			}
		}
		var err error
		if next, err = nextLink(p.base, res.Properties.NextLink); err != nil {
			return nil, "", err
		}
	}
	return out, currency, nil
}

var bigQueryTable = regexp.MustCompile(`^[a-z][-a-z0-9]{4,28}[a-z0-9]\.[A-Za-z0-9_]+\.[A-Za-z0-9_]+$`)

// gcpBillingQuery sums Compute Engine cost with credits per day for one GKE cluster. The
// table name is validated, never taken as written; the values are query parameters.
const gcpBillingQuery = "SELECT FORMAT_DATE('%%F', DATE(usage_start_time)) AS day, " +
	"SUM(cost) + SUM(IFNULL((SELECT SUM(c.amount) FROM UNNEST(credits) c), 0)) AS cost, ANY_VALUE(currency) AS currency " +
	"FROM `%s` WHERE service.description = 'Compute Engine' " +
	"AND EXISTS (SELECT 1 FROM UNNEST(labels) l WHERE l.key = 'goog-k8s-cluster-name' AND l.value = @cluster) " +
	"AND usage_start_time >= @from AND usage_start_time < @to GROUP BY day"

// DailyComputeCost reads the cost per day, credits included, of the cluster's nodes from
// the BigQuery billing export. The query runs in the provider's project.
func (p *GCPProvider) DailyComputeCost(ctx context.Context, table, cluster string, from, to time.Time) (DailyCost, string, error) {
	if !bigQueryTable.MatchString(table) {
		return nil, "", fmt.Errorf("%q is not a BigQuery table (project.dataset.table)", table)
	}
	param := func(name, typ, value string) map[string]any {
		return map[string]any{"name": name, "parameterType": map[string]string{"type": typ}, "parameterValue": map[string]string{"value": value}}
	}
	body := map[string]any{
		"query":         fmt.Sprintf(gcpBillingQuery, table),
		"useLegacySql":  false,
		"parameterMode": "NAMED",
		"timeoutMs":     30000,
		"queryParameters": []any{
			param("cluster", "STRING", cluster),
			param("from", "TIMESTAMP", from.Format("2006-01-02 00:00:00")+" UTC"),
			param("to", "TIMESTAMP", to.AddDate(0, 0, 1).Format("2006-01-02 00:00:00")+" UTC"),
		},
	}
	type row struct {
		F []struct {
			V any `json:"v"`
		} `json:"f"`
	}
	var res struct {
		JobComplete  bool  `json:"jobComplete"`
		Rows         []row `json:"rows"`
		JobReference struct {
			JobID    string `json:"jobId"`
			Location string `json:"location"`
		} `json:"jobReference"`
	}
	base := fmt.Sprintf("%s/projects/%s/queries", p.bigqueryBase, url.PathEscape(p.project))
	if err := p.rest.do(ctx, http.MethodPost, base, body, &res); err != nil {
		return nil, "", fmt.Errorf("bigquery: %w", err)
	}
	for wait := 0; !res.JobComplete && wait < 10; wait++ {
		select {
		case <-ctx.Done():
			return nil, "", ctx.Err()
		case <-time.After(2 * time.Second):
		}
		u := fmt.Sprintf("%s/%s?location=%s&timeoutMs=10000", base, url.PathEscape(res.JobReference.JobID), url.QueryEscape(res.JobReference.Location))
		if err := p.rest.do(ctx, http.MethodGet, u, nil, &res); err != nil {
			return nil, "", fmt.Errorf("bigquery: %w", err)
		}
	}
	if !res.JobComplete {
		return nil, "", errors.New("the BigQuery job did not finish in time")
	}
	out, currency := DailyCost{}, ""
	for _, r := range res.Rows {
		if len(r.F) < 3 {
			continue
		}
		day, _ := r.F[0].V.(string)
		costText, _ := r.F[1].V.(string)
		amount, err := strconv.ParseFloat(costText, 64)
		if day == "" || err != nil {
			continue
		}
		out[day] += amount
		if c, ok := r.F[2].V.(string); ok {
			currency = c
		}
	}
	return out, currency, nil
}
