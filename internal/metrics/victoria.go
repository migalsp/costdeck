package metrics

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// VMClient queries a PromQL-compatible endpoint (VictoriaMetrics / Prometheus).
type VMClient struct {
	Endpoint   string
	HTTPClient *http.Client
	// Auth fields resolved from the K8s Secret
	BearerToken string
	Username    string
	Password    string
}

// NewVMClient creates a VMClient, optionally resolving auth credentials from a K8s Secret.
func NewVMClient(ctx context.Context, k8sClient client.Client, endpoint, secretRef, secretNamespace string) (*VMClient, error) {
	c := &VMClient{
		Endpoint:   endpoint,
		HTTPClient: &http.Client{Timeout: 10 * time.Second},
	}

	if secretRef != "" {
		secret := &corev1.Secret{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: secretRef, Namespace: secretNamespace}, secret); err != nil {
			return nil, fmt.Errorf("failed to read VictoriaMetrics secret %s/%s: %w", secretNamespace, secretRef, err)
		}
		c.BearerToken = string(secret.Data["BEARER_TOKEN"])
		c.Username = string(secret.Data["USERNAME"])
		c.Password = string(secret.Data["PASSWORD"])
	}

	return c, nil
}

// promResponse is the standard Prometheus/VictoriaMetrics instant query response.
type promResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric map[string]string `json:"metric"`
			Value  [2]any            `json:"value"` // [timestamp, "value"]
		} `json:"result"`
	} `json:"data"`
	Error    string `json:"error,omitempty"`
	ErrorMsg string `json:"errorType,omitempty"`
}

// QueryNamespaceUsage fetches current CPU and Memory usage for a namespace.
// CPU query:  sum(rate(container_cpu_usage_seconds_total{namespace="X",container!=""}[5m]))
// Memory query: sum(container_memory_working_set_bytes{namespace="X",container!=""})
func (c *VMClient) QueryNamespaceUsage(ctx context.Context, namespace string) (cpu resource.Quantity, mem resource.Quantity, err error) {
	log := logf.FromContext(ctx).WithName("vmclient")

	// CPU usage (cores)
	cpuQuery := fmt.Sprintf(`sum(rate(container_cpu_usage_seconds_total{namespace="%s",container!=""}[5m]))`, namespace)
	cpuVal, err := c.queryScalar(ctx, cpuQuery)
	if err != nil {
		log.Error(err, "Failed to query CPU usage from VictoriaMetrics", "namespace", namespace)
		return cpu, mem, err
	}

	// Memory usage (bytes)
	memQuery := fmt.Sprintf(`sum(container_memory_working_set_bytes{namespace="%s",container!=""})`, namespace)
	memVal, err := c.queryScalar(ctx, memQuery)
	if err != nil {
		log.Error(err, "Failed to query Memory usage from VictoriaMetrics", "namespace", namespace)
		return cpu, mem, err
	}

	// Convert to resource.Quantity
	// CPU: value is in cores (float), convert to millicores
	cpuMillis := int64(cpuVal * 1000)
	cpu = *resource.NewMilliQuantity(cpuMillis, resource.DecimalSI)

	// Memory: value is in bytes
	mem = *resource.NewQuantity(int64(memVal), resource.BinarySI)

	return cpu, mem, nil
}

// Validate checks that the VictoriaMetrics endpoint is reachable and returns data.
func (c *VMClient) Validate(ctx context.Context) error {
	_, err := c.queryScalar(ctx, "up")
	if err != nil {
		return fmt.Errorf("VictoriaMetrics connectivity check failed: %w", err)
	}
	return nil
}

// queryScalar executes a PromQL instant query and returns a single scalar value.
func (c *VMClient) queryScalar(ctx context.Context, query string) (float64, error) {
	u, err := url.Parse(c.Endpoint)
	if err != nil {
		return 0, fmt.Errorf("invalid VictoriaMetrics endpoint: %w", err)
	}
	u.Path += "/api/v1/query"
	q := u.Query()
	q.Set("query", query)
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return 0, err
	}

	// Apply authentication
	if c.BearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.BearerToken)
	} else if c.Username != "" && c.Password != "" {
		req.SetBasicAuth(c.Username, c.Password)
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("failed to query VictoriaMetrics: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, fmt.Errorf("failed to read VictoriaMetrics response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("VictoriaMetrics returned status %d: %s", resp.StatusCode, string(body))
	}

	var promResp promResponse
	if err := json.Unmarshal(body, &promResp); err != nil {
		return 0, fmt.Errorf("failed to parse VictoriaMetrics response: %w", err)
	}

	if promResp.Status != "success" {
		return 0, fmt.Errorf("VictoriaMetrics query failed: %s (%s)", promResp.Error, promResp.ErrorMsg)
	}

	if len(promResp.Data.Result) == 0 {
		return 0, nil // No data = zero value
	}

	// Extract scalar value from first result
	valStr, ok := promResp.Data.Result[0].Value[1].(string)
	if !ok {
		return 0, fmt.Errorf("unexpected value type in VictoriaMetrics response")
	}

	val, err := strconv.ParseFloat(valStr, 64)
	if err != nil {
		return 0, fmt.Errorf("failed to parse VictoriaMetrics value: %w", err)
	}

	return val, nil
}
