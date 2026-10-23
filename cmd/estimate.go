package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/migalsp/costdeck-operator/internal/estimate"
	"github.com/migalsp/costdeck-operator/internal/pricing"
)

// runEstimate is "manager estimate": it prices the change between two sets of rendered
// manifests, at a Cost Deck's rates and schedules with --server, or offline at list-price
// estimates. CI runs it on a pull request and posts the Markdown as a comment.
func runEstimate(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("estimate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "Usage: manager estimate --base BASE.yaml --head HEAD.yaml [--server URL --token TOKEN]")
		_, _ = fmt.Fprintln(stderr, "Prices the change between two sets of manifests (helm template, kustomize build).")
		fs.PrintDefaults()
	}
	base := fs.String("base", "", "manifests before the change: a file, or - for stdin")
	head := fs.String("head", "", "manifests after the change: a file, or - for stdin")
	namespace := fs.String("namespace", "default", "namespace of objects without one")
	server := fs.String("server", os.Getenv("COSTDECK_URL"),
		"a Cost Deck to price with its rates and schedules (env COSTDECK_URL)")
	token := fs.String("token", os.Getenv("COSTDECK_TOKEN"), "a viewer API token of that Cost Deck (env COSTDECK_TOKEN)")
	format := fs.String("format", "markdown", "markdown or json")
	failAbove := fs.Float64("fail-above", 0, "exit 2 when the change adds more than this a month (0: never)")
	cloud := fs.String("cloud", "local", "offline: list-price estimates of aws, azure, gcp or local")
	cpuHour := fs.Float64("cpu-hour", 0, "offline: price of a core-hour (default: the cloud's estimate)")
	gibHour := fs.Float64("gib-hour", 0, "offline: price of a GiB-hour of memory (default: the cloud's estimate)")
	gpuHour := fs.Float64("gpu-hour", 0, "offline: price of a GPU-hour")
	storageMonth := fs.Float64("storage-gib-month", 0, "offline: price of a GiB-month of volumes")
	lbMonth := fs.Float64("lb-month", 0, "offline: price of a load balancer a month")
	nodes := fs.Int("nodes", 1, "offline: nodes a DaemonSet runs on")
	currency := fs.String("currency", "USD", "offline: currency of the prices")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	fail := func(err error) int {
		_, _ = fmt.Fprintln(stderr, "estimate:", err)
		return 1
	}
	if *base == "" || *head == "" {
		fs.Usage()
		return 1
	}
	if *format != "markdown" && *format != "json" {
		return fail(errors.New("--format is markdown or json"))
	}
	baseRaw, err := readManifests(*base)
	if err != nil {
		return fail(err)
	}
	headRaw, err := readManifests(*head)
	if err != nil {
		return fail(err)
	}

	var res estimate.Result
	var markdown string
	if *server != "" {
		res, markdown, err = remoteEstimate(*server, *token, baseRaw, headRaw, *namespace)
	} else {
		rates := pricing.HeuristicRates(*cloud)
		if *cpuHour > 0 || *gibHour > 0 {
			rates = pricing.Rates{CPUCoreHour: *cpuHour, MemoryGBHour: *gibHour, Currency: strings.ToUpper(*currency),
				Basis: "Rates given on the command line"}
		}
		rates.GPUHour = *gpuHour
		perGiB := *storageMonth
		res, err = estimate.Estimate(baseRaw, headRaw, *namespace, estimate.Prices{
			Rates: rates, LoadBalancerMonth: *lbMonth, Nodes: *nodes,
			Storage: func(string) float64 { return perGiB },
		})
		markdown = res.Markdown()
	}
	if err != nil {
		return fail(err)
	}
	if *format == "json" {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(struct {
			estimate.Result
			Markdown string `json:"markdown"`
		}{res, markdown})
	} else {
		_, _ = fmt.Fprint(stdout, markdown)
	}
	if *failAbove > 0 && res.Delta > *failAbove {
		_, _ = fmt.Fprintf(stderr, "estimate: the change adds %.2f %s a month, more than %.2f\n",
			res.Delta, res.Currency, *failAbove)
		return 2
	}
	return 0
}

func readManifests(path string) ([]byte, error) {
	if path == "-" {
		return io.ReadAll(io.LimitReader(os.Stdin, 64<<20))
	}
	return os.ReadFile(path)
}

// remoteEstimate asks a Cost Deck to price the change.
func remoteEstimate(server, token string, base, head []byte, namespace string) (estimate.Result, string, error) {
	body, err := json.Marshal(map[string]string{"base": string(base), "head": string(head), "namespace": namespace})
	if err != nil {
		return estimate.Result{}, "", err
	}
	endpoint := strings.TrimRight(server, "/") + "/api/estimate"
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return estimate.Result{}, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
	if err != nil {
		return estimate.Result{}, "", err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(io.LimitReader(res.Body, 500))
		return estimate.Result{}, "", fmt.Errorf("%s answered %s: %s",
			req.URL.Host, res.Status, strings.TrimSpace(string(detail)))
	}
	var out struct {
		estimate.Result
		Markdown string `json:"markdown"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return estimate.Result{}, "", err
	}
	return out.Result, out.Markdown, nil
}
