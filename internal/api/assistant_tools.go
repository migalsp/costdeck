package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/ai"
	"github.com/migalsp/costdeck-operator/internal/auth"
	"github.com/migalsp/costdeck-operator/internal/config"
	"github.com/migalsp/costdeck-operator/internal/pricing"
	"github.com/migalsp/costdeck-operator/internal/rightsizing"
	"github.com/migalsp/costdeck-operator/internal/scaling"
)

// assistantSystemPrompt is deliberately static: live data comes from tools, so the prompt
// and the tool list form a stable prefix that providers can cache across conversations.
const assistantSystemPrompt = `You are CostDeck Assistant, the FinOps assistant built into the CostDeck Kubernetes operator.

CostDeck scales non-production namespaces up and down on schedules (ScalingGroups for sets of namespaces, ScalingConfigs for single namespaces), tracks namespace resource usage, and estimates cost from configured rates.

Look up live data with the tools instead of guessing, and never invent numbers. Cost figures are estimates; say so when you quote them. A group's mode tells who is in control: Schedule, ManualUp/ManualDown (a manual override that ignores the schedule), Exception (a dated exception or a holiday calendar day keeps it up or down), AlwaysOn (no schedule), Dependency or OnDemand (driven by groups that depend on it), Idle (put to sleep inside its hours because nobody used it for its idleAfter period; it stays down until status idleUntil or a manual start).

When the user asks to change something (scale a group or namespace, hand control back to the schedule, keep a schedule up or down on given dates, revert an earlier right-sizing), call the matching tool. For a one-off "keep it up until tonight" use a manual override; for dates in the future (a holiday, a release weekend) add an exception. CostDeck then asks the user to confirm before anything changes. CostDeck does not change resource requests itself: for right-sizing, present the recommendations and the YAML values the user should apply.

Answer in concise Markdown. Prefer short tables for lists.`

