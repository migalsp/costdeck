package scaling

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
)

func day(s string) time.Time { t, _ := time.Parse(time.DateOnly, s); return t }

func TestAzureDailyComputeCost(t *testing.T) {
	f := &fakeCloud{routes: map[string]func(http.ResponseWriter, *http.Request){
		"POST /subscriptions/" + sub + "/resourceGroups/MC_rg_aks_westeurope/providers/Microsoft.CostManagement/query": jsonReply(map[string]any{
			"properties": map[string]any{
				"columns": []map[string]string{{"name": "Cost"}, {"name": "UsageDate"}, {"name": "Currency"}},
				"rows":    [][]any{{12.5, 20260928, "EUR"}, {10.0, 20260929, "EUR"}},
			},
		}),
	}}
	rest, base := testREST(t, f)
	p := &AzureProvider{subscription: sub, rest: rest, base: base}
	bill, cur, err := p.DailyComputeCost(context.Background(), "MC_rg_aks_westeurope", day("2026-09-28"), day("2026-10-04"))
	if err != nil || cur != "EUR" || bill["2026-09-28"] != 12.5 || bill["2026-09-29"] != 10 {
		t.Fatalf("got %v %s %v", bill, cur, err)
	}
	if !strings.Contains(f.bodies[0], `"AmortizedCost"`) || !strings.Contains(f.bodies[0], "Virtual Machines") {
		t.Errorf("query should ask for amortized VM cost: %s", f.bodies[0])
	}
	if _, _, err := p.DailyComputeCost(context.Background(), "bad/../group", day("2026-09-28"), day("2026-10-04")); err == nil {
		t.Error("a resource group with a slash must be rejected")
	}
}

func TestGCPDailyComputeCost(t *testing.T) {
	f := &fakeCloud{routes: map[string]func(http.ResponseWriter, *http.Request){
		"POST /projects/demo/queries": jsonReply(map[string]any{
			"jobComplete": true,
			"rows": []map[string]any{
				{"f": []map[string]any{{"v": "2026-09-28"}, {"v": "8.25"}, {"v": "USD"}}},
				{"f": []map[string]any{{"v": "2026-09-29"}, {"v": "7.75"}, {"v": "USD"}}},
			},
		}),
	}}
	rest, base := testREST(t, f)
	p := &GCPProvider{project: "demo", rest: rest, bigqueryBase: base}
	bill, cur, err := p.DailyComputeCost(context.Background(), "billing-proj.export.gcp_billing_export_resource_v1_X", "prod-eu", day("2026-09-28"), day("2026-10-04"))
	if err != nil || cur != "USD" || bill["2026-09-28"] != 8.25 || len(bill) != 2 {
		t.Fatalf("got %v %s %v", bill, cur, err)
	}
	body := f.bodies[0]
	if !strings.Contains(body, "`billing-proj.export.gcp_billing_export_resource_v1_X`") || !strings.Contains(body, `"value":"prod-eu"`) ||
		strings.Contains(body, "prod-eu'") {
		t.Errorf("the table is inlined, the cluster is a parameter: %s", body)
	}
	if _, _, err := p.DailyComputeCost(context.Background(), "x.y`; DROP TABLE z; --", "c", day("2026-09-28"), day("2026-10-04")); err == nil {
		t.Error("an unsafe table name must be rejected")
	}
}

func TestAWSDailyComputeCost(t *testing.T) {
	var target, body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target = r.Header.Get("X-Amz-Target")
		buf := make([]byte, 4096)
		n, _ := r.Body.Read(buf)
		body = string(buf[:n])
		w.Header().Set("Content-Type", "application/x-amz-json-1.1")
		_, _ = w.Write([]byte(`{"ResultsByTime":[
			{"TimePeriod":{"Start":"2026-09-28","End":"2026-09-29"},"Total":{"AmortizedCost":{"Amount":"20.5","Unit":"USD"}}},
			{"TimePeriod":{"Start":"2026-09-29","End":"2026-09-30"},"Total":{"AmortizedCost":{"Amount":"19.5","Unit":"USD"}}}]}`))
	}))
	defer srv.Close()
	cfg := aws.Config{Region: "eu-west-1", Credentials: credentials.NewStaticCredentialsProvider("AKID", "SECRET", ""), BaseEndpoint: aws.String(srv.URL)}
	p := &AWSProvider{cfg: cfg}
	bill, cur, err := p.DailyComputeCost(context.Background(), "aws:eks:cluster-name", "prod", day("2026-09-28"), day("2026-10-04"))
	if err != nil || cur != "USD" || bill["2026-09-28"] != 20.5 || bill["2026-09-29"] != 19.5 {
		t.Fatalf("got %v %s %v", bill, cur, err)
	}
	if !strings.HasSuffix(target, "GetCostAndUsage") || !strings.Contains(body, `"End":"2026-10-05"`) || !strings.Contains(body, AWSComputeService) {
		t.Errorf("request: %s %s", target, body)
	}
}
