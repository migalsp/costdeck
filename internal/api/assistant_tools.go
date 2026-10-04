package api

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/ai"
	"github.com/migalsp/costdeck-operator/internal/config"
	"github.com/migalsp/costdeck-operator/internal/scaling"
)

// assistantSystemPrompt is deliberately static: live data comes from tools, so the prompt
// and the tool list form a stable prefix that providers can cache across conversations.
const assistantSystemPrompt = `You are CostDeck Assistant, the FinOps assistant built into the CostDeck Kubernetes operator.

CostDeck scales non-production namespaces up and down on schedules (ScalingGroups for sets of namespaces, ScalingConfigs for single namespaces), tracks namespace resource usage, and estimates cost from configured rates.

Look up live data with the tools instead of guessing, and never invent numbers. Cost figures are estimates; say so when you quote them. A group's mode tells who is in control: Schedule, ManualUp/ManualDown (a manual override that ignores the schedule), AlwaysOn (no schedule), Dependency or OnDemand (driven by groups that depend on it).

When the user asks to change something (scale a group or namespace, hand control back to the schedule, right-size or revert a namespace), call the matching tool. CostDeck then asks the user to confirm before anything changes.

Answer in concise Markdown. Prefer short tables for lists.`

// toolRegistry returns every assistant tool. Mutating tools are offered to operators and
// admins only, and even then they produce a confirmation card instead of running.
func (s *Server) toolRegistry() []ai.Tool {
	str := func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
	obj := func(props map[string]any, required ...string) map[string]any {
		return map[string]any{"type": "object", "properties": props, "required": required}
	}
	action := map[string]any{
		"type": "string", "enum": []string{"up", "down", "resume"},
		"description": "up or down forces the state; resume removes the override so the schedule decides again",
	}
	duration := str(`How long an up/down override holds: "nextTransition" (until the schedule would change state on its own; the default when there is a schedule), a duration such as "4h", or "forever"`)

	return []ai.Tool{
		{
			Name:        "get_cluster_overview",
			Description: "Cluster-wide capacity, requested resources, node and pod counts, and the estimated monthly compute cost.",
			Parameters:  obj(map[string]any{}),
			Run:         func(ctx context.Context, _ map[string]any) (string, error) { return s.toolClusterOverview(ctx) },
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
			Name:        "optimize_namespace",
			Description: "Right-size the requests and limits of a namespace's workloads from observed usage. Reversible. Requires the user's confirmation.",
			Mutating:    true,
			Parameters:  obj(map[string]any{"namespace": str("Kubernetes namespace")}, "namespace"),
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
// only when the caller may confirm them.
func (s *Server) toolsFor(canMutate bool) []ai.Tool {
	var out []ai.Tool
	for _, t := range s.toolRegistry() {
		if !t.Mutating || canMutate {
			out = append(out, t)
		}
	}
	return out
}

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
	case "optimize_namespace":
		return s.optimizeNamespace(ctx, arg("namespace"))
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
		schedules, activeField, untilField := overrideFields(obj)
		d := duration
		switch {
		case d == "forever":
			d = ""
		case d == "" && len(schedules) > 0:
			d = UntilNextTransition
		}
		resolved, deadline, err := ResolveOverride(active, nil, d, schedules, time.Now())
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

// overrideFields exposes the schedule and override fields of a group or config.
func overrideFields(obj client.Object) ([]finopsv1.ScalingSchedule, **bool, **metav1.Time) {
	switch o := obj.(type) {
	case *finopsv1.ScalingGroup:
		return o.Spec.Schedules, &o.Spec.Active, &o.Spec.ActiveUntil
	case *finopsv1.ScalingConfig:
		return o.Spec.Schedules, &o.Spec.Active, &o.Spec.ActiveUntil
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
		"estimatedMonthlyCostOfAllocatable": round2(rates.monthly(allocCPU, allocMem)),
		"estimatedMonthlyCostOfRequests":    round2(rates.monthly(reqCPU, reqMem)),
		"currency":                          rates.Currency,
		"pricing":                           rates.Basis,
	})
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
			"phase": g.Status.Phase, "ready": fmt.Sprintf("%d/%d", g.Status.NamespacesReady, g.Status.NamespacesTotal),
			"nextChange": describeTransition(g.Status.NextTransition), "dependsOn": g.Spec.DependsOn,
			"requiredBy": g.Status.RequiredBy, "namespaces": len(g.Spec.Namespaces),
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
		"name": g.Name, "namespaces": g.Spec.Namespaces, "sequence": g.Spec.Sequence, "schedules": g.Spec.Schedules,
		"activation": g.Spec.Activation, "dependsOn": g.Spec.DependsOn, "override": g.Spec.Active,
		"overrideUntil": g.Spec.ActiveUntil, "status": map[string]any{
			"mode": g.Status.Mode, "desired": g.Status.DesiredState, "phase": g.Status.Phase,
			"readyNamespaces": g.Status.ReadyNamespaces, "nextChange": describeTransition(g.Status.NextTransition),
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
func (s *Server) namespaceRows(ctx context.Context) ([]namespaceRow, priceRates, error) {
	var list finopsv1.NamespaceFinOpsList
	if err := s.Client.List(ctx, &list, client.InNamespace(config.OperatorNamespace())); err != nil {
		return nil, priceRates{}, err
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
			row.MonthlyCost = round2(rates.monthly(cpuReq, memReq))
			wasteCPU, wasteMem := cpuReq.DeepCopy(), memReq.DeepCopy()
			wasteCPU.Sub(cpuUse)
			wasteMem.Sub(memUse)
			row.MonthlyWaste = round2(rates.monthly(clampZero(wasteCPU), clampZero(wasteMem)))
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
			if slices.Contains(g.Spec.Namespaces, ns) {
				result["scaledByGroup"] = g.Name
			}
		}
	}
	if cfg, err := s.configForNamespace(ctx, ns); err == nil {
		result["scalingConfig"] = map[string]any{"name": cfg.Name, "mode": cfg.Status.Mode, "phase": cfg.Status.Phase}
	}
	return toJSON(result)
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