// toolRegistry returns every assistant tool. Mutating tools are offered to operators and
// admins only, and even then they produce a confirmation card instead of running.
func (s *Server) toolRegistry() []ai.Tool {
	str := func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
	// required is left out when nothing is required: a nil slice would be sent as
	// "required": null, which strict validators such as vLLM's reject, failing every chat.
	obj := func(props map[string]any, required ...string) map[string]any {
		schema := map[string]any{"type": "object", "properties": props}
		if len(required) > 0 {
			schema["required"] = required
		}
		return schema
	}
	action := map[string]any{
		"type": "string", "enum": []string{"up", "down", "resume"},
		"description": "up or down forces the state; resume removes the override so the schedule decides again",
	}
	duration := str(`How long an up/down override holds: "nextTransition" (until the schedule would change state on its own; the default when there is a schedule), a duration such as "4h", or "forever"`)
	target := map[string]any{
		"type": "string", "enum": []string{targetGroup, targetNamespace},
		"description": "group: name is a ScalingGroup; namespace: name is a namespace with its own ScalingConfig",
	}
	bound := func(what string) map[string]any {
		return str(what + `: a date "2026-12-24" (the whole day) or a date and time "2026-12-24T18:00", in the schedule's timezone`)
	}

	return []ai.Tool{
		{
			Name:        "get_cluster_overview",
			Description: "Cluster-wide capacity, requested resources, node and pod counts, and the estimated monthly compute cost.",
			Parameters:  obj(map[string]any{}),
			Run:         func(ctx context.Context, _ map[string]any) (string, error) { return s.toolClusterOverview(ctx) },
		},
		{
			Name: "get_cost_overview",
			Description: "The FinOps summary: monthly run rate split into used, requested-but-idle and unrequested capacity, " +
				"cost efficiency, month to date and forecast, what schedules save, the most expensive namespaces, and the " +
				"savings opportunities ranked by what they save. Start here for questions about cost, waste or savings.",
			Parameters: obj(map[string]any{}),
			Run:        func(ctx context.Context, _ map[string]any) (string, error) { return s.toolCostOverview(ctx) },
		},
		{
			Name: "suggest_schedule",
			Description: "Working hours for a namespace, read from its CPU usage over the last week: the weekly windows it " +
				"was busy in (with an hour of margin), the share of the week they cover and what a schedule with them would " +
				"save. Says so when the namespace is busy around the clock, idle, or has too little history.",
			Parameters: obj(map[string]any{"namespace": str("Namespace"), "timezone": str("IANA time zone for the windows, e.g. Europe/Berlin; default UTC")}, "namespace"),
			Run: func(ctx context.Context, args map[string]any) (string, error) {
				tz, _ := args["timezone"].(string)
				return s.toolSuggestSchedule(ctx, args["namespace"].(string), tz)
			},
		},
		{
			Name: "get_clusters",
			Description: "Every Cost Deck installation this one shows on its Clusters page, itself first: nodes, monthly cost, " +
				"what schedules save and what more could be saved, month to date and forecast, schedules and how many are down, " +
				"or why a cluster could not be read. Totals per currency.",
			Parameters: obj(map[string]any{}),
			Run: func(ctx context.Context, _ map[string]any) (string, error) {
				rec := httptest.NewRecorder()
				s.handleClusters(rec, httptest.NewRequest(http.MethodGet, "/api/clusters", nil).WithContext(ctx))
				if rec.Code != http.StatusOK {
					return "", fmt.Errorf("clusters: %s", strings.TrimSpace(rec.Body.String()))
				}
				return rec.Body.String(), nil
			},
		},
		{
			Name: "get_cost_allocation",
			Description: "Showback and chargeback: a period's cost from the daily history charged to teams, environments, " +
				"namespaces or the values of a namespace label, with shared namespaces and unrequested capacity split by the " +
				"configured policy. The rows and the unallocated capacity add up to the bill.",
			Parameters: obj(map[string]any{
				"by":     str("team (the default), environment, namespace, or label:<key> such as label:cost-center"),
				"period": str("this-month (the default), last-month, last-7-days or last-30-days"),
				"from":   str("First day, YYYY-MM-DD; with to, instead of period"),
				"to":     str("Last day, YYYY-MM-DD"),
			}),
			Run: func(ctx context.Context, args map[string]any) (string, error) {
				q := url.Values{}
				for _, k := range []string{"by", "period", "from", "to"} {
					if v, _ := args[k].(string); v != "" {
						q.Set(k, v)
					}
				}
				report, err := s.allocation(ctx, q)
				if err != nil {
					return "", err
				}
				return toJSON(report)
			},
		},
		{
			Name: "get_ai_costs",
			Description: "LLM costs: what applications pay LLM APIs per month by team and by model (LiteLLM spend, or " +
				"OpenTelemetry GenAI token counts at the model prices in Settings), models without a price, and what each " +
				"self-hosted model served by vLLM costs per million tokens at its namespace's run rate.",
			Parameters: obj(map[string]any{}),
			Run:        func(ctx context.Context, _ map[string]any) (string, error) { return s.toolAICosts(ctx) },
		},
		{
			Name: "get_gpu_usage",
			Description: "GPUs: how many the cluster has and pods request, what they cost per month, which namespaces hold " +
				"them, how busy they were (DCGM, when connected), and the GPU advice: idle GPUs, GPUs several workloads " +
				"could share through MIG or time-slicing, and cheaper GPU models with enough memory.",
			Parameters: obj(map[string]any{}),
			Run:        func(ctx context.Context, _ map[string]any) (string, error) { return s.toolGPUUsage(ctx) },
		},
		{
			Name:        "list_scaling_groups",
			Description: "All ScalingGroups with their mode, desired state, phase, readiness, next scheduled change and dependencies.",
			Parameters:  obj(map[string]any{}),
			Run:         func(ctx context.Context, _ map[string]any) (string, error) { return s.toolListGroups(ctx) },
		},
		{
			Name:        "get_scaling_group",
			Description: "Details of one ScalingGroup: namespaces, schedules, sequence, dependencies, override and conditions.",
			Parameters:  obj(map[string]any{"name": str("ScalingGroup name")}, "name"),
			Run: func(ctx context.Context, args map[string]any) (string, error) {
				return s.toolGetGroup(ctx, args["name"].(string))
			},
		},
		{
			Name: "preview_schedule",
			Description: "What a schedule does, worked out without changing anything: the namespaces and workloads it scales " +
				"(and those another schedule keeps), its timetable for the next two weeks with exceptions, the share of " +
				"time it is up, and the estimated monthly saving.",
			Parameters: obj(map[string]any{"target": target, "name": str("ScalingGroup name or namespace")}, "target", "name"),
			Run: func(ctx context.Context, args map[string]any) (string, error) {
				return s.toolPreview(ctx, args["target"].(string), args["name"].(string))
			},
		},
		{
			Name: "realized_savings",
			Description: "Whether a schedule's last scale-down saved money: the nodes its workloads ran on, which of them " +
				"autoscalers removed (and what that saves per hour against the estimate), and for each node still there " +
				"what keeps it: PodDisruptionBudgets, do-not-disrupt or safe-to-evict annotations, local storage, bare " +
				"pods, or other namespaces still using it.",
			Parameters: obj(map[string]any{"target": target, "name": str("ScalingGroup name or namespace")}, "target", "name"),
			Run: func(ctx context.Context, args map[string]any) (string, error) {
				return s.toolRealized(ctx, args["target"].(string), args["name"].(string))
			},
		},
		{
			Name:        "list_calendars",
			Description: "The shared holiday calendars schedules can use, with the days still to come.",
			Parameters:  obj(map[string]any{}),
			Run:         func(ctx context.Context, _ map[string]any) (string, error) { return s.toolListCalendars(ctx) },
		},
		{
			Name:        "list_namespace_configs",
			Description: "All ScalingConfigs (single-namespace schedules) with mode, phase and next scheduled change.",
			Parameters:  obj(map[string]any{}),
			Run:         func(ctx context.Context, _ map[string]any) (string, error) { return s.toolListConfigs(ctx) },
		},
		{
			Name:        "list_namespaces",
			Description: "Tracked namespaces sorted by estimated monthly cost, with CPU/memory usage versus requests and efficiency insights.",
			Parameters:  obj(map[string]any{}),
			Run:         func(ctx context.Context, _ map[string]any) (string, error) { return s.toolListNamespaces(ctx) },
		},
		{
			Name:        "get_namespace_status",
			Description: "One namespace in depth: pods and failing pods, requests, limits, usage, estimated cost and waste, and which group or config scales it.",
			Parameters:  obj(map[string]any{"namespace": str("Kubernetes namespace")}, "namespace"),
			Run: func(ctx context.Context, args map[string]any) (string, error) {
				return s.toolNamespaceStatus(ctx, args["namespace"].(string))
			},
		},
		{
			Name:        "get_rightsizing_recommendations",
			Description: "Per-workload CPU and memory request recommendations for a namespace, from observed demand (p95 CPU and peak memory when history is available), with the estimated monthly saving. For Java containers it also reads the JVM flags and, when VictoriaMetrics has JVM metrics, the heap: findings such as a heap at the memory limit or the default 25% heap, and a recommended heap with its saving. Advice only: nothing is changed.",
			Parameters:  obj(map[string]any{"namespace": str("Kubernetes namespace")}, "namespace"),
			Run: func(ctx context.Context, args map[string]any) (string, error) {
				return s.toolRecommendations(ctx, args["namespace"].(string))
			},
		},
		{
			Name:        "scale_group",
			Description: "Force a ScalingGroup up or down, or resume its schedule. Requires the user's confirmation.",
			Mutating:    true,
			Parameters:  obj(map[string]any{"name": str("ScalingGroup name"), "action": action, "duration": duration}, "name", "action"),
		},
		{
			Name:        "scale_namespace",
			Description: "Force a namespace with a ScalingConfig up or down, or resume its schedule. Requires the user's confirmation.",
			Mutating:    true,
			Parameters:  obj(map[string]any{"namespace": str("Kubernetes namespace"), "action": action, "duration": duration}, "namespace", "action"),
		},
		{
			Name: "add_schedule_exception",
			Description: "Keep a schedule's namespaces up or down on given dates whatever its hours say, for a holiday or a release " +
				"weekend. Changes the schedule, so only admins can run it. Requires the user's confirmation.",
			Mutating:  true,
			AdminOnly: true,
			Parameters: obj(map[string]any{
				"target": target, "name": str("ScalingGroup name or namespace"),
				"state": map[string]any{"type": "string", "enum": []string{"Up", "Down"}, "description": "Keep the namespaces up or down"},
				"start": bound("First day or moment"), "end": bound("Last day (included) or the moment it stops; defaults to the end of the start day"),
				"label": str("What the exception is for, such as Christmas break"),
			}, "target", "name", "state", "start"),
		},
		{
			Name:        "remove_schedule_exception",
			Description: "Remove a dated exception from a schedule, by its label or start. Only admins can run it. Requires the user's confirmation.",
			Mutating:    true,
			AdminOnly:   true,
			Parameters: obj(map[string]any{
				"target": target, "name": str("ScalingGroup name or namespace"), "exception": str("The exception's label or start date"),
			}, "target", "name", "exception"),
		},
		{
			Name: "apply_rightsizing",
			Description: "Apply a workload's right-sizing advice: its pod requests are set to the recommended ones, which rolls its pods; " +
				"they are put back if the new pods are OOMKilled or restart. Needs applying turned on under Settings → Right-sizing, and " +
				"refuses workloads a GitOps tool manages. Requires the user's confirmation.",
			Mutating: true,
			Parameters: obj(map[string]any{"namespace": str("Kubernetes namespace"), "kind": str("Deployment, StatefulSet or Rollout"),
				"name": str("Workload name")}, "namespace", "kind", "name"),
		},
		{
			Name: "propose_rightsizing",
			Description: "Open a pull request with a workload's right-sizing advice in the GitOps repository set up under Settings → " +
				"Right-sizing. Requires the user's confirmation.",
			Mutating: true,
			Parameters: obj(map[string]any{"namespace": str("Kubernetes namespace"), "kind": str("Deployment, StatefulSet or Rollout"),
				"name": str("Workload name")}, "namespace", "kind", "name"),
		},
		{
			Name:        "revert_optimization",
			Description: "Restore the requests and limits a namespace had before it was right-sized. Requires the user's confirmation.",
			Mutating:    true,
			Parameters:  obj(map[string]any{"namespace": str("Kubernetes namespace")}, "namespace"),
		},
	}
}

