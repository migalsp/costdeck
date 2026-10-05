package metrics

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
)

// VMOptions configures a PromQL client.
type VMOptions struct {
	// Endpoint is the base URL that /api/v1/query is appended to, e.g.
	// http://vmsingle.monitoring.svc:8428 or http://vmselect:8481/select/0/prometheus.
	Endpoint string
	// LabelSelector is added to every series selector, e.g. `cluster="prod-eu"`, so a
	// VictoriaMetrics instance shared by several clusters only returns this cluster's data.
	LabelSelector string

	BearerToken string
	Username    string
	Password    string

	// CACert is an optional PEM bundle trusted in addition to the system roots.
	CACert []byte
	// InsecureSkipVerify disables TLS verification (self-signed endpoints).
	InsecureSkipVerify bool
}

// VMClient queries a PromQL-compatible endpoint (VictoriaMetrics or Prometheus).
type VMClient struct {
	base          *url.URL
	labelSelector string
	opts          VMOptions
	http          *http.Client
}

// labelMatchers validates a comma separated list of PromQL label matchers.
var labelMatchers = regexp.MustCompile(`^\s*[a-zA-Z_][a-zA-Z0-9_]*\s*(=|!=|=~|!~)\s*"(?:[^"\\]|\\.)*"\s*(,\s*[a-zA-Z_][a-zA-Z0-9_]*\s*(=|!=|=~|!~)\s*"(?:[^"\\]|\\.)*"\s*)*$`)

// NewVMClient validates the options and builds a client.
func NewVMClient(opts VMOptions) (*VMClient, error) {
	base, err := NormalizeEndpoint(opts.Endpoint)
	if err != nil {
		return nil, err
	}
	selector := strings.TrimSpace(opts.LabelSelector)
	selector = strings.TrimSuffix(strings.TrimPrefix(selector, "{"), "}")
	if selector != "" && !labelMatchers.MatchString(selector) {
		return nil, fmt.Errorf(`invalid label selector %q: expected PromQL matchers such as cluster="prod"`, opts.LabelSelector)
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if len(opts.CACert) > 0 {
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(opts.CACert) {
			return nil, errors.New("CA certificate is not valid PEM")
		}
		tlsCfg.RootCAs = pool
	}
	if opts.InsecureSkipVerify {
		tlsCfg.InsecureSkipVerify = true //nolint:gosec // Explicit, admin-controlled opt-in for self-signed endpoints.
	}
	transport.TLSClientConfig = tlsCfg

	return &VMClient{
		base:          base,
		labelSelector: selector,
		opts:          opts,
		http:          &http.Client{Timeout: 15 * time.Second, Transport: transport},
	}, nil
}

// NormalizeEndpoint turns the URL a user pasted into the base PromQL URL. It tolerates the
// common mistakes: a trailing slash, or the full /api/v1/query path copied from Grafana.
func NormalizeEndpoint(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("endpoint is empty")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid endpoint %q: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("invalid endpoint %q: scheme must be http or https", raw)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("invalid endpoint %q: host is missing", raw)
	}
	p := strings.TrimRight(u.Path, "/")
	for _, suffix := range []string{"/api/v1/query_range", "/api/v1/query", "/api/v1"} {
		p = strings.TrimSuffix(p, suffix)
	}
	u.Path = p
	u.RawPath = ""
	u.Fragment = ""
	return u, nil
}

// Endpoint returns the normalized base URL.
func (c *VMClient) Endpoint() string { return c.base.String() }

// selector renders a series selector for the given metric, always excluding the
// pod-level cgroup (container="") and the pause container.
func (c *VMClient) selector(metric, namespace string) string {
	matchers := []string{`container!=""`, `container!="POD"`}
	if namespace != "" {
		matchers = append([]string{fmt.Sprintf("namespace=%q", namespace)}, matchers...)
	}
	if c.labelSelector != "" {
		matchers = append(matchers, c.labelSelector)
	}
	return metric + "{" + strings.Join(matchers, ",") + "}"
}

func (c *VMClient) cpuExpr(namespace string) string {
	return fmt.Sprintf("rate(%s[5m])", c.selector("container_cpu_usage_seconds_total", namespace))
}

func (c *VMClient) memExpr(namespace string) string {
	return c.selector("container_memory_working_set_bytes", namespace)
}

// NamespaceUsage implements Source.
func (c *VMClient) NamespaceUsage(ctx context.Context, namespace string) (Usage, error) {
	cpu, err := c.scalar(ctx, "sum("+c.cpuExpr(namespace)+")")
	if err != nil {
		return Usage{}, fmt.Errorf("query namespace CPU: %w", err)
	}
	mem, err := c.scalar(ctx, "sum("+c.memExpr(namespace)+")")
	if err != nil {
		return Usage{}, fmt.Errorf("query namespace memory: %w", err)
	}
	return newUsage(cpu, mem), nil
}

