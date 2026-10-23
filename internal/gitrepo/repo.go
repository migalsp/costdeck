package gitrepo

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
)

// Repo is a GitOps repository changes are proposed in.
type Repo interface {
	// Check reaches the repository with the token.
	Check(ctx context.Context) error
	// Files lists the YAML files under dir on the base branch.
	Files(ctx context.Context, dir string) ([]string, error)
	// Read returns a file of the base branch and its revision.
	Read(ctx context.Context, file string) ([]byte, string, error)
	// Propose commits a file to a new branch and opens a pull request; it returns its URL.
	Propose(ctx context.Context, p Proposal) (string, error)
}

// Proposal is one changed file and the pull request around it.
type Proposal struct {
	Branch   string
	Path     string
	Content  []byte
	Revision string
	Message  string
	Title    string
	Body     string
}

// MaxFiles bounds how many YAML files a search reads.
const MaxFiles = 500

const (
	// DefaultBranch is where pull requests go without spec.rightsizing.git.baseBranch.
	DefaultBranch = "main"
	treeBlob      = "blob"
)

// New returns the repository of a configuration.
func New(cfg finopsv1.GitRepositoryConfig, token string, hc *http.Client) (Repo, error) {
	base := strings.TrimRight(cfg.APIURL, "/")
	branch := cfg.BaseBranch
	if branch == "" {
		branch = DefaultBranch
	}
	if cfg.Repository == "" {
		return nil, fmt.Errorf("no repository is set")
	}
	a := api{hc: hc, base: base, branch: branch, repo: cfg.Repository}
	switch cfg.Provider {
	case "github":
		if a.base == "" {
			a.base = "https://api.github.com"
		}
		a.auth = func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer "+token)
			r.Header.Set("Accept", "application/vnd.github+json")
		}
		return &github{a}, nil
	case "gitea":
		if a.base == "" {
			return nil, fmt.Errorf("a Gitea needs its API URL, such as https://gitea.example.com/api/v1")
		}
		a.auth = func(r *http.Request) { r.Header.Set("Authorization", "token "+token) }
		return &gitea{a}, nil
	case "gitlab":
		if a.base == "" {
			a.base = "https://gitlab.com/api/v4"
		}
		a.auth = func(r *http.Request) { r.Header.Set("PRIVATE-TOKEN", token) }
		return &gitlab{a}, nil
	}
	return nil, fmt.Errorf("the provider is github, gitlab or gitea")
}

// api is what the providers share: a base URL, a token and a branch.
type api struct {
	hc     *http.Client
	base   string
	branch string
	repo   string
	auth   func(*http.Request)
}

