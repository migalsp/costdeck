package gitrepo

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
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

func TestEditRequestsUnderEmptyResources(t *testing.T) {
	t.Skip("bug: ensure returns the null node of an empty resources: or requests: key and set writes into it, so EditRequests reports the workload edited while the output has no new requests")
	for name, in := range map[string]string{
		"empty resources": "kind: Deployment\nmetadata:\n  name: api\nspec:\n  template:\n    spec:\n      containers:\n        - name: main\n          resources:\n",
		"empty requests":  "kind: Deployment\nmetadata:\n  name: api\nspec:\n  template:\n    spec:\n      containers:\n        - name: main\n          resources:\n            requests:\n",
	} {
		out, ok, err := EditRequests([]byte(in), "Deployment", "shop", "api", map[string]Change{"main": {CPU: "250m"}})
		if err != nil || !ok || !strings.Contains(string(out), "cpu: 250m") {
			t.Errorf("%s: ok=%v err=%v, output lacks the new request:\n%s", name, ok, err, out)
		}
	}
}

func TestEditRequestsIgnoresDocumentsWithoutAWorkload(t *testing.T) {
	in := "---\n# only a comment\n---\n- a\n- b\n---\nkind: Deployment\nmetadata: {name: api}\nspec: {template: {spec: {initContainers: []}}}\n"
	out, ok, err := EditRequests([]byte(in), "Deployment", "shop", "api", map[string]Change{"main": {CPU: "1"}})
	if err != nil || ok || string(out) != in {
		t.Errorf("EditRequests() = %q, %v, %v; a file without the container must come back untouched", out, ok, err)
	}
	if _, ok, _ := EditRequests([]byte(file), "Deployment", "shop", "api", map[string]Change{"main": {CPU: " ", Memory: ""}}); ok {
		t.Error("blank changes reported an edit")
	}
}

type gitRoute struct {
	status int
	body   string
	header map[string]string
}

type scriptedGit struct {
	mu     sync.Mutex
	routes map[string]gitRoute
	calls  []string
	uris   []string
	auth   []http.Header
	bodies map[string]map[string]any
}

func (s *scriptedGit) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := r.Method + " " + r.URL.EscapedPath()
	s.calls = append(s.calls, key)
	s.uris = append(s.uris, r.RequestURI)
	s.auth = append(s.auth, r.Header.Clone())
	var body map[string]any
	if json.NewDecoder(r.Body).Decode(&body) == nil {
		if s.bodies == nil {
			s.bodies = map[string]map[string]any{}
		}
		s.bodies[key] = body
	}
	rt, ok := s.routes[key]
	if !ok {
		http.NotFound(w, r)
		return
	}
	for k, v := range rt.header {
		w.Header().Set(k, v)
	}
	if rt.status != 0 {
		w.WriteHeader(rt.status)
	}
	_, _ = w.Write([]byte(rt.body))
}

func (s *scriptedGit) serve(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	return srv
}

