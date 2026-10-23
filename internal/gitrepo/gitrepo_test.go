package gitrepo

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
)

const file = `# The shop's API.
apiVersion: v1
kind: Service
metadata: {name: api}
spec: {ports: [{port: 80}]}
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
  namespace: shop # owned by the payments team
spec:
  template:
    spec:
      containers:
        - name: main
          image: api:1.2
          resources:
            requests:
              cpu: "2" # sized for the launch
              memory: 4Gi
            limits:
              memory: 4Gi
        - name: sidecar
          image: proxy
`

func TestEditRequests(t *testing.T) {
	out, ok, err := EditRequests([]byte(file), "Deployment", "shop", "api", map[string]Change{"main": {CPU: "250m", Memory: "512Mi"}, "sidecar": {CPU: "50m"}})
	if err != nil || !ok {
		t.Fatalf("%v %v", ok, err)
	}
	s := string(out)
	for _, want := range []string{"# The shop's API.", "# owned by the payments team", "cpu: 250m # sized for the launch", "memory: 512Mi", "limits:\n              memory: 4Gi",
		"- name: sidecar\n          image: proxy\n          resources:\n            requests:\n              cpu: 50m", "kind: Service"} {
		if !strings.Contains(s, want) {
			t.Errorf("no %q in:\n%s", want, s)
		}
	}
	if _, ok, _ := EditRequests([]byte(file), "Deployment", "other", "api", map[string]Change{"main": {CPU: "1"}}); ok {
		t.Error("a manifest of another namespace was edited")
	}
	if _, ok, _ := EditRequests([]byte(file), "StatefulSet", "shop", "api", map[string]Change{"main": {CPU: "1"}}); ok {
		t.Error("a manifest of another kind was edited")
	}
	if _, _, err := EditRequests([]byte("kind: [broken"), "Deployment", "shop", "api", nil); err == nil {
		t.Error("broken YAML was accepted")
	}
}

// fakeGit records what a provider is asked and answers like the real API.
type fakeGit struct {
	mu    sync.Mutex
	calls []string
	puts  map[string]map[string]any
}

func (f *fakeGit) record(r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	if f.puts == nil {
		f.puts = map[string]map[string]any{}
	}
	if body != nil {
		f.puts[r.Method+" "+r.URL.Path] = body
	}
}

var content64 = base64.StdEncoding.EncodeToString([]byte(file))

