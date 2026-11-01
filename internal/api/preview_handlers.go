package api

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/config"
	"github.com/migalsp/costdeck-operator/internal/scaling"
)

// previewDays is how far ahead the preview shows the timetable; the share of time up is
// taken over four weeks so a monthly saving is not skewed by one odd week.
const (
	previewDays    = 14
	shareDays      = 28
	hoursPerMonth  = 730
	previewNewName = "(new schedule)"
)

// previewRequest is a schedule as the editor holds it, saved or not.
type previewRequest struct {
	// Kind is group or config; Name is the schedule being edited, so it does not conflict
	// with itself.
	Kind string `json:"kind"`
	Name string `json:"name,omitempty"`
	Spec struct {
		Namespaces        []string                     `json:"namespaces,omitempty"`
		NamespaceSelector *metav1.LabelSelector        `json:"namespaceSelector,omitempty"`
		TargetNamespace   string                       `json:"targetNamespace,omitempty"`
		Schedules         []finopsv1.ScalingSchedule   `json:"schedules,omitempty"`
		Exceptions        []finopsv1.ScheduleException `json:"exceptions,omitempty"`
		Calendars         []string                     `json:"calendars,omitempty"`
		Activation        string                       `json:"activation,omitempty"`
		Exclusions        []string                     `json:"exclusions,omitempty"`
		ExternalTargets   []finopsv1.ExternalTarget    `json:"externalTargets,omitempty"`
		PauseGitOps       bool                         `json:"pauseGitOps,omitempty"`
		Hibernate         []finopsv1.HibernateRule     `json:"hibernate,omitempty"`
		Prewarm           *finopsv1.Prewarm            `json:"prewarm,omitempty"`
	} `json:"spec"`
}

type namespacePreview struct {
	Name string `json:"name"`
	// Source is listed (by name) or selected (by label).
	Source string `json:"source"`
	// OwnedBy names the group that scales the namespace instead of this schedule.
	OwnedBy string `json:"ownedBy,omitempty"`
	// Missing is true when no such namespace exists (yet).
	Missing bool `json:"missing,omitempty"`
	scaling.NamespacePreview
}

type previewResponse struct {
	Namespaces   []namespacePreview        `json:"namespaces"`
	CloudTargets []finopsv1.ExternalTarget `json:"cloudTargets"`
	// Intervals covers the next previewDays days; UpShare is the share of the next four
	// weeks the namespaces are up.
	Intervals  []scaling.Interval `json:"intervals"`
	UpShare    float64            `json:"upShare"`
	NextChange *time.Time         `json:"nextChange,omitempty"`
	OnDemand   bool               `json:"onDemand,omitempty"`
	Savings    struct {
		// Hourly is what the namespaces cost per hour while they are down (their requests at
		// the current rates); Monthly what that saves over a month of this timetable.
		Hourly   float64 `json:"hourly"`
		Monthly  float64 `json:"monthly"`
		Currency string  `json:"currency"`
		Basis    string  `json:"basis"`
	} `json:"savings"`
	Problems []string `json:"problems"`
	Warnings []string `json:"warnings"`
}

