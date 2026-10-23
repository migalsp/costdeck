package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const manifest = `apiVersion: apps/v1
kind: Deployment
metadata: {name: api}
spec:
  replicas: REPLICAS
  selector: {matchLabels: {app: api}}
  template:
    metadata: {labels: {app: api}}
    spec:
      containers: [{name: main, image: api, resources: {requests: {cpu: "1", memory: 1Gi}}}]
`

func files(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	base, head := filepath.Join(dir, "base.yaml"), filepath.Join(dir, "head.yaml")
	_ = os.WriteFile(base, []byte(strings.ReplaceAll(manifest, "REPLICAS", "1")), 0o600)
	_ = os.WriteFile(head, []byte(strings.ReplaceAll(manifest, "REPLICAS", "3")), 0o600)
	return base, head
}

func TestEstimateOffline(t *testing.T) {
	base, head := files(t)
	sides := []string{"--base", base, "--head", head}
	var out, errOut bytes.Buffer
	// 2 pods more of 1 core at 0.04 and 1 GiB at 0.01: 2 × 0.05 × 730 = 73.
	args := append(sides, "--cpu-hour", "0.04", "--gib-hour", "0.01", "--currency", "eur", "--fail-above", "50")
	code := runEstimate(args, &out, &errOut)
	if code != 2 || !strings.Contains(out.String(), "### Cost estimate: +€73.00 a month (+200%)") ||
		!strings.Contains(errOut.String(), "more than 50.00") {
		t.Fatalf("code %d\n%s\n%s", code, out.String(), errOut.String())
	}
	out.Reset()
	if code := runEstimate(append(sides, "--cloud", "aws", "--format", "json"), &out, io.Discard); code != 0 {
		t.Fatalf("json: %d", code)
	}
	var res struct {
		Delta    float64 `json:"delta"`
		Basis    string  `json:"basis"`
		Markdown string  `json:"markdown"`
	}
	if err := json.Unmarshal(out.Bytes(), &res); err != nil || res.Delta <= 0 || !strings.Contains(res.Basis, "aws") ||
		res.Markdown == "" {
		t.Errorf("json = %s (%v)", out.String(), err)
	}
	if code := runEstimate([]string{"--base", base}, io.Discard, io.Discard); code != 1 {
		t.Errorf("without --head: %d", code)
	}
	if code := runEstimate(append(sides, "--format", "yaml"), io.Discard, io.Discard); code != 1 {
		t.Errorf("an unknown format: %d", code)
	}
}

func TestEstimateWithServer(t *testing.T) {
	base, head := files(t)
	var got map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/estimate" || r.Header.Get("Authorization") != "Bearer cdk_ci" {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"currency":"USD","delta":12,"items":[],"markdown":"from the server\n"}`))
	}))
	defer srv.Close()
	sides := []string{"--base", base, "--head", head}
	var out bytes.Buffer
	args := append(sides, "--server", srv.URL+"/", "--token", "cdk_ci", "--namespace", "shop")
	if code := runEstimate(args, &out, io.Discard); code != 0 {
		t.Fatalf("code %d", code)
	}
	if out.String() != "from the server\n" || got["namespace"] != "shop" || !strings.Contains(got["head"], "replicas: 3") {
		t.Errorf("out %q, sent %v", out.String(), got)
	}
	var errOut bytes.Buffer
	code := runEstimate(append(sides, "--server", srv.URL, "--token", "wrong"), io.Discard, &errOut)
	if code != 1 || !strings.Contains(errOut.String(), "401") {
		t.Errorf("a refused token: %d %s", code, errOut.String())
	}
}