// toolsFor filters the registry for a caller: read-only tools for everyone, mutating ones
// for operators, and the ones that change a schedule's definition for admins.
func (s *Server) toolsFor(ctx context.Context) []ai.Tool {
	var out []ai.Tool
	for _, t := range s.toolRegistry() {
		if toolAllowed(ctx, t) {
			out = append(out, t)
		}
	}
	return out
}

func toolAllowed(ctx context.Context, t ai.Tool) bool {
	switch {
	case t.AdminOnly:
		return roleAllows(ctx, auth.RoleAdmin)
	case t.Mutating:
		return roleAllows(ctx, auth.RoleOperator)
	}
	return true
}

// errAdminOnly refuses a tool that changes a schedule to a caller who is not an admin.
var errAdminOnly = errors.New("this action changes a schedule and needs the admin role")

// executeMutatingTool runs a confirmed action. It returns a human-readable result.
func (s *Server) executeMutatingTool(ctx context.Context, name string, args map[string]any) (string, error) {
	var tool *ai.Tool
	for _, t := range s.toolRegistry() {
		if t.Name == name && t.Mutating {
			tool = &t
			break
		}
	}
	if tool == nil {
		return "", fmt.Errorf("unknown action %q", name)
	}
	if !toolAllowed(ctx, *tool) {
		if tool.AdminOnly {
			return "", errAdminOnly
		}
		return "", fmt.Errorf("this action needs the operator role")
	}
	if err := ai.ValidateArgs(tool.Parameters, args); err != nil {
		return "", err
	}
	arg := func(k string) string { v, _ := args[k].(string); return v }

	switch name {
	case "scale_group":
		group := &finopsv1.ScalingGroup{}
		key := client.ObjectKey{Name: arg("name"), Namespace: config.OperatorNamespace()}
		return s.applyOverride(ctx, key, group, arg("action"), arg("duration"))
	case "scale_namespace":
		cfg, err := s.configForNamespace(ctx, arg("namespace"))
		if err != nil {
			return "", err
		}
		return s.applyOverride(ctx, client.ObjectKeyFromObject(cfg), cfg, arg("action"), arg("duration"))
	case "add_schedule_exception":
		ex := finopsv1.ScheduleException{Name: arg("label"), Start: arg("start"), End: arg("end"), State: arg("state")}
		return s.changeExceptions(ctx, arg("target"), arg("name"), func(own []finopsv1.ScheduleException) ([]finopsv1.ScheduleException, string, error) {
			return append(own, ex), fmt.Sprintf("added: kept %s %s", strings.ToLower(ex.State), describeExceptionDates(ex)), nil
		})
	case "remove_schedule_exception":
		which := arg("exception")
		return s.changeExceptions(ctx, arg("target"), arg("name"), func(own []finopsv1.ScheduleException) ([]finopsv1.ScheduleException, string, error) {
			kept := slices.DeleteFunc(slices.Clone(own), func(e finopsv1.ScheduleException) bool {
				return strings.EqualFold(e.Name, which) || e.Start == which
			})
			if len(kept) == len(own) {
				return nil, "", errBadOverride{fmt.Errorf("no exception is labelled or starts %q", which)}
			}
			return kept, fmt.Sprintf("removed %d exception(s) matching %q", len(own)-len(kept), which), nil
		})
	case "apply_rightsizing":
		rec, err := s.applyRightsizing(ctx, arg("namespace"), workloadRef{Kind: arg("kind"), Name: arg("name")}, assistantActor(ctx))
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("Applied the advice to %s %s/%s in %d container(s); its new pods are watched and the old requests come back if one fails.",
			rec.Kind, arg("namespace"), rec.Name, len(rec.Containers)), nil
	case "propose_rightsizing":
		rec, err := s.proposeRightsizing(ctx, arg("namespace"), workloadRef{Kind: arg("kind"), Name: arg("name")}, assistantActor(ctx))
		if err != nil {
			return "", err
		}
		return "Opened a pull request: " + rec.PullRequest, nil
	case "revert_optimization":
		if err := s.revertNamespace(ctx, arg("namespace")); err != nil {
			return "", err
		}
		return fmt.Sprintf("Restored the original requests and limits in namespace %s.", arg("namespace")), nil
	}
	return "", fmt.Errorf("unknown action %q", name)
}

