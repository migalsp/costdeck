package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/auth"
	"github.com/migalsp/costdeck-operator/internal/config"
	"github.com/migalsp/costdeck-operator/internal/gitrepo"
	"github.com/migalsp/costdeck-operator/internal/netguard"
	"github.com/migalsp/costdeck-operator/internal/optimize"
	"github.com/migalsp/costdeck-operator/internal/rightsizing"
	"github.com/migalsp/costdeck-operator/internal/scaling"
)

const gitTokenSecret = "costdeck-git-credentials"

// workloadRef names a workload in a namespace's advice.
type workloadRef struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

// adviceFor finds a workload's advice in a namespace's report.
func (s *Server) adviceFor(ctx context.Context, ns string, ref workloadRef) (rightsizing.WorkloadAdvice, rightsizing.Report, error) {
	report, err := s.recommendations(ctx, ns)
	if err != nil {
		return rightsizing.WorkloadAdvice{}, report, errNoUsage{err}
	}
	for _, w := range report.Workloads {
		if w.Kind == ref.Kind && w.Name == ref.Name {
			if w.ApplyForbidden {
				return w, report, errPolicyForbids{policy: w.Policy, workload: w.Name}
			}
			return w, report, nil
		}
	}
	return rightsizing.WorkloadAdvice{}, report, apierrors.NewNotFound(finopsv1.GroupVersion.WithResource("workloads").GroupResource(), ref.Name)
}

// actorName is who made a change, as the dashboard shows people.
func actorName(r *http.Request) string {
	if id := auth.FromContext(r.Context()); id != nil {
		return id.Name
	}
	return ""
}

// handleApplyRightsizing applies a workload's advice, when spec.rightsizing.apply allows.
func (s *Server) handleApplyRightsizing(w http.ResponseWriter, r *http.Request) {
	var ref workloadRef
	if err := decodeJSON(w, r, &ref); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	rec, err := s.applyRightsizing(r.Context(), r.PathValue("ns"), ref, actorName(r))
	writeRightsizing(w, rec, err)
}

func (s *Server) applyRightsizing(ctx context.Context, ns string, ref workloadRef, by string) (finopsv1.WorkloadOptimization, error) {
	cfg, err := config.Get(ctx, s.Client)
	if err != nil {
		return finopsv1.WorkloadOptimization{}, err
	}
	if !cfg.Spec.Rightsizing.Apply {
		return finopsv1.WorkloadOptimization{}, errApplyOff
	}
	if err := s.changeableNamespace(ctx, ns); err != nil {
		return finopsv1.WorkloadOptimization{}, err
	}
	advice, _, err := s.adviceFor(ctx, ns, ref)
	if err != nil {
		return finopsv1.WorkloadOptimization{}, err
	}
	rec, err := optimize.Apply(ctx, s.Client, ns, advice, by, time.Now())
	if err == nil {
		s.recommendationCache.drop(ns, ref.Kind, ref.Name)
	}
	return rec, err
}

type errProtectedNamespace struct{ namespace, reason string }

func (e errProtectedNamespace) Error() string {
	return fmt.Sprintf("workloads in %s are not changed through Cost Deck: %s", e.namespace, e.reason)
}

func (s *Server) changeableNamespace(ctx context.Context, name string) error {
	ns := &corev1.Namespace{}
	if err := s.Client.Get(ctx, client.ObjectKey{Name: name}, ns); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	ns.Name = name
	if reason := scaling.ProtectedNamespace(ns, config.OperatorNamespace()); reason != "" {
		return errProtectedNamespace{namespace: name, reason: reason}
	}
	return nil
}

type errPolicyForbids struct{ policy, workload string }

func (e errPolicyForbids) Error() string {
	if e.policy == policiesUnreadable {
		return fmt.Sprintf("the right-sizing policies of this namespace could not be read, so advice for %s cannot be applied or proposed until they can", e.workload)
	}
	return fmt.Sprintf("the RightsizingPolicy %s forbids applying advice to %s or proposing it", e.policy, e.workload)
}

// errNoUsage means there is no advice to act on yet: the usage data is missing.
type errNoUsage struct{ error }

func (e errNoUsage) Unwrap() error { return e.error }

var errApplyOff = errors.New("applying advice is off: an admin turns it on under Settings → Right-sizing")