// handlePreviewSchedule answers what a schedule would do, without changing anything:
// POST /api/scaling/preview with the schedule as the editor holds it.
func (s *Server) handlePreviewSchedule(w http.ResponseWriter, r *http.Request) {
	var req previewRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Kind != targetGroup && req.Kind != kindConfig {
		writeError(w, http.StatusBadRequest, "kind must be group or config")
		return
	}
	resp, err := s.previewSchedule(r.Context(), req, time.Now())
	if err != nil {
		writeK8sError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) previewSchedule(ctx context.Context, req previewRequest, now time.Time) (*previewResponse, error) {
	resp := &previewResponse{Namespaces: []namespacePreview{}, CloudTargets: req.Spec.ExternalTargets, Problems: []string{}, Warnings: []string{}}
	if resp.CloudTargets == nil {
		resp.CloudTargets = []finopsv1.ExternalTarget{}
	}
	sp := req.Spec
	calendars := s.calendars(ctx)
	exceptions, problems := scaling.ResolveExceptions(sp.Exceptions, sp.Calendars, calendars, scaling.ExceptionZone(sp.Schedules))
	resp.Problems = append(resp.Problems, problems...)
	tt := scaling.Timetable{Schedules: sp.Schedules, Exceptions: exceptions, Lead: scaling.PrewarmLead(sp.Prewarm, nil)}
	resp.Intervals = tt.Intervals(now, now.AddDate(0, 0, previewDays))
	resp.UpShare = tt.ActiveShare(now, now.AddDate(0, 0, shareDays))
	resp.NextChange = tt.NextChange(now)
	if sp.Activation == finopsv1.ActivationOnDemand {
		resp.OnDemand = true
		resp.UpShare = 0
	} else if len(sp.Schedules) == 0 && len(exceptions) == 0 {
		resp.Warnings = append(resp.Warnings, "No hours are set, so the namespaces stay up: Cost Deck never scales down without a usable schedule.")
	}

	var all corev1.NamespaceList
	if err := s.Client.List(ctx, &all); err != nil {
		return nil, err
	}
	exists := map[string]bool{}
	for _, ns := range all.Items {
		exists[ns.Name] = true
	}
	var groups finopsv1.ScalingGroupList
	if err := s.Client.List(ctx, &groups, client.InNamespace(config.OperatorNamespace())); err != nil {
		return nil, err
	}

	switch req.Kind {
	case targetGroup:
		selected, err := scaling.SelectNamespaces(sp.NamespaceSelector, all.Items, config.OperatorNamespace())
		if err != nil {
			resp.Problems = append(resp.Problems, err.Error())
		}
		if sp.NamespaceSelector != nil && err == nil && len(selected) == 0 {
			resp.Warnings = append(resp.Warnings, "The label selector matches no namespace yet; namespaces join as they are labelled.")
		}
		// Plan the draft among the other groups, as the controller will.
		draft := finopsv1.ScalingGroup{
			Name: previewNewName, CreationTimestamp: metav1.NewTime(now),
			Spec:   finopsv1.ScalingGroupSpec{Namespaces: sp.Namespaces, NamespaceSelector: sp.NamespaceSelector},
			Status: finopsv1.ScalingGroupStatus{SelectedNamespaces: selected},
		}
		var planned []finopsv1.ScalingGroup
		for _, g := range groups.Items {
			if g.Name == req.Name {
				draft.Name, draft.CreationTimestamp = g.Name, g.CreationTimestamp
				continue
			}
			planned = append(planned, g)
		}
		plan := (&scaling.Engine{}).PlanGroups(now, append(planned, draft), calendars)[draft.Name]
		for _, ns := range draft.Members() {
			np := namespacePreview{Name: ns, Source: "listed", OwnedBy: plan.NamespaceOwners[ns], Missing: !exists[ns]}
			if !slices.Contains(sp.Namespaces, ns) {
				np.Source = "selected"
			}
			resp.Namespaces = append(resp.Namespaces, np)
		}
	case kindConfig:
		np := namespacePreview{Name: sp.TargetNamespace, Source: "listed", Missing: !exists[sp.TargetNamespace]}
		for _, g := range groups.Items {
			if slices.Contains(g.Members(), sp.TargetNamespace) {
				np.OwnedBy = g.Name
				resp.Warnings = append(resp.Warnings, fmt.Sprintf("%s belongs to the schedule %s, whose hours apply; this one only adds its workload rules.", sp.TargetNamespace, g.Name))
			}
		}
		resp.Namespaces = append(resp.Namespaces, np)
	}

	engine := &scaling.Engine{Client: s.Client}
	var freed namespacePreview
	for i := range resp.Namespaces {
		np := &resp.Namespaces[i]
		switch {
		case np.Missing:
			resp.Warnings = append(resp.Warnings, fmt.Sprintf("There is no namespace %s.", np.Name))
			continue
		case np.OwnedBy != "" && req.Kind == targetGroup:
			resp.Warnings = append(resp.Warnings, fmt.Sprintf("%s is already scaled by %s and will be left to it.", np.Name, np.OwnedBy))
			continue
		}
		exclusions, hibernate := s.previewRules(ctx, req, np.Name)
		pv, err := engine.PreviewNamespace(ctx, np.Name, exclusions)
		if err != nil && !apierrors.IsNotFound(err) {
			return nil, err
		}
		if pv.Services, err = engine.Services(ctx, np.Name, hibernate); err != nil {
			resp.Warnings = append(resp.Warnings, fmt.Sprintf("Could not list the services to stop in %s: %s", np.Name, hibernateError(err)))
		}
		np.NamespacePreview = pv
		if owners := gitOpsOwners(pv); len(owners) > 0 && !sp.PauseGitOps {
			resp.Warnings = append(resp.Warnings, fmt.Sprintf("Workloads in %s are managed by %s: unless it ignores the replicas, its sync brings them back. Pausing GitOps while down prevents that.",
				np.Name, strings.Join(owners, ", ")))
		}
		if np.OwnedBy == "" {
			freed.CPU.Add(pv.CPU)
			freed.Memory.Add(pv.Memory)
		}
	}
	rates := s.pricingResolver().Rates(ctx)
	resp.Savings.Hourly = rates.Hourly(freed.CPU, freed.Memory)
	resp.Savings.Monthly = resp.Savings.Hourly * hoursPerMonth * (1 - resp.UpShare)
	resp.Savings.Currency, resp.Savings.Basis = rates.Currency, rates.Basis
	return resp, nil
}

// gitOpsOwners names the GitOps objects managing the workloads a preview would stop.
func gitOpsOwners(pv scaling.NamespacePreview) []string {
	var out []string
	for _, w := range pv.Workloads {
		if w.GitOps != nil && !w.Excluded && !slices.Contains(out, w.GitOps.String()) {
			out = append(out, w.GitOps.String())
		}
	}
	return out
}

// previewRules returns the exclusions and hibernate rules a schedule applies in a namespace:
// a group adds its own hibernate rules to the namespace's workload rules.
func (s *Server) previewRules(ctx context.Context, req previewRequest, ns string) ([]string, []finopsv1.HibernateRule) {
	exclusions, hibernate := req.Spec.Exclusions, req.Spec.Hibernate
	if req.Kind == targetGroup {
		var own []finopsv1.HibernateRule
		exclusions, own = s.workloadRules(ctx, ns)
		hibernate = append(slices.Clone(hibernate), own...)
	}
	return scaling.ExclusionsWith(exclusions, hibernate), hibernate
}

// workloadRules returns the exclusions and hibernate rules a namespace's own ScalingConfig
// sets, which a group applies too.
func (s *Server) workloadRules(ctx context.Context, ns string) ([]string, []finopsv1.HibernateRule) {
	var configs finopsv1.ScalingConfigList
	if err := s.Client.List(ctx, &configs, client.InNamespace(config.OperatorNamespace())); err != nil {
		return nil, nil
	}
	for _, c := range configs.Items {
		if c.Spec.TargetNamespace == ns {
			return c.Spec.Exclusions, c.Spec.Hibernate
		}
	}
	return nil, nil
}

// hibernateError explains why the services a rule covers could not be listed.
func hibernateError(err error) string {
	if apierrors.IsForbidden(err) {
		return "no permission to read them; enable hibernation in the Helm chart (hibernation.enabled, or hibernation.extraRules for other kinds)"
	}
	return err.Error()
}

// toolPreview previews a saved group (target "group") or namespace schedule ("namespace")
// for the assistant and MCP.
func (s *Server) toolPreview(ctx context.Context, target, name string) (string, error) {
	var req previewRequest
	switch target {
	case targetGroup:
		g := &finopsv1.ScalingGroup{}
		if err := s.Client.Get(ctx, client.ObjectKey{Name: name, Namespace: config.OperatorNamespace()}, g); err != nil {
			return "", fmt.Errorf("ScalingGroup %s: %w", name, err)
		}
		req.Kind, req.Name = targetGroup, g.Name
		req.Spec.Namespaces, req.Spec.NamespaceSelector, req.Spec.Activation = g.Spec.Namespaces, g.Spec.NamespaceSelector, g.Spec.Activation
		req.Spec.Schedules, req.Spec.Exceptions, req.Spec.Calendars, req.Spec.ExternalTargets = g.Spec.Schedules, g.Spec.Exceptions, g.Spec.Calendars, g.Spec.ExternalTargets
		req.Spec.PauseGitOps, req.Spec.Hibernate = g.Spec.PauseGitOps, g.Spec.Hibernate
	case targetNamespace:
		c, err := s.configForNamespace(ctx, name)
		if err != nil {
			return "", err
		}
		req.Kind, req.Name = kindConfig, c.Name
		req.Spec.TargetNamespace, req.Spec.Exclusions = c.Spec.TargetNamespace, c.Spec.Exclusions
		req.Spec.Schedules, req.Spec.Exceptions, req.Spec.Calendars = c.Spec.Schedules, c.Spec.Exceptions, c.Spec.Calendars
		req.Spec.PauseGitOps, req.Spec.Hibernate = c.Spec.PauseGitOps, c.Spec.Hibernate
	default:
		return "", fmt.Errorf("target must be group or namespace")
	}
	p, err := s.previewSchedule(ctx, req, time.Now())
	if err != nil {
		return "", err
	}
	// The model needs the gist: per namespace what stops, and when the state changes.
	type nsLine struct {
		Namespace string   `json:"namespace"`
		Workloads int      `json:"workloadsToStop"`
		Excluded  int      `json:"excluded,omitempty"`
		CronJobs  int      `json:"cronJobs,omitempty"`
		Services  []string `json:"servicesToStop,omitempty"`
		Note      string   `json:"note,omitempty"`
	}
	lines := make([]nsLine, 0, len(p.Namespaces))
	for _, np := range p.Namespaces {
		l := nsLine{Namespace: np.Name, CronJobs: np.CronJobs}
		for _, sv := range np.Services {
			if sv.Running {
				l.Services = append(l.Services, sv.Kind+" "+sv.Name)
			}
		}
		for _, w := range np.Workloads {
			if w.Excluded {
				l.Excluded++
			} else {
				l.Workloads++
			}
		}
		switch {
		case np.Missing:
			l.Note = "does not exist"
		case np.OwnedBy != "" && req.Kind == targetGroup:
			l.Note = "scaled by " + np.OwnedBy
		}
		lines = append(lines, l)
	}
	changes := make([]string, 0, len(p.Intervals))
	for _, iv := range p.Intervals {
		state := "down"
		if iv.Active {
			state = "up"
		}
		changes = append(changes, fmt.Sprintf("%s → %s %s (%s)", iv.From.UTC().Format("Mon 02 Jan 15:04"), iv.To.UTC().Format("Mon 02 Jan 15:04 MST"), state, iv.Reason))
	}
	return toJSON(map[string]any{
		"namespaces": lines, "nextTwoWeeks": changes, "upShare": fmt.Sprintf("%.0f%%", p.UpShare*100),
		"estimatedSaving": map[string]any{"hourlyWhileDown": p.Savings.Hourly, "monthly": p.Savings.Monthly, "currency": p.Savings.Currency, "basis": p.Savings.Basis},
		"problems":        p.Problems, "warnings": p.Warnings,
	})
}