// applyOverride sets or clears the manual override of a group or config.
func (s *Server) applyOverride(ctx context.Context, key client.ObjectKey, obj client.Object, action, duration string) (string, error) {
	var active *bool
	switch action {
	case "up":
		active = new(true)
	case "down":
		active = new(false)
	}
	var until *metav1.Time
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := s.Client.Get(ctx, key, obj); err != nil {
			return err
		}
		activeField, untilField := overrideFields(obj)
		tt := s.timetableOf(ctx, obj)
		d := duration
		switch {
		case d == "forever":
			d = ""
		case d == "" && tt.NextChange(time.Now()) != nil:
			d = UntilNextTransition
		}
		resolved, deadline, err := ResolveOverride(active, nil, d, tt, time.Now())
		if err != nil {
			return errBadOverride{err}
		}
		*activeField, *untilField, until = resolved, deadline, deadline
		if resolved == nil {
			annotations := obj.GetAnnotations()
			delete(annotations, scaling.LegacyOverrideAnnotation)
			obj.SetAnnotations(annotations)
		}
		return s.Client.Update(ctx, obj)
	})
	if err != nil {
		return "", err
	}
	switch {
	case active == nil:
		return fmt.Sprintf("%s now follows its schedule again.", key.Name), nil
	case until != nil:
		return fmt.Sprintf("%s is forced %s until %s.", key.Name, action, until.UTC().Format(time.RFC1123)), nil
	default:
		return fmt.Sprintf("%s is forced %s until the override is removed.", key.Name, action), nil
	}
}