// handleRevertRightsizing puts a workload's old requests back.
func (s *Server) handleRevertRightsizing(w http.ResponseWriter, r *http.Request) {
	var ref workloadRef
	if err := decodeJSON(w, r, &ref); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	reason := "put back by hand"
	if by := actorName(r); by != "" {
		reason = "put back by " + by
	}
	rec, err := optimize.Revert(r.Context(), s.Client, r.PathValue("ns"), ref.Kind, ref.Name, reason)
	if err == nil {
		s.recommendationCache.forget(r.PathValue("ns"))
	}
	writeRightsizing(w, rec, err)
}

// handleProposeRightsizing opens a pull request with a workload's advice in the GitOps
// repository of spec.rightsizing.git.
func (s *Server) handleProposeRightsizing(w http.ResponseWriter, r *http.Request) {
	var ref workloadRef
	if err := decodeJSON(w, r, &ref); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	rec, err := s.proposeRightsizing(r.Context(), r.PathValue("ns"), ref, actorName(r))
	writeRightsizing(w, rec, err)
}

var errNoRepository = errors.New("no GitOps repository is set up: an admin adds one under Settings → Right-sizing")

func (s *Server) gitRepo(ctx context.Context, git *finopsv1.GitRepositoryConfig, token string) (gitrepo.Repo, error) {
	if git == nil {
		return nil, errNoRepository
	}
	if token == "" {
		token = s.secretValue(ctx, gitTokenSecret, "TOKEN")
	}
	hc := s.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second, Transport: netguard.Transport()}
	}
	return gitrepo.New(*git, token, hc)
}

func (s *Server) proposeRightsizing(ctx context.Context, ns string, ref workloadRef, by string) (finopsv1.WorkloadOptimization, error) {
	cfg, err := config.Get(ctx, s.Client)
	if err != nil {
		return finopsv1.WorkloadOptimization{}, err
	}
	repo, err := s.gitRepo(ctx, cfg.Spec.Rightsizing.Git, "")
	if err != nil {
		return finopsv1.WorkloadOptimization{}, err
	}
	advice, report, err := s.adviceFor(ctx, ns, ref)
	if err != nil {
		return finopsv1.WorkloadOptimization{}, err
	}
	changes := map[string]gitrepo.Change{}
	rec := finopsv1.WorkloadOptimization{Kind: ref.Kind, Name: ref.Name, AppliedBy: by, State: optimize.StateProposed}
	var lines []string
	for _, c := range advice.Containers {
		ch := gitrepo.Change{CPU: proposed(c.CPU), Memory: proposed(c.Memory)}
		if ch == (gitrepo.Change{}) {
			continue
		}
		changes[c.Name] = ch
		rec.Containers = append(rec.Containers, finopsv1.ContainerOptimization{Name: c.Name,
			Original:  finopsv1.ResourceValues{CPURequest: c.CPU.Request, MemoryRequest: c.Memory.Request},
			Optimized: finopsv1.ResourceValues{CPURequest: firstOf(ch.CPU, c.CPU.Request), MemoryRequest: firstOf(ch.Memory, c.Memory.Request)}})
		lines = append(lines, fmt.Sprintf("| %s | %s → **%s** (used %s) | %s → **%s** (used %s) |", c.Name,
			firstOf(c.CPU.Request, "none"), firstOf(ch.CPU, c.CPU.Request), firstOf(c.CPU.Observed, "?"),
			firstOf(c.Memory.Request, "none"), firstOf(ch.Memory, c.Memory.Request), firstOf(c.Memory.Observed, "?")))
	}
	if len(changes) == 0 {
		return rec, optimize.ErrNothingToApply
	}
	file, content, revision, err := gitrepo.Find(ctx, repo, cfg.Spec.Rightsizing.Git.Path, ref.Kind, ns, ref.Name, changes)
	if err != nil {
		return rec, err
	}
	now := time.Now()
	title := fmt.Sprintf("Right-size %s %s/%s", ref.Kind, ns, ref.Name)
	body := fmt.Sprintf("%s\n\n| Container | CPU request | Memory request |\n|---|---|---|\n%s\n\nSaves about %.2f %s a month. %s\n\nProposed by Cost Deck",
		title, strings.Join(lines, "\n"), advice.MonthlySavings, report.Currency, report.Basis)
	if by != "" {
		body += " for " + by
	}
	url, err := repo.Propose(ctx, gitrepo.Proposal{
		Branch: fmt.Sprintf("costdeck/rightsize-%s-%s-%d", ns, strings.ToLower(ref.Name), now.Unix()), Path: file, Content: content,
		Revision: revision, Message: title, Title: title, Body: body + ".",
	})
	if err != nil {
		return rec, err
	}
	rec.PullRequest = url
	return rec, s.recordProposal(ctx, ns, rec, now)
}