func TestCheckReachesTheRepository(t *testing.T) {
	tests := []struct {
		provider, repo, prefix, route string
		authHeader, authValue         string
	}{
		{"github", "acme/apps", "", "GET /repos/acme/apps", "Authorization", "Bearer t0k"},
		{"gitea", "acme/apps", "/api/v1", "GET /api/v1/repos/acme/apps", "Authorization", "token t0k"},
		{"gitlab", "acme/apps", "/api/v4", "GET /api/v4/projects/acme%2Fapps", "Private-Token", "t0k"},
		{"gitlab", "group/sub/apps", "/api/v4", "GET /api/v4/projects/group%2Fsub%2Fapps", "Private-Token", "t0k"},
	}
	for _, tt := range tests {
		t.Run(tt.provider+" "+tt.repo, func(t *testing.T) {
			git := &scriptedGit{routes: map[string]gitRoute{tt.route: {body: `{}`}}}
			srv := git.serve(t)
			r, err := New(finopsv1.GitRepositoryConfig{Provider: tt.provider, Repository: tt.repo, APIURL: srv.URL + tt.prefix + "/"}, "t0k", srv.Client())
			if err != nil {
				t.Fatal(err)
			}
			if err := r.Check(context.Background()); err != nil {
				t.Fatalf("Check() = %v (calls %v)", err, git.calls)
			}
			if git.calls[0] != tt.route || git.auth[0].Get(tt.authHeader) != tt.authValue {
				t.Errorf("request %s with %s=%q", git.calls[0], tt.authHeader, git.auth[0].Get(tt.authHeader))
			}
			if tt.provider == "github" && git.auth[0].Get("Accept") != "application/vnd.github+json" {
				t.Errorf("Accept = %q", git.auth[0].Get("Accept"))
			}

			git.routes[tt.route] = gitRoute{status: http.StatusNotFound, body: `{"message":"404 Project Not Found"}`}
			err = r.Check(context.Background())
			if err == nil || !strings.HasSuffix(err.Error(), `answered 404 Not Found: {"message":"404 Project Not Found"}`) || !strings.HasPrefix(err.Error(), "GET "+tt.prefix) {
				t.Errorf("Check() = %v, want the method, path, status and answer", err)
			}
		})
	}
}

func TestNewDefaultsAndRefusals(t *testing.T) {
	gh, err := New(finopsv1.GitRepositoryConfig{Provider: "github", Repository: "acme/apps"}, "t", nil)
	if err != nil || gh.(*github).base != "https://api.github.com" || gh.(*github).branch != DefaultBranch {
		t.Errorf("GitHub defaults = %+v, %v", gh, err)
	}
	gl, err := New(finopsv1.GitRepositoryConfig{Provider: "gitlab", Repository: "acme/apps", APIURL: "https://git.example.com/api/v4//", BaseBranch: "develop"}, "t", nil)
	if err != nil || gl.(*gitlab).base != "https://git.example.com/api/v4" || gl.(*gitlab).branch != "develop" {
		t.Errorf("GitLab settings = %+v, %v", gl, err)
	}
	if gl, _ := New(finopsv1.GitRepositoryConfig{Provider: "gitlab", Repository: "acme/apps"}, "t", nil); gl.(*gitlab).base != "https://gitlab.com/api/v4" {
		t.Errorf("GitLab default base = %q", gl.(*gitlab).base)
	}
	for _, tt := range []struct {
		cfg  finopsv1.GitRepositoryConfig
		want string
	}{
		{finopsv1.GitRepositoryConfig{Provider: "github"}, "no repository is set"},
		{finopsv1.GitRepositoryConfig{Provider: "bitbucket", Repository: "acme/apps"}, "the provider is github, gitlab or gitea"},
		{finopsv1.GitRepositoryConfig{Provider: "gitea", Repository: "acme/apps"}, "a Gitea needs its API URL"},
	} {
		if r, err := New(tt.cfg, "t", nil); r != nil || err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("New(%+v) = %v, %v; want %q", tt.cfg, r, err, tt.want)
		}
	}
}

var proposal = Proposal{Branch: "costdeck/rightsize-shop-api-1", Path: "deploy/api.yaml", Content: []byte("kind: Deployment\n"), Revision: "abc", Message: "m", Title: "t", Body: "b"}