// overrideFields exposes the manual override fields of a group or config.
func overrideFields(obj client.Object) (**bool, **metav1.Time) {
	switch o := obj.(type) {
	case *finopsv1.ScalingGroup:
		return &o.Spec.Active, &o.Spec.ActiveUntil
	case *finopsv1.ScalingConfig:
		return &o.Spec.Active, &o.Spec.ActiveUntil
	}
	panic(fmt.Sprintf("unexpected override target %T", obj))
}

func (s *Server) configForNamespace(ctx context.Context, ns string) (*finopsv1.ScalingConfig, error) {
	var list finopsv1.ScalingConfigList
	if err := s.Client.List(ctx, &list, client.InNamespace(config.OperatorNamespace())); err != nil {
		return nil, err
	}
	for i := range list.Items {
		if list.Items[i].Spec.TargetNamespace == ns || list.Items[i].Name == ns {
			return &list.Items[i], nil
		}
	}
	return nil, fmt.Errorf("namespace %s has no ScalingConfig; create one on the Workload Scaling page first", ns)
}

func toJSON(v any) (string, error) {
	b, err := json.Marshal(v)
	return string(b), err
}

func (s *Server) toolClusterOverview(ctx context.Context) (string, error) {
	var nodes corev1.NodeList
	if err := s.Client.List(ctx, &nodes); err != nil {
		return "", err
	}
	var pods corev1.PodList
	if err := s.Client.List(ctx, &pods); err != nil {
		return "", err
	}
	var allocCPU, allocMem, reqCPU, reqMem resource.Quantity
	ready := 0
	for _, n := range nodes.Items {
		allocCPU.Add(*n.Status.Allocatable.Cpu())
		allocMem.Add(*n.Status.Allocatable.Memory())
		for _, c := range n.Status.Conditions {
			if c.Type == corev1.NodeReady && c.Status == corev1.ConditionTrue {
				ready++
			}
		}
	}
	running := 0
	for _, p := range pods.Items {
		if p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
			continue
		}
		running++
		cpu, mem := calculatePodRequests(p)
		reqCPU.Add(*cpu)
		reqMem.Add(*mem)
	}
	rates := s.costRates(ctx)
	return toJSON(map[string]any{
		"nodes": len(nodes.Items), "nodesReady": ready, "activePods": running,
		"allocatableCPU": allocCPU.String(), "allocatableMemory": allocMem.String(),
		"requestedCPU": reqCPU.String(), "requestedMemory": reqMem.String(),
		"estimatedMonthlyCostOfAllocatable": round2(rates.Monthly(allocCPU, allocMem)),
		"estimatedMonthlyCostOfRequests":    round2(rates.Monthly(reqCPU, reqMem)),
		"currency":                          rates.Currency,
		"pricing":                           rates.Basis,
	})
}