// call sends a request with a JSON body and decodes a JSON answer; it returns the
// response headers for pagination.
func (a api) call(ctx context.Context, method, p string, body, out any) (http.Header, error) {
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.base+p, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	a.auth(req)
	res, err := a.hc.Do(req)
	if err != nil {
		if ue, ok := err.(*url.Error); ok {
			err = ue.Err
		}
		return nil, fmt.Errorf("could not reach %s: %w", req.URL.Host, err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode/100 != 2 {
		detail, _ := io.ReadAll(io.LimitReader(res.Body, 300))
		return nil, fmt.Errorf("%s %s answered %s: %s", method, req.URL.Path, res.Status, strings.TrimSpace(string(detail)))
	}
	if out != nil {
		if err := json.NewDecoder(io.LimitReader(res.Body, 16<<20)).Decode(out); err != nil {
			return nil, err
		}
	}
	return res.Header, nil
}

func yamlUnder(p, dir string) bool {
	dir = strings.Trim(dir, "/")
	ext := path.Ext(p)
	return (ext == ".yaml" || ext == ".yml") && (dir == "" || p == dir || strings.HasPrefix(p, dir+"/"))
}

func esc(p string) string {
	parts := strings.Split(p, "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	return strings.Join(parts, "/")
}

// github is GitHub and GitHub Enterprise.
type github struct{ api }

func (g *github) Check(ctx context.Context) error {
	_, err := g.call(ctx, http.MethodGet, "/repos/"+g.repo, nil, nil)
	return err
}

func (g *github) Files(ctx context.Context, dir string) ([]string, error) {
	var tree struct {
		Tree []struct{ Path, Type string } `json:"tree"`
	}
	if _, err := g.call(ctx, http.MethodGet, "/repos/"+g.repo+"/git/trees/"+url.PathEscape(g.branch)+"?recursive=1", nil, &tree); err != nil {
		return nil, err
	}
	var out []string
	for _, t := range tree.Tree {
		if t.Type == treeBlob && yamlUnder(t.Path, dir) && len(out) < MaxFiles {
			out = append(out, t.Path)
		}
	}
	return out, nil
}

func (g *github) Read(ctx context.Context, file string) ([]byte, string, error) {
	var f struct{ Content, Sha string }
	if _, err := g.call(ctx, http.MethodGet, "/repos/"+g.repo+"/contents/"+esc(file)+"?ref="+url.QueryEscape(g.branch), nil, &f); err != nil {
		return nil, "", err
	}
	raw, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(f.Content, "\n", ""))
	return raw, f.Sha, err
}

func (g *github) Propose(ctx context.Context, p Proposal) (string, error) {
	var ref struct {
		Object struct{ Sha string } `json:"object"`
	}
	if _, err := g.call(ctx, http.MethodGet, "/repos/"+g.repo+"/git/ref/heads/"+esc(g.branch), nil, &ref); err != nil {
		return "", err
	}
	if _, err := g.call(ctx, http.MethodPost, "/repos/"+g.repo+"/git/refs", map[string]string{"ref": "refs/heads/" + p.Branch, "sha": ref.Object.Sha}, nil); err != nil {
		return "", err
	}
	put := map[string]string{"message": p.Message, "content": base64.StdEncoding.EncodeToString(p.Content), "sha": p.Revision, "branch": p.Branch}
	if _, err := g.call(ctx, http.MethodPut, "/repos/"+g.repo+"/contents/"+esc(p.Path), put, nil); err != nil {
		return "", err
	}
	var pr struct {
		URL string `json:"html_url"`
	}
	_, err := g.call(ctx, http.MethodPost, "/repos/"+g.repo+"/pulls", map[string]string{"title": p.Title, "head": p.Branch, "base": g.branch, "body": p.Body}, &pr)
	return pr.URL, err
}

// gitea is Gitea and Forgejo, whose API is close to GitHub's.
type gitea struct{ api }

func (g *gitea) Check(ctx context.Context) error {
	_, err := g.call(ctx, http.MethodGet, "/repos/"+g.repo, nil, nil)
	return err
}

func (g *gitea) Files(ctx context.Context, dir string) ([]string, error) {
	var tree struct {
		Tree []struct{ Path, Type string } `json:"tree"`
	}
	if _, err := g.call(ctx, http.MethodGet, "/repos/"+g.repo+"/git/trees/"+url.PathEscape(g.branch)+"?recursive=true&per_page=10000", nil, &tree); err != nil {
		return nil, err
	}
	var out []string
	for _, t := range tree.Tree {
		if t.Type == treeBlob && yamlUnder(t.Path, dir) && len(out) < MaxFiles {
			out = append(out, t.Path)
		}
	}
	return out, nil
}

func (g *gitea) Read(ctx context.Context, file string) ([]byte, string, error) {
	var f struct{ Content, Sha string }
	if _, err := g.call(ctx, http.MethodGet, "/repos/"+g.repo+"/contents/"+esc(file)+"?ref="+url.QueryEscape(g.branch), nil, &f); err != nil {
		return nil, "", err
	}
	raw, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(f.Content, "\n", ""))
	return raw, f.Sha, err
}

func (g *gitea) Propose(ctx context.Context, p Proposal) (string, error) {
	put := map[string]string{"message": p.Message, "content": base64.StdEncoding.EncodeToString(p.Content), "sha": p.Revision, "branch": g.branch, "new_branch": p.Branch}
	if _, err := g.call(ctx, http.MethodPut, "/repos/"+g.repo+"/contents/"+esc(p.Path), put, nil); err != nil {
		return "", err
	}
	var pr struct {
		URL string `json:"html_url"`
	}
	_, err := g.call(ctx, http.MethodPost, "/repos/"+g.repo+"/pulls", map[string]string{"title": p.Title, "head": p.Branch, "base": g.branch, "body": p.Body}, &pr)
	return pr.URL, err
}

// gitlab is GitLab, with merge requests.
type gitlab struct{ api }

func (g *gitlab) project() string { return "/projects/" + url.PathEscape(g.repo) }

func (g *gitlab) Check(ctx context.Context) error {
	_, err := g.call(ctx, http.MethodGet, g.project(), nil, nil)
	return err
}

func (g *gitlab) Files(ctx context.Context, dir string) ([]string, error) {
	var out []string
	for page := 1; page > 0 && len(out) < MaxFiles; {
		var items []struct{ Path, Type string }
		q := url.Values{"ref": {g.branch}, "recursive": {"true"}, "per_page": {"100"}, "page": {strconv.Itoa(page)}}
		if d := strings.Trim(dir, "/"); d != "" {
			q.Set("path", d)
		}
		h, err := g.call(ctx, http.MethodGet, g.project()+"/repository/tree?"+q.Encode(), nil, &items)
		if err != nil {
			return nil, err
		}
		for _, it := range items {
			if it.Type == treeBlob && yamlUnder(it.Path, dir) && len(out) < MaxFiles {
				out = append(out, it.Path)
			}
		}
		page, _ = strconv.Atoi(h.Get("X-Next-Page"))
	}
	return out, nil
}

func (g *gitlab) Read(ctx context.Context, file string) ([]byte, string, error) {
	var f struct {
		Content string `json:"content"`
		BlobID  string `json:"blob_id"`
	}
	if _, err := g.call(ctx, http.MethodGet, g.project()+"/repository/files/"+url.PathEscape(file)+"?ref="+url.QueryEscape(g.branch), nil, &f); err != nil {
		return nil, "", err
	}
	raw, err := base64.StdEncoding.DecodeString(f.Content)
	return raw, f.BlobID, err
}

func (g *gitlab) Propose(ctx context.Context, p Proposal) (string, error) {
	commit := map[string]any{"branch": p.Branch, "start_branch": g.branch, "commit_message": p.Message,
		"actions": []map[string]string{{"action": "update", "file_path": p.Path, "content": string(p.Content)}}}
	if _, err := g.call(ctx, http.MethodPost, g.project()+"/repository/commits", commit, nil); err != nil {
		return "", err
	}
	var mr struct {
		URL string `json:"web_url"`
	}
	_, err := g.call(ctx, http.MethodPost, g.project()+"/merge_requests",
		map[string]any{"source_branch": p.Branch, "target_branch": g.branch, "title": p.Title, "description": p.Body, "remove_source_branch": true}, &mr)
	return mr.URL, err
}

// Find returns the first file under dir that holds a workload and its edited content.
func Find(ctx context.Context, r Repo, dir, kind, namespace, name string, changes map[string]Change) (file string, content []byte, revision string, err error) {
	files, err := r.Files(ctx, dir)
	if err != nil {
		return "", nil, "", err
	}
	for _, f := range files {
		raw, rev, err := r.Read(ctx, f)
		if err != nil {
			continue
		}
		edited, ok, err := EditRequests(raw, kind, namespace, name, changes)
		if err == nil && ok {
			return f, edited, rev, nil
		}
	}
	return "", nil, "", ErrNotFound
}

// ErrNotFound means no plain manifest of the workload is in the repository: it may come
// from a Helm chart, whose values files Cost Deck cannot map to a workload.
var ErrNotFound = fmt.Errorf("no manifest of this workload in the repository; one rendered from a Helm chart keeps its requests in a values file, which Cost Deck cannot map, so change it there")