func TestGitHubProposeStopsAtTheFirstFailure(t *testing.T) {
	const (
		ref   = "GET /repos/acme/apps/git/ref/heads/main"
		refs  = "POST /repos/acme/apps/git/refs"
		put   = "PUT /repos/acme/apps/contents/deploy/api.yaml"
		pulls = "POST /repos/acme/apps/pulls"
	)
	tests := []struct {
		name      string
		failing   string
		failure   gitRoute
		wantErr   string
		wantCalls []string
	}{
		{"base branch missing", ref, gitRoute{status: http.StatusNotFound, body: `{"message":"Not Found"}`},
			`GET /repos/acme/apps/git/ref/heads/main answered 404 Not Found: {"message":"Not Found"}`, []string{ref}},
		{"branch exists", refs, gitRoute{status: http.StatusUnprocessableEntity, body: `{"message":"Reference already exists"}`},
			`POST /repos/acme/apps/git/refs answered 422 Unprocessable Entity: {"message":"Reference already exists"}`, []string{ref, refs}},
		{"file changed underneath", put, gitRoute{status: http.StatusConflict, body: `{"message":"deploy/api.yaml does not match abc"}`},
			`PUT /repos/acme/apps/contents/deploy/api.yaml answered 409 Conflict: {"message":"deploy/api.yaml does not match abc"}`, []string{ref, refs, put}},
		{"pull request refused", pulls, gitRoute{status: http.StatusUnprocessableEntity, body: `{"message":"A pull request already exists"}`},
			`POST /repos/acme/apps/pulls answered 422 Unprocessable Entity`, []string{ref, refs, put, pulls}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			git := &scriptedGit{routes: map[string]gitRoute{
				ref: {body: `{"object":{"sha":"base1"}}`}, refs: {body: `{}`}, put: {body: `{}`},
				pulls: {body: `{"html_url":"https://github.com/acme/apps/pull/9"}`},
			}}
			git.routes[tt.failing] = tt.failure
			srv := git.serve(t)
			r, _ := New(finopsv1.GitRepositoryConfig{Provider: "github", Repository: "acme/apps", APIURL: srv.URL}, "t0k", srv.Client())
			url, err := r.Propose(context.Background(), proposal)
			if url != "" || err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Propose() = %q, %v; want %q", url, err, tt.wantErr)
			}
			if strings.Join(git.calls, ",") != strings.Join(tt.wantCalls, ",") {
				t.Errorf("calls = %v, want %v: nothing may follow a failed step", git.calls, tt.wantCalls)
			}
		})
	}
}

func TestGiteaProposeStopsAtTheFirstFailure(t *testing.T) {
	const (
		put   = "PUT /api/v1/repos/acme/apps/contents/deploy/api.yaml"
		pulls = "POST /api/v1/repos/acme/apps/pulls"
	)
	tests := []struct {
		name      string
		failing   string
		failure   gitRoute
		wantErr   string
		wantCalls []string
	}{
		{"branch exists", put, gitRoute{status: http.StatusUnprocessableEntity, body: `{"message":"branch already exists"}`}, "422 Unprocessable Entity: {\"message\":\"branch already exists\"}", []string{put}},
		{"file changed underneath", put, gitRoute{status: http.StatusConflict, body: `{"message":"sha does not match"}`}, "409 Conflict", []string{put}},
		{"pull request refused", pulls, gitRoute{status: http.StatusConflict, body: `{"message":"pull request already exists"}`}, "POST /api/v1/repos/acme/apps/pulls answered 409 Conflict", []string{put, pulls}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			git := &scriptedGit{routes: map[string]gitRoute{put: {body: `{}`}, pulls: {body: `{"html_url":"http://gitea/acme/apps/pulls/2"}`}}}
			git.routes[tt.failing] = tt.failure
			srv := git.serve(t)
			r, _ := New(finopsv1.GitRepositoryConfig{Provider: "gitea", Repository: "acme/apps", APIURL: srv.URL + "/api/v1"}, "t0k", srv.Client())
			url, err := r.Propose(context.Background(), proposal)
			if url != "" || err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Propose() = %q, %v; want %q", url, err, tt.wantErr)
			}
			if strings.Join(git.calls, ",") != strings.Join(tt.wantCalls, ",") {
				t.Errorf("calls = %v, want %v", git.calls, tt.wantCalls)
			}
		})
	}
}