func (s *Server) toolCostOverview(ctx context.Context) (string, error) {
	o, err := s.finopsOverview(ctx)
	if err != nil {
		return "", err
	}
	rows := slices.Clone(o.Namespaces)
	sort.Slice(rows, func(i, j int) bool { return rows[i].MonthlyCost > rows[j].MonthlyCost })
	if len(rows) > 10 {
		rows = rows[:10]
	}
	top := make([]map[string]any, 0, len(rows))
	for _, n := range rows {
		row := map[string]any{
			"namespace": n.Name, "environment": n.Environment, "monthlyCost": round2(n.MonthlyCost),
			"monthlyIdle": round2(n.MonthlyIdle), "findings": n.Insights,
		}
		if n.Team != "" {
			row["team"] = n.Team
		}
		if n.Efficiency != nil {
			row["efficiency"] = round2(*n.Efficiency)
		}
		if n.Schedule != nil {
			row["schedule"] = n.Schedule.Name
		}
		if save := n.ScheduleSavingMonthly + ptrValue(n.RightsizingMonthly); save > 0 {
			row["couldSaveMonthly"] = round2(save)
		}
		top = append(top, row)
	}
	m := o.Monthly
	efficiency := 0.0
	if m.Provisioned > 0 {
		efficiency = m.Used / m.Provisioned
	}
	return toJSON(map[string]any{
		"currency": o.Currency, "pricing": o.Rates.Basis,
		"monthlyRunRate": map[string]any{
			"nodes": round2(m.Provisioned), "requestedByPods": round2(m.Requested), "usedByPods": round2(m.Used),
			"notRequested": round2(m.Unallocated), "requestedNotUsed": round2(m.Overprovisioned),
		},
		"costEfficiency": round2(efficiency),
		"monthToDate": map[string]any{
			"cost": round2(o.MonthToDate.Cost), "saved": round2(o.MonthToDate.Saved),
			"forecast": round2(o.MonthToDate.Forecast), "incompleteHistory": o.MonthToDate.Partial,
		},
		"savings": map[string]any{
			"schedulesMonthly": round2(o.Savings.SchedulesMonthly), "rightsizingMonthly": round2(o.Savings.RightsizingMonthly),
			"scheduleCandidatesMonthly": round2(o.Savings.ScheduleCandidatesMonthly), "namespacesStillAnalysed": o.Savings.RightsizingPending,
		},
		"topNamespaces": top,
		"opportunities": o.Opportunities,
	})
}

func ptrValue(v *float64) float64 {
	if v == nil {
		return 0
	}
	return *v
}

func describeTransition(t *finopsv1.ScheduledTransition) string {
	if t == nil {
		return ""
	}
	return fmt.Sprintf("%s at %s", t.DesiredState, t.Time.UTC().Format(time.RFC3339))
}

func (s *Server) toolListGroups(ctx context.Context) (string, error) {
	var list finopsv1.ScalingGroupList
	if err := s.Client.List(ctx, &list, client.InNamespace(config.OperatorNamespace())); err != nil {
		return "", err
	}
	out := make([]map[string]any, 0, len(list.Items))
	for _, g := range list.Items {
		out = append(out, map[string]any{
			"name": g.Name, "category": g.Spec.Category, "mode": g.Status.Mode, "desired": g.Status.DesiredState,
			"phase": g.Status.Phase, "ready": fmt.Sprintf("%d/%d", g.Status.NamespacesReady, g.Status.NamespacesTotal), "exception": g.Status.Exception,
			"nextChange": describeTransition(g.Status.NextTransition), "dependsOn": g.Spec.DependsOn,
			"requiredBy": g.Status.RequiredBy, "namespaces": len(g.Members()),
		})
	}
	return toJSON(out)
}