func (s *Server) recordProposal(ctx context.Context, ns string, rec finopsv1.WorkloadOptimization, now time.Time) error {
	at := metav1.NewTime(now)
	rec.AppliedAt = &at
	if len(rec.Containers) > 0 {
		rec.Original, rec.Optimized = rec.Containers[0].Original, rec.Containers[0].Optimized
	}
	return optimize.Record(ctx, s.Client, ns, rec)
}

// proposed is the request a pull request sets, or "" to leave it.
func proposed(a rightsizing.ResourceAdvice) string {
	switch a.Action {
	case rightsizing.ActionReduce, rightsizing.ActionIncrease, rightsizing.ActionSet:
		return a.Recommended
	}
	return ""
}

func firstOf(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

func writeRightsizing(w http.ResponseWriter, rec finopsv1.WorkloadOptimization, err error) {
	var forbids errPolicyForbids
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, rec)
	case errors.As(err, &forbids) && forbids.policy == policiesUnreadable:
		writeError(w, http.StatusServiceUnavailable, err.Error())
	case errors.Is(err, errApplyOff), errors.Is(err, errNoRepository), errors.As(err, new(errPolicyForbids)), errors.As(err, new(errProtectedNamespace)):
		writeError(w, http.StatusForbidden, err.Error())
	case errors.Is(err, optimize.ErrManagedByGitOps):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, optimize.ErrNothingToApply), errors.Is(err, optimize.ErrUnsupported), errors.Is(err, optimize.ErrAmbiguousWorkload),
		errors.Is(err, gitrepo.ErrNotFound):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.As(err, new(errNoUsage)):
		writeError(w, http.StatusServiceUnavailable, err.Error())
	case apierrors.IsNotFound(err):
		writeError(w, http.StatusNotFound, "no right-sizing advice or change for this workload")
	default:
		writeK8sError(w, err)
	}
}

// rightsizingChange is one change on record, with its namespace.
type rightsizingChange struct {
	Namespace string `json:"namespace"`
	finopsv1.WorkloadOptimization
}

// rightsizingChanges is GET /api/rightsizing/changes: what can be done with advice, for
// every role, and the changes on record.
type rightsizingChanges struct {
	ApplyEnabled bool                `json:"applyEnabled"`
	GuardMinutes int32               `json:"guardMinutes"`
	Repository   string              `json:"repository,omitempty"`
	Changes      []rightsizingChange `json:"changes"`
}

// handleRightsizingChanges lists the changes applied, proposed and put back, newest first.
func (s *Server) handleRightsizingChanges(w http.ResponseWriter, r *http.Request) {
	cfg, err := config.Get(r.Context(), s.Client)
	if err != nil {
		writeK8sError(w, err)
		return
	}
	var list finopsv1.NamespaceOptimizationList
	if err := s.Client.List(r.Context(), &list, client.InNamespace(config.OperatorNamespace())); err != nil {
		writeK8sError(w, err)
		return
	}
	out := []rightsizingChange{}
	ns := r.URL.Query().Get("namespace")
	for _, opt := range list.Items {
		if ns != "" && opt.Spec.TargetNamespace != ns {
			continue
		}
		for _, wl := range opt.Status.Workloads {
			out = append(out, rightsizingChange{Namespace: opt.Spec.TargetNamespace, WorkloadOptimization: wl})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		ai, aj := out[i].AppliedAt, out[j].AppliedAt
		return ai != nil && (aj == nil || aj.Before(ai))
	})
	rs := rightsizingSettings(cfg.Spec.Rightsizing)
	resp := rightsizingChanges{ApplyEnabled: rs.Apply, GuardMinutes: rs.GuardMinutes, Changes: out}
	if g := rs.Git; g != nil {
		resp.Repository = g.Repository + " (" + g.Provider + ")"
	}
	writeJSON(w, http.StatusOK, resp)
}

// RightsizingSettings is spec.rightsizing as the settings API reads it; the token is only
// reported.
type RightsizingSettings struct {
	Apply        bool         `json:"apply"`
	GuardMinutes int32        `json:"guardMinutes"`
	Git          *GitSettings `json:"git,omitempty"`
}

type GitSettings struct {
	Provider   string `json:"provider"`
	Repository string `json:"repository"`
	APIURL     string `json:"apiUrl,omitempty"`
	BaseBranch string `json:"baseBranch"`
	Path       string `json:"path,omitempty"`
	HasToken   bool   `json:"hasToken"`
}