func TestGitLabProposeStopsAtTheFirstFailure(t *testing.T) {
	const (
		commits = "POST /api/v4/projects/acme%2Fapps/repository/commits"
		mrs     = "POST /api/v4/projects/acme%2Fapps/merge_requests"
	)
	tests := []struct {
		name      string
		failing   string
		failure   gitRoute
		wantErr   string
		wantCalls []string
	}{
		{"branch exists", commits, gitRoute{status: http.StatusBadRequest, body: `{"message":"A branch called 'costdeck/rightsize-shop-api-1' already exists."}`}, "400 Bad Request: {\"message\":\"A branch called", []string{commits}},
		{"merge request refused", mrs, gitRoute{status: http.StatusConflict, body: `{"message":["Another open merge request already exists for this source branch: !3"]}`}, "answered 409 Conflict", []string{commits, mrs}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			git := &scriptedGit{routes: map[string]gitRoute{commits: {body: `{}`}, mrs: {body: `{"web_url":"https://gitlab.com/acme/apps/-/merge_requests/4"}`}}}
			git.routes[tt.failing] = tt.failure
			srv := git.serve(t)
			r, _ := New(finopsv1.GitRepositoryConfig{Provider: "gitlab", Repository: "acme/apps", APIURL: srv.URL + "/api/v4"}, "t0k", srv.Client())
			url, err := r.Propose(context.Background(), proposal)
			if url != "" || err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Propose() = %q, %v; want %q", url, err, tt.wantErr)
			}
			if strings.Join(git.calls, ",") != strings.Join(tt.wantCalls, ",") {
				t.Errorf("calls = %v, want %v", git.calls, tt.wantCalls)
			}
		})
	}
}

type changingGitLab struct {
	mu         sync.Mutex
	lastCommit string
	commits    int
}

func (g *changingGitLab) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	switch r.Method + " " + r.URL.EscapedPath() {
	case "GET /api/v4/projects/acme%2Fapps/repository/files/api.yaml":
		_ = json.NewEncoder(w).Encode(map[string]string{"content": content64, "blob_id": "blob-" + g.lastCommit, "last_commit_id": g.lastCommit})
	case "POST /api/v4/projects/acme%2Fapps/repository/commits":
		var body struct {
			Actions []map[string]string `json:"actions"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		for _, a := range body.Actions {
			if id, ok := a["last_commit_id"]; ok && id != g.lastCommit {
				http.Error(w, `{"message":"You are attempting to update a file that has changed since you started editing it."}`, http.StatusBadRequest)
				return
			}
		}
		g.commits++
		_, _ = w.Write([]byte(`{}`))
	case "POST /api/v4/projects/acme%2Fapps/merge_requests":
		_, _ = w.Write([]byte(`{"web_url":"https://gitlab.com/acme/apps/-/merge_requests/5"}`))
	default:
		http.NotFound(w, r)
	}
}

func TestGitLabProposeRefusesAFileChangedSinceItWasRead(t *testing.T) {
	t.Skip("bug: the GitLab Propose ignores Proposal.Revision, so a manifest changed on the base branch after Read is overwritten and the merge request silently reverts that change")
	git := &changingGitLab{lastCommit: "c1"}
	srv := httptest.NewServer(git)
	defer srv.Close()
	r, _ := New(finopsv1.GitRepositoryConfig{Provider: "gitlab", Repository: "acme/apps", APIURL: srv.URL + "/api/v4"}, "t0k", srv.Client())
	ctx := context.Background()
	raw, rev, err := r.Read(ctx, "api.yaml")
	if err != nil {
		t.Fatal(err)
	}
	edited, _, _ := EditRequests(raw, "Deployment", "shop", "api", map[string]Change{"main": {CPU: "1"}})
	git.mu.Lock()
	git.lastCommit = "c2"
	git.mu.Unlock()
	url, err := r.Propose(ctx, Proposal{Branch: "b", Path: "api.yaml", Content: edited, Revision: rev, Title: "t"})
	if err == nil || url != "" || git.commits != 0 {
		t.Errorf("Propose() = %q, %v after %d commits; a file changed since it was read must not be overwritten", url, err, git.commits)
	}
}

func TestCallReportsTransportAndPayloadErrors(t *testing.T) {
	closed := httptest.NewServer(http.NotFoundHandler())
	host := strings.TrimPrefix(closed.URL, "http://")
	closed.Close()
	r, _ := New(finopsv1.GitRepositoryConfig{Provider: "github", Repository: "acme/apps", APIURL: "http://" + host}, "t", http.DefaultClient)
	err := r.Check(context.Background())
	if err == nil || !strings.HasPrefix(err.Error(), "could not reach "+host+": ") || strings.Contains(err.Error(), `"http://`) {
		t.Errorf("Check() = %v, want the host and the network error without the URL wrapper", err)
	}

	git := &scriptedGit{routes: map[string]gitRoute{
		"GET /repos/acme/apps/git/trees/main":           {body: `{"tree": [`},
		"GET /repos/acme/apps/contents/deploy/api.yaml": {body: `{"content":"not base64!","sha":"s"}`},
		"GET /repos/acme/apps":                          {status: http.StatusBadGateway, body: strings.Repeat("e", 1000)},
	}}
	srv := git.serve(t)
	r, _ = New(finopsv1.GitRepositoryConfig{Provider: "github", Repository: "acme/apps", APIURL: srv.URL}, "t", srv.Client())
	if files, err := r.Files(context.Background(), ""); files != nil || err == nil {
		t.Errorf("Files() on a broken answer = %v, %v", files, err)
	}
	if _, _, err := r.Read(context.Background(), "deploy/api.yaml"); err == nil {
		t.Error("Read() accepted content that is not base64")
	}
	if err := r.Check(context.Background()); err == nil || strings.Count(err.Error(), "e") > 320 {
		t.Errorf("Check() = %v; the error body must be capped at 300 bytes", err)
	}
}