func (s *Server) toolGetGroup(ctx context.Context, name string) (string, error) {
	g := &finopsv1.ScalingGroup{}
	if err := s.Client.Get(ctx, client.ObjectKey{Name: name, Namespace: config.OperatorNamespace()}, g); err != nil {
		return "", fmt.Errorf("ScalingGroup %s: %w", name, err)
	}
	conds := map[string]string{}
	for _, c := range g.Status.Conditions {
		conds[c.Type] = fmt.Sprintf("%s (%s): %s", c.Status, c.Reason, c.Message)
	}
	return toJSON(map[string]any{
		"name": g.Name, "namespaces": g.Members(), "namespaceSelector": g.Spec.NamespaceSelector, "sequence": g.Spec.Sequence, "schedules": g.Spec.Schedules,
		"activation": g.Spec.Activation, "dependsOn": g.Spec.DependsOn, "override": g.Spec.Active,
		"overrideUntil": g.Spec.ActiveUntil, "dryRun": g.Spec.DryRun, "exceptions": g.Spec.Exceptions, "calendars": g.Spec.Calendars,
		"sleepPage": g.Spec.SleepPage, "idleAfter": g.Spec.IdleAfter, "status": map[string]any{
			"mode": g.Status.Mode, "desired": g.Status.DesiredState, "phase": g.Status.Phase,
			"exception": g.Status.Exception, "exceptionEndsAt": g.Status.ExceptionEndsAt, "idleUntil": g.Status.IdleUntil,
			"readyNamespaces": g.Status.ReadyNamespaces, "skippedNamespaces": g.Status.SkippedNamespaces,
			"nextChange": describeTransition(g.Status.NextTransition),
			"requiredBy": g.Status.RequiredBy, "conflictingNamespaces": g.Status.ConflictingNamespaces, "conditions": conds,
		},
	})
}

func (s *Server) toolListConfigs(ctx context.Context) (string, error) {
	var list finopsv1.ScalingConfigList
	if err := s.Client.List(ctx, &list, client.InNamespace(config.OperatorNamespace())); err != nil {
		return "", err
	}
	out := make([]map[string]any, 0, len(list.Items))
	for _, c := range list.Items {
		out = append(out, map[string]any{
			"name": c.Name, "namespace": c.Spec.TargetNamespace, "mode": c.Status.Mode,
			"desired": c.Status.DesiredState, "phase": c.Status.Phase, "nextChange": describeTransition(c.Status.NextTransition),
			"exception": c.Status.Exception, "exceptions": c.Spec.Exceptions, "calendars": c.Spec.Calendars,
		})
	}
	return toJSON(out)
}

type namespaceRow struct {
	Namespace      string   `json:"namespace"`
	CPUUsage       string   `json:"cpuUsage"`
	CPURequests    string   `json:"cpuRequests"`
	MemoryUsage    string   `json:"memoryUsage"`
	MemoryRequests string   `json:"memoryRequests"`
	MonthlyCost    float64  `json:"estimatedMonthlyCost"`
	MonthlyWaste   float64  `json:"estimatedMonthlyWaste"`
	Insights       []string `json:"insights,omitempty"`
}

// namespaceRows summarizes every tracked namespace from its latest data point.
func (s *Server) namespaceRows(ctx context.Context) ([]namespaceRow, pricing.Rates, error) {
	var list finopsv1.NamespaceFinOpsList
	if err := s.Client.List(ctx, &list, client.InNamespace(config.OperatorNamespace())); err != nil {
		return nil, pricing.Rates{}, err
	}
	rates := s.costRates(ctx)
	rows := make([]namespaceRow, 0, len(list.Items))
	for _, n := range list.Items {
		row := namespaceRow{Namespace: n.Spec.TargetNamespace, Insights: n.Status.Insights}
		if h := n.Status.History; len(h) > 0 {
			last := h[len(h)-1]
			row.CPUUsage, row.CPURequests = last.CPU.Usage, last.CPU.Requests
			row.MemoryUsage, row.MemoryRequests = last.Memory.Usage, last.Memory.Requests
			cpuReq, memReq := parseQ(last.CPU.Requests), parseQ(last.Memory.Requests)
			cpuUse, memUse := parseQ(last.CPU.Usage), parseQ(last.Memory.Usage)
			row.MonthlyCost = round2(rates.Monthly(cpuReq, memReq))
			wasteCPU, wasteMem := cpuReq.DeepCopy(), memReq.DeepCopy()
			wasteCPU.Sub(cpuUse)
			wasteMem.Sub(memUse)
			row.MonthlyWaste = round2(rates.Monthly(clampZero(wasteCPU), clampZero(wasteMem)))
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].MonthlyCost > rows[j].MonthlyCost })
	return rows, rates, nil
}