// PodUsage implements Source.
func (c *VMClient) PodUsage(ctx context.Context, namespace string) (map[string]Usage, error) {
	cpu, err := c.vector(ctx, "sum by (pod) ("+c.cpuExpr(namespace)+")", "pod")
	if err != nil {
		return nil, fmt.Errorf("query pod CPU: %w", err)
	}
	mem, err := c.vector(ctx, "sum by (pod) ("+c.memExpr(namespace)+")", "pod")
	if err != nil {
		return nil, fmt.Errorf("query pod memory: %w", err)
	}
	usage := make(map[string]Usage, len(cpu))
	for pod, v := range cpu {
		usage[pod] = newUsage(v, mem[pod])
	}
	for pod, v := range mem {
		if _, ok := usage[pod]; !ok {
			usage[pod] = newUsage(0, v)
		}
	}
	return usage, nil
}

// Name implements Source.
func (c *VMClient) Name() string { return SourceVictoriaMetrics }

// Validate checks connectivity and, more importantly, that the container metrics CostDeck
// needs are actually there. "Connected but no cAdvisor data" is the most common reason a
// configured integration shows nothing, so it gets its own explicit error.
func (c *VMClient) Validate(ctx context.Context) error {
	n, err := c.scalar(ctx, "count("+c.selector("container_cpu_usage_seconds_total", "")+")")
	if err != nil {
		return err
	}
	if n == 0 {
		hint := "is the kubelet/cAdvisor endpoint scraped into this VictoriaMetrics?"
		if c.labelSelector != "" {
			hint = fmt.Sprintf("does the label selector {%s} match your series?", c.labelSelector)
		}
		return fmt.Errorf("connected, but no container_cpu_usage_seconds_total series were found: %s", hint)
	}
	return nil
}

func newUsage(cores, bytes float64) Usage {
	return Usage{
		CPU:    *resource.NewMilliQuantity(int64(math.Round(cores*1000)), resource.DecimalSI),
		Memory: *resource.NewQuantity(int64(bytes), resource.BinarySI),
	}
}

// promDuration renders a duration in PromQL notation (whole minutes, hours or days).
func promDuration(d time.Duration) string {
	switch {
	case d <= 0:
		return "1h"
	case d%(24*time.Hour) == 0:
		return fmt.Sprintf("%dd", d/(24*time.Hour))
	case d%time.Hour == 0:
		return fmt.Sprintf("%dh", d/time.Hour)
	default:
		return fmt.Sprintf("%dm", int(math.Ceil(d.Minutes())))
	}
}

// promResponse is the Prometheus HTTP API instant query response.
type promResponse struct {
	Status    string `json:"status"`
	ErrorType string `json:"errorType,omitempty"`
	Error     string `json:"error,omitempty"`
	Data      struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric map[string]string `json:"metric"`
			Value  [2]any            `json:"value"` // [timestamp, "value"]
		} `json:"result"`
	} `json:"data"`
}

// scalar runs an instant query expected to return at most one series.
func (c *VMClient) scalar(ctx context.Context, query string) (float64, error) {
	resp, err := c.query(ctx, query)
	if err != nil {
		return 0, err
	}
	if len(resp.Data.Result) == 0 {
		return 0, nil
	}
	return sampleValue(resp.Data.Result[0].Value)
}

// vector runs an instant query and returns the value of each series keyed by one label.
func (c *VMClient) vector(ctx context.Context, query, label string) (map[string]float64, error) {
	resp, err := c.query(ctx, query)
	if err != nil {
		return nil, err
	}
	out := make(map[string]float64, len(resp.Data.Result))
	for _, r := range resp.Data.Result {
		v, err := sampleValue(r.Value)
		if err != nil {
			return nil, err
		}
		out[r.Metric[label]] = v
	}
	return out, nil
}

func sampleValue(sample [2]any) (float64, error) {
	s, ok := sample[1].(string)
	if !ok {
		return 0, fmt.Errorf("unexpected sample value type %T", sample[1])
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("parse sample value %q: %w", s, err)
	}
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, nil
	}
	return v, nil
}

func (c *VMClient) query(ctx context.Context, query string) (*promResponse, error) {
	u := *c.base
	u.Path += "/api/v1/query"
	q := u.Query() // Keep parameters from the endpoint, e.g. VictoriaMetrics extra_label.
	q.Set("query", query)
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	switch {
	case c.opts.BearerToken != "":
		req.Header.Set("Authorization", "Bearer "+c.opts.BearerToken)
	case c.opts.Username != "":
		req.SetBasicAuth(c.opts.Username, c.opts.Password)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request %s: %w", c.base.Redacted(), err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, fmt.Errorf("%s rejected the credentials (HTTP %d)", c.base.Redacted(), resp.StatusCode)
	case http.StatusNotFound:
		return nil, fmt.Errorf("%s/api/v1/query returned 404: for a VictoriaMetrics cluster the endpoint must include /select/<accountID>/prometheus", c.base.Redacted())
	default:
		return nil, fmt.Errorf("%s returned HTTP %d: %s", c.base.Redacted(), resp.StatusCode, truncate(string(body), 300))
	}

	var pr promResponse
	if err := json.Unmarshal(body, &pr); err != nil {
		return nil, fmt.Errorf("response is not a Prometheus API payload (is the endpoint a PromQL URL?): %w", err)
	}
	if pr.Status != "success" {
		return nil, fmt.Errorf("query failed: %s: %s", pr.ErrorType, pr.Error)
	}
	return &pr, nil
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