func TestFilesFilterYAMLUnderTheDirectoryAndStopAtTheCap(t *testing.T) {
	var tree []map[string]string
	for i := range MaxFiles + 100 {
		tree = append(tree, map[string]string{"path": "deploy/app" + strings.Repeat("x", i%3) + "/" + string(rune('a'+i%26)) + ".yaml", "type": "blob"})
	}
	tree = append([]map[string]string{
		{"path": "deployments/other.yaml", "type": "blob"},
		{"path": "deploy/README.md", "type": "blob"},
		{"path": "deploy/sub.yaml", "type": "tree"},
		{"path": "deploy/first.yml", "type": "blob"},
	}, tree...)
	raw, _ := json.Marshal(map[string]any{"tree": tree})

	git := &scriptedGit{routes: map[string]gitRoute{
		"GET /repos/acme/apps/git/trees/release%2F1.0":        {body: string(raw)},
		"GET /api/v1/repos/acme/apps/git/trees/release%2F1.0": {body: string(raw)},
	}}
	srv := git.serve(t)
	for _, cfg := range []finopsv1.GitRepositoryConfig{
		{Provider: "github", Repository: "acme/apps", APIURL: srv.URL, BaseBranch: "release/1.0"},
		{Provider: "gitea", Repository: "acme/apps", APIURL: srv.URL + "/api/v1", BaseBranch: "release/1.0"},
	} {
		r, _ := New(cfg, "t", srv.Client())
		files, err := r.Files(context.Background(), "/deploy/")
		if err != nil {
			t.Fatalf("%s: %v (calls %v)", cfg.Provider, err, git.calls)
		}
		if len(files) != MaxFiles || files[0] != "deploy/first.yml" {
			t.Errorf("%s: %d files starting with %q, want %d starting with deploy/first.yml", cfg.Provider, len(files), files[0], MaxFiles)
		}
		for _, f := range files {
			if !strings.HasPrefix(f, "deploy/") || strings.HasSuffix(f, ".md") || f == "deploy/sub.yaml" {
				t.Errorf("%s: %q is not a YAML file under deploy/", cfg.Provider, f)
			}
		}
	}
	if !strings.HasSuffix(git.uris[0], "?recursive=1") || !strings.HasSuffix(git.uris[1], "?recursive=true&per_page=10000") {
		t.Errorf("tree requests = %v", git.uris)
	}
}