// RightsizingUpdateRequest changes spec.rightsizing; git null removes the repository.
type RightsizingUpdateRequest struct {
	Apply        *bool             `json:"apply,omitempty"`
	GuardMinutes *int32            `json:"guardMinutes,omitempty"`
	Git          *GitUpdateRequest `json:"git,omitempty"`
	RemoveGit    bool              `json:"removeGit,omitempty"`
}

type GitUpdateRequest struct {
	Provider   string `json:"provider"`
	Repository string `json:"repository"`
	APIURL     string `json:"apiUrl,omitempty"`
	BaseBranch string `json:"baseBranch,omitempty"`
	Path       string `json:"path,omitempty"`
	Token      string `json:"token,omitempty"`
}

func rightsizingSettings(rs finopsv1.RightsizingConfig) RightsizingSettings {
	out := RightsizingSettings{Apply: rs.Apply, GuardMinutes: rs.GuardMinutes}
	if out.GuardMinutes == 0 {
		out.GuardMinutes = optimize.DefaultGuardMinutes
	}
	if g := rs.Git; g != nil {
		out.Git = &GitSettings{Provider: g.Provider, Repository: g.Repository, APIURL: g.APIURL, BaseBranch: firstOf(g.BaseBranch, "main"), Path: g.Path, HasToken: g.SecretRef != ""}
	}
	return out
}

func (u *GitUpdateRequest) config(secretRef string) (*finopsv1.GitRepositoryConfig, error) {
	g := &finopsv1.GitRepositoryConfig{Provider: u.Provider, Repository: strings.Trim(strings.TrimSpace(u.Repository), "/"), APIURL: strings.TrimRight(strings.TrimSpace(u.APIURL), "/"),
		BaseBranch: strings.TrimSpace(u.BaseBranch), Path: strings.Trim(strings.TrimSpace(u.Path), "/"), SecretRef: secretRef}
	switch g.Provider {
	case "github", "gitlab", "gitea":
	default:
		return nil, badRequestf("right-sizing: the provider is github, gitlab or gitea")
	}
	if g.Repository == "" {
		return nil, badRequestf("right-sizing: enter the repository, such as acme/apps")
	}
	if g.APIURL != "" {
		u, ok := httpURL(g.APIURL)
		if !ok || netguard.ForbiddenHost(u.Hostname()) {
			return nil, badRequestf("right-sizing: the API URL must be an http(s) address")
		}
	} else if g.Provider == "gitea" {
		return nil, badRequestf("right-sizing: a Gitea needs its API URL, such as https://gitea.example.com/api/v1")
	}
	return g, nil
}

func (s *Server) applyRightsizingSettings(ctx context.Context, cfg *finopsv1.CostDeckConfig, req *SettingsUpdateRequest) error {
	u := req.Rightsizing
	if u == nil {
		return nil
	}
	rs := &cfg.Spec.Rightsizing
	if u.Apply != nil {
		rs.Apply = *u.Apply
	}
	if u.GuardMinutes != nil {
		if *u.GuardMinutes < 5 || *u.GuardMinutes > 1440 {
			return badRequestf("right-sizing: the watch lasts 5 to 1440 minutes")
		}
		rs.GuardMinutes = *u.GuardMinutes
	}
	if u.RemoveGit {
		rs.Git = nil
		return nil
	}
	if u.Git == nil {
		return nil
	}
	ref := ""
	if rs.Git != nil {
		ref = rs.Git.SecretRef
	}
	if u.Git.Token != "" {
		ref = gitTokenSecret
		if err := s.storeCredentials(ctx, gitTokenSecret, map[string][]byte{"TOKEN": []byte(strings.TrimSpace(u.Git.Token))}, &ref); err != nil {
			return err
		}
	}
	if ref == "" {
		return badRequestf("right-sizing: enter an access token that can push branches and open pull requests")
	}
	g, err := u.Git.config(ref)
	if err != nil {
		return err
	}
	rs.Git = g
	return nil
}

// testGit reaches the repository with the unsaved values of the form.
func (s *Server) testGit(w http.ResponseWriter, r *http.Request, saved *finopsv1.CostDeckConfig) {
	var u GitUpdateRequest
	if err := decodeJSON(w, r, &u); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	g, err := u.config("")
	if err == nil {
		var repo gitrepo.Repo
		token := u.Token
		if token == "" && saved.Spec.Rightsizing.Git != nil {
			token = s.secretValue(r.Context(), gitTokenSecret, "TOKEN")
		}
		if repo, err = s.gitRepo(r.Context(), g, token); err == nil {
			err = repo.Check(r.Context())
		}
	}
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"connected": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"connected": true, "message": "Reached " + g.Repository})
}