func (s *Server) toolListNamespaces(ctx context.Context) (string, error) {
	rows, rates, err := s.namespaceRows(ctx)
	if err != nil {
		return "", err
	}
	if len(rows) > 60 {
		rows = rows[:60]
	}
	return toJSON(map[string]any{"currency": rates.Currency, "pricing": rates.Basis, "namespaces": rows})
}

func (s *Server) toolNamespaceStatus(ctx context.Context, ns string) (string, error) {
	var pods corev1.PodList
	if err := s.Client.List(ctx, &pods, client.InNamespace(ns)); err != nil {
		return "", err
	}
	var problems []string
	active := 0
	for _, p := range pods.Items {
		switch p.Status.Phase {
		case corev1.PodSucceeded:
			continue
		case corev1.PodFailed:
			problems = append(problems, fmt.Sprintf("%s failed (%s)", p.Name, p.Status.Reason))
			continue
		case corev1.PodPending:
			problems = append(problems, p.Name+" is pending")
		}
		active++
		for _, cs := range p.Status.ContainerStatuses {
			if w := cs.State.Waiting; w != nil && w.Reason != "" && w.Reason != "ContainerCreating" {
				problems = append(problems, fmt.Sprintf("%s/%s: %s", p.Name, cs.Name, w.Reason))
			}
		}
	}

	result := map[string]any{"namespace": ns, "activePods": active, "problems": problems}
	rows, rates, err := s.namespaceRows(ctx)
	if err == nil {
		for _, r := range rows {
			if r.Namespace == ns {
				result["usage"] = r
				result["currency"], result["pricing"] = rates.Currency, rates.Basis
			}
		}
	}
	if nf, err := s.findNamespaceFinOps(ctx, ns); err == nil {
		if c := meta.FindStatusCondition(nf.Status.Conditions, "MetricsAvailable"); c != nil {
			result["metricsSource"] = c.Message
		}
	}
	var groups finopsv1.ScalingGroupList
	if err := s.Client.List(ctx, &groups, client.InNamespace(config.OperatorNamespace())); err == nil {
		for _, g := range groups.Items {
			if slices.Contains(g.Members(), ns) {
				result["scaledByGroup"] = g.Name
			}
		}
	}
	if cfg, err := s.configForNamespace(ctx, ns); err == nil {
		result["scalingConfig"] = map[string]any{"name": cfg.Name, "mode": cfg.Status.Mode, "phase": cfg.Status.Phase}
	}
	return toJSON(result)
}

// toolRecommendations returns the right-sizing report without the containers that need
// no change, so the model reads only what matters.
func (s *Server) toolRecommendations(ctx context.Context, ns string) (string, error) {
	report, err := s.recommendations(ctx, ns)
	if err != nil {
		return "", err
	}
	var workloads []rightsizing.WorkloadAdvice
	for _, w := range report.Workloads {
		var containers []rightsizing.ContainerAdvice
		for _, c := range w.Containers {
			if c.CPU.Action != rightsizing.ActionKeep || c.Memory.Action != rightsizing.ActionKeep || (c.JVM != nil && len(c.JVM.Findings) > 0) {
				containers = append(containers, c)
			}
		}
		if len(containers) > 0 {
			w.Containers = containers
			w.MonthlySavings = round2(w.MonthlySavings)
			workloads = append(workloads, w)
		}
	}
	report.Workloads = workloads
	report.MonthlySavings = round2(report.MonthlySavings)
	report.JavaMonthlySavings = round2(report.JavaMonthlySavings)
	return toJSON(report)
}

func parseQ(s string) resource.Quantity {
	q, err := resource.ParseQuantity(s)
	if err != nil {
		return resource.Quantity{}
	}
	return q
}

func clampZero(q resource.Quantity) resource.Quantity {
	if q.Sign() < 0 {
		return resource.Quantity{}
	}
	return q
}

func round2(f float64) float64 { return float64(int64(f*100+0.5)) / 100 }

// assistantActor is who confirmed an assistant's action.
func assistantActor(ctx context.Context) string {
	if id := auth.FromContext(ctx); id != nil {
		return id.Name
	}
	return "the assistant"
}