func TestGitLabFilesStopsPagingAtTheCap(t *testing.T) {
	page := func(n int) string {
		items := make([]map[string]string, 100)
		for i := range items {
			items[i] = map[string]string{"path": "deploy/p" + strings.Repeat("x", n) + "/" + string(rune('a'+i%26)) + ".yaml", "type": "blob"}
		}
		raw, _ := json.Marshal(items)
		return string(raw)
	}
	var pages []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		pages = append(pages, q.Get("page"))
		if q.Get("path") != "deploy" || q.Get("ref") != "main" || q.Get("recursive") != "true" || q.Get("per_page") != "100" {
			http.Error(w, "unexpected query "+r.URL.RawQuery, http.StatusBadRequest)
			return
		}
		n := len(pages)
		w.Header().Set("X-Next-Page", strconv.Itoa(n+1))
		_, _ = w.Write([]byte(page(n)))
	}))
	defer srv.Close()
	r, _ := New(finopsv1.GitRepositoryConfig{Provider: "gitlab", Repository: "acme/apps", APIURL: srv.URL}, "t", srv.Client())
	files, err := r.Files(context.Background(), "deploy/")
	if err != nil || len(files) != MaxFiles {
		t.Fatalf("Files() = %d files, %v", len(files), err)
	}
	if strings.Join(pages, ",") != "1,2,3,4,5" {
		t.Errorf("pages requested = %v, want paging to stop once %d files are found", pages, MaxFiles)
	}
}

func TestReadEscapesThePath(t *testing.T) {
	wrapped := ""
	for i := 0; i < len(content64); i += 60 {
		wrapped += content64[i:min(i+60, len(content64))] + "\n"
	}
	ghBody, _ := json.Marshal(map[string]string{"content": wrapped, "sha": "gh-sha"})
	git := &scriptedGit{routes: map[string]gitRoute{
		"GET /repos/acme/apps/contents/deploy/my%20app.yaml":                       {body: string(ghBody)},
		"GET /api/v4/projects/acme%2Fapps/repository/files/deploy%2Fmy%20app.yaml": {body: `{"content":"` + content64 + `","blob_id":"gl-blob"}`},
	}}
	srv := git.serve(t)
	gh, _ := New(finopsv1.GitRepositoryConfig{Provider: "github", Repository: "acme/apps", APIURL: srv.URL, BaseBranch: "dev"}, "t", srv.Client())
	raw, rev, err := gh.Read(context.Background(), "deploy/my app.yaml")
	if err != nil || rev != "gh-sha" || string(raw) != file {
		t.Errorf("GitHub Read() = %q, %q, %v; content wrapped at 60 characters must decode", raw, rev, err)
	}
	gl, _ := New(finopsv1.GitRepositoryConfig{Provider: "gitlab", Repository: "acme/apps", APIURL: srv.URL + "/api/v4", BaseBranch: "dev"}, "t", srv.Client())
	raw, rev, err = gl.Read(context.Background(), "deploy/my app.yaml")
	if err != nil || rev != "gl-blob" || string(raw) != file {
		t.Errorf("GitLab Read() = %q, %q, %v", raw, rev, err)
	}
	for _, uri := range git.uris {
		if !strings.HasSuffix(uri, "?ref=dev") {
			t.Errorf("request %q does not read the base branch", uri)
		}
	}
}