func github_(t *testing.T, f *fakeGit) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer t0k" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f.record(r)
		switch r.Method + " " + r.URL.Path {
		case "GET /repos/acme/apps":
			_, _ = w.Write([]byte(`{}`))
		case "GET /repos/acme/apps/git/trees/main":
			_, _ = w.Write([]byte(`{"tree":[{"path":"README.md","type":"blob"},{"path":"deploy/shop/api.yaml","type":"blob"},{"path":"other/x.yaml","type":"blob"}]}`))
		case "GET /repos/acme/apps/contents/deploy/shop/api.yaml":
			_, _ = w.Write([]byte(`{"content":"` + content64 + `","sha":"abc"}`))
		case "GET /repos/acme/apps/git/ref/heads/main":
			_, _ = w.Write([]byte(`{"object":{"sha":"base1"}}`))
		case "POST /repos/acme/apps/git/refs", "PUT /repos/acme/apps/contents/deploy/shop/api.yaml":
			_, _ = w.Write([]byte(`{}`))
		case "POST /repos/acme/apps/pulls":
			_, _ = w.Write([]byte(`{"html_url":"https://github.com/acme/apps/pull/7"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestGitHub(t *testing.T) {
	f := &fakeGit{}
	srv := github_(t, f)
	r, err := New(finopsv1.GitRepositoryConfig{Provider: "github", Repository: "acme/apps", APIURL: srv.URL}, "t0k", srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := r.Check(ctx); err != nil {
		t.Fatal(err)
	}
	path, edited, rev, err := Find(ctx, r, "deploy", "Deployment", "shop", "api", map[string]Change{"main": {CPU: "250m"}})
	if err != nil || path != "deploy/shop/api.yaml" || rev != "abc" || !strings.Contains(string(edited), "cpu: 250m") {
		t.Fatalf("find = %s %s %v", path, rev, err)
	}
	url, err := r.Propose(ctx, Proposal{Branch: "costdeck/rightsize-shop-api", Path: path, Content: edited, Revision: rev, Message: "m", Title: "Right-size", Body: "b"})
	if err != nil || url != "https://github.com/acme/apps/pull/7" {
		t.Fatalf("propose = %s, %v", url, err)
	}
	if ref := f.puts["POST /repos/acme/apps/git/refs"]; ref["ref"] != "refs/heads/costdeck/rightsize-shop-api" || ref["sha"] != "base1" {
		t.Errorf("branch = %v", ref)
	}
	put := f.puts["PUT /repos/acme/apps/contents/deploy/shop/api.yaml"]
	raw, _ := base64.StdEncoding.DecodeString(put["content"].(string))
	if put["sha"] != "abc" || put["branch"] != "costdeck/rightsize-shop-api" || !strings.Contains(string(raw), "cpu: 250m") {
		t.Errorf("commit = %v", put)
	}
	if pr := f.puts["POST /repos/acme/apps/pulls"]; pr["base"] != DefaultBranch || pr["head"] != "costdeck/rightsize-shop-api" {
		t.Errorf("pull request = %v", pr)
	}
	if _, _, _, err := Find(ctx, r, "deploy", "Deployment", "shop", "missing", nil); err != ErrNotFound {
		t.Errorf("a workload in no manifest: %v", err)
	}
	bad, _ := New(finopsv1.GitRepositoryConfig{Provider: "github", Repository: "acme/apps", APIURL: srv.URL}, "wrong", srv.Client())
	if err := bad.Check(ctx); err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("a wrong token: %v", err)
	}
}

func TestGiteaAndGitLab(t *testing.T) {
	ctx := context.Background()
	f := &fakeGit{}
	gitea := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "token t0k" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f.record(r)
		switch r.Method + " " + r.URL.Path {
		case "GET /api/v1/repos/acme/apps/git/trees/main":
			_, _ = w.Write([]byte(`{"tree":[{"path":"api.yaml","type":"blob"}]}`))
		case "GET /api/v1/repos/acme/apps/contents/api.yaml":
			_, _ = w.Write([]byte(`{"content":"` + content64 + `","sha":"g1"}`))
		case "PUT /api/v1/repos/acme/apps/contents/api.yaml":
			_, _ = w.Write([]byte(`{}`))
		case "POST /api/v1/repos/acme/apps/pulls":
			_, _ = w.Write([]byte(`{"html_url":"http://gitea/acme/apps/pulls/1"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer gitea.Close()
	r, _ := New(finopsv1.GitRepositoryConfig{Provider: "gitea", Repository: "acme/apps", APIURL: gitea.URL + "/api/v1"}, "t0k", gitea.Client())
	path, edited, rev, err := Find(ctx, r, "", "Deployment", "shop", "api", map[string]Change{"main": {Memory: "1Gi"}})
	if err != nil {
		t.Fatal(err)
	}
	if url, err := r.Propose(ctx, Proposal{Branch: "b", Path: path, Content: edited, Revision: rev, Title: "t"}); err != nil || url != "http://gitea/acme/apps/pulls/1" {
		t.Fatalf("gitea propose = %s, %v", url, err)
	}
	if put := f.puts["PUT /api/v1/repos/acme/apps/contents/api.yaml"]; put["branch"] != DefaultBranch || put["new_branch"] != "b" || put["sha"] != "g1" {
		t.Errorf("gitea commit = %v", put)
	}
	if _, err := New(finopsv1.GitRepositoryConfig{Provider: "gitea", Repository: "acme/apps"}, "t", nil); err == nil {
		t.Error("a Gitea without an API URL was accepted")
	}

	g := &fakeGit{}
	gitlab := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("PRIVATE-TOKEN") != "t0k" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		g.record(r)
		switch {
		case r.URL.Path == "/api/v4/projects/acme/apps/repository/tree" && r.URL.Query().Get("page") == "1":
			w.Header().Set("X-Next-Page", "2")
			_, _ = w.Write([]byte(`[{"path":"deploy/a.txt","type":"blob"}]`))
		case r.URL.Path == "/api/v4/projects/acme/apps/repository/tree":
			_, _ = w.Write([]byte(`[{"path":"deploy/api.yml","type":"blob"}]`))
		case r.URL.Path == "/api/v4/projects/acme/apps/repository/files/deploy/api.yml":
			_, _ = w.Write([]byte(`{"content":"` + content64 + `","blob_id":"l1"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v4/projects/acme/apps/repository/commits":
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v4/projects/acme/apps/merge_requests":
			_, _ = w.Write([]byte(`{"web_url":"https://gitlab.com/acme/apps/-/merge_requests/3"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer gitlab.Close()
	r, _ = New(finopsv1.GitRepositoryConfig{Provider: "gitlab", Repository: "acme/apps", APIURL: gitlab.URL + "/api/v4", BaseBranch: "trunk"}, "t0k", gitlab.Client())
	path, edited, _, err = Find(ctx, r, "deploy", "Deployment", "shop", "api", map[string]Change{"main": {CPU: "1"}})
	if err != nil || path != "deploy/api.yml" {
		t.Fatalf("gitlab find = %s, %v (%v)", path, err, g.calls)
	}
	if url, err := r.Propose(ctx, Proposal{Branch: "b", Path: path, Content: edited, Title: "t"}); err != nil || !strings.HasSuffix(url, "/merge_requests/3") {
		t.Fatalf("gitlab propose = %s, %v", url, err)
	}
	if c := g.puts["POST /api/v4/projects/acme/apps/repository/commits"]; c["start_branch"] != "trunk" || c["branch"] != "b" {
		t.Errorf("gitlab commit = %v", c)
	}
	if mr := g.puts["POST /api/v4/projects/acme/apps/merge_requests"]; mr["target_branch"] != "trunk" {
		t.Errorf("gitlab merge request = %v", mr)
	}
}
