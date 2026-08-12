package api

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// AITool defines a unified tool schema
type AITool struct {
	Name        string
	Description string
	Parameters  map[string]any // JSON Schema
}

// GetAITools returns the available tools for the AI
func (s *Server) GetAITools() []AITool {
	return []AITool{
		{
			Name:        "get_namespace_status",
			Description: "Get CPU/Memory usage, waste, insights, pod errors, and current scaling phase for a namespace.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"namespace": map[string]any{
						"type":        "string",
						"description": "The target Kubernetes namespace.",
					},
				},
				"required": []string{"namespace"},
			},
		},
		{
			Name:        "get_scaling_group_status",
			Description: "Get the current status, schedule, and active namespaces of a ScalingGroup.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"group_name": map[string]any{
						"type":        "string",
						"description": "The name of the ScalingGroup.",
					},
				},
				"required": []string{"group_name"},
			},
		},
		{
			Name:        "scale_group",
			Description: "Force a ScalingGroup to scale up, down, or reset its state.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"group_name": map[string]any{
						"type":        "string",
						"description": "The name of the ScalingGroup.",
					},
					"action": map[string]any{
						"type":        "string",
						"description": "Action to perform: 'up', 'down', or 'reset'.",
						"enum":        []string{"up", "down", "reset"},
					},
				},
				"required": []string{"group_name", "action"},
			},
		},
		{
			Name:        "scale_config",
			Description: "Force a ScalingConfig (namespace level) to scale up, down, or reset its state.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"namespace": map[string]any{
						"type":        "string",
						"description": "The target Kubernetes namespace.",
					},
					"action": map[string]any{
						"type":        "string",
						"description": "Action to perform: 'up', 'down', or 'reset'.",
						"enum":        []string{"up", "down", "reset"},
					},
				},
				"required": []string{"namespace", "action"},
			},
		},
		{
			Name:        "optimize_namespace",
			Description: "Trigger a one-click optimization for a namespace to reduce resource waste based on AI recommendations.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"namespace": map[string]any{
						"type":        "string",
						"description": "The target Kubernetes namespace.",
					},
				},
				"required": []string{"namespace"},
			},
		},
	}
}

// ExecuteAITool executes a tool by name with JSON arguments
func (s *Server) ExecuteAITool(ctx context.Context, name string, argsJSON string) (string, error) {
	var args map[string]any
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("failed to parse tool arguments: %w", err)
	}

	switch name {
	case "get_namespace_status":
		ns, _ := args["namespace"].(string)
		report, err := s.generateNamespaceReport(ctx, ns)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("Namespace: %s\nStatus: %s\nCost: $%.2f/mo\nWaste: $%.2f/mo\nPods: %d\nInsights: %s",
			ns, report.ScalingPhase, report.CurrentCost, report.CurrentWaste, report.TotalPods, report.AIInsight), nil

	case "get_scaling_group_status":
		gn, _ := args["group_name"].(string)
		status, err := s.getScalingGroupStatus(ctx, gn)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("ScalingGroup: %s\nPhase: %s\nNamespaces: %v\nSchedule: %s\n", gn, status.Phase, status.Namespaces, status.Schedules), nil

	case "scale_group":
		gn, _ := args["group_name"].(string)
		act, _ := args["action"].(string)
		if err := s.executeScalingGroupAction(ctx, gn, act); err != nil {
			return "", err
		}
		return fmt.Sprintf("Successfully initiated '%s' action on ScalingGroup '%s'.", act, gn), nil

	case "scale_config":
		ns, _ := args["namespace"].(string)
		act, _ := args["action"].(string)
		if err := s.executeScalingConfigAction(ctx, ns, act); err != nil {
			return "", err
		}
		return fmt.Sprintf("Successfully initiated '%s' action on ScalingConfig in namespace '%s'.", act, ns), nil

	case "optimize_namespace":
		ns, _ := args["namespace"].(string)
		if err := s.triggerNamespaceOptimization(ctx, ns); err != nil {
			return "", err
		}
		return fmt.Sprintf("Optimization triggered for namespace '%s'.", ns), nil

	default:
		return "", fmt.Errorf("unknown tool: %s", name)
	}
}

// Helper structs and methods to implement the tool functions internally
type namespaceReport struct {
	ScalingPhase string
	CurrentCost  float64
	CurrentWaste float64
	TotalPods    int
	Errors       string
	AIInsight    string
}

func (s *Server) generateNamespaceReport(ctx context.Context, nsName string) (*namespaceReport, error) {
	operatorNs := os.Getenv("POD_NAMESPACE")
	if operatorNs == "" {
		operatorNs = "costdeck"
	}

	var nsFinOps finopsv1.NamespaceFinOps
	err := s.Client.Get(ctx, types.NamespacedName{Name: nsName, Namespace: operatorNs}, &nsFinOps)
	if err != nil {
		// Try to find by targetNamespace
		var list finopsv1.NamespaceFinOpsList
		if listErr := s.Client.List(ctx, &list); listErr == nil {
			for _, item := range list.Items {
				if item.Spec.TargetNamespace == nsName {
					nsFinOps = item
					err = nil
					break
				}
			}
		}
	}
	if err != nil {
		return nil, fmt.Errorf("NamespaceFinOps for %s not found: %w", nsName, err)
	}

	insightsStr := "None"
	if len(nsFinOps.Status.Insights) > 0 {
		insightsBytes, _ := json.Marshal(nsFinOps.Status.Insights)
		insightsStr = string(insightsBytes)
	}

	// Calculate from actual pods
	var totalPods int
	var monthlyCost float64
	var podErrors []string
	pods, podErr := s.K8sClient.CoreV1().Pods(nsName).List(ctx, metav1.ListOptions{})
	if podErr == nil {
		var cpuReq, memReq resource.Quantity
		for _, p := range pods.Items {
			if p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
				if p.Status.Phase == corev1.PodFailed {
					podErrors = append(podErrors, fmt.Sprintf("Pod %s failed", p.Name))
				}
				continue
			}
			for _, containerStatus := range p.Status.ContainerStatuses {
				if containerStatus.State.Waiting != nil && containerStatus.State.Waiting.Reason == "CrashLoopBackOff" {
					podErrors = append(podErrors, fmt.Sprintf("Pod %s is in CrashLoopBackOff", p.Name))
				}
			}
			totalPods++
			for _, c := range p.Spec.Containers {
				if c.Resources.Requests.Cpu() != nil {
					cpuReq.Add(*c.Resources.Requests.Cpu())
				}
				if c.Resources.Requests.Memory() != nil {
					memReq.Add(*c.Resources.Requests.Memory())
				}
			}
		}
		cpuCores := float64(cpuReq.MilliValue()) / 1000.0
		ramGb := float64(memReq.Value()) / 1024.0 / 1024.0 / 1024.0

		cpuRate, ramRate := 0.035, 0.003 // defaults
		hourlyCost := (cpuCores * cpuRate) + (ramGb * ramRate)
		monthlyCost = hourlyCost * 730
	}

	// Calculate waste from recent metrics if available
	var monthlyWaste float64
	if len(nsFinOps.Status.History) > 0 {
		latest := nsFinOps.Status.History[len(nsFinOps.Status.History)-1]
		cpuReq, _ := resource.ParseQuantity(latest.CPU.Requests)
		cpuUse, _ := resource.ParseQuantity(latest.CPU.Usage)
		memReq, _ := resource.ParseQuantity(latest.Memory.Requests)
		memUse, _ := resource.ParseQuantity(latest.Memory.Usage)

		wasteCPU := float64(cpuReq.MilliValue()-cpuUse.MilliValue()) / 1000.0
		if wasteCPU < 0 {
			wasteCPU = 0
		}
		wasteMem := float64(memReq.Value()-memUse.Value()) / 1024.0 / 1024.0 / 1024.0
		if wasteMem < 0 {
			wasteMem = 0
		}

		cpuRate, ramRate := 0.035, 0.003 // defaults
		hourlyWaste := (wasteCPU * cpuRate) + (wasteMem * ramRate)
		monthlyWaste = hourlyWaste * 730
	}

	errsStr := "None"
	if len(podErrors) > 0 {
		errsStr = strings.Join(podErrors, "; ")
	}

	return &namespaceReport{
		ScalingPhase: "Active",
		CurrentCost:  monthlyCost,
		CurrentWaste: monthlyWaste,
		TotalPods:    totalPods,
		Errors:       errsStr,
		AIInsight:    insightsStr,
	}, nil
}

type scalingGroupReport struct {
	Phase      string
	Namespaces []string
	Schedules  string
}

func (s *Server) getScalingGroupStatus(ctx context.Context, groupName string) (*scalingGroupReport, error) {
	operatorNs := os.Getenv("POD_NAMESPACE")
	if operatorNs == "" {
		operatorNs = "costdeck"
	}

	var group finopsv1.ScalingGroup
	if err := s.Client.Get(ctx, types.NamespacedName{Name: groupName, Namespace: operatorNs}, &group); err != nil {
		return nil, fmt.Errorf("ScalingGroup %s not found: %w", groupName, err)
	}

	schedule := "None"
	if len(group.Spec.Schedules) > 0 {
		sched := group.Spec.Schedules[0]
		schedule = fmt.Sprintf("Active: %s-%s (%s)", sched.StartTime, sched.EndTime, sched.Timezone)
	}

	return &scalingGroupReport{
		Phase:      group.Status.Phase,
		Namespaces: group.Spec.Namespaces,
		Schedules:  schedule,
	}, nil
}

func (s *Server) executeScalingGroupAction(ctx context.Context, groupName string, action string) error {
	operatorNs := os.Getenv("POD_NAMESPACE")
	if operatorNs == "" {
		operatorNs = "costdeck"
	}

	var group finopsv1.ScalingGroup
	if err := s.Client.Get(ctx, types.NamespacedName{Name: groupName, Namespace: operatorNs}, &group); err != nil {
		return err
	}

	if group.Annotations == nil {
		group.Annotations = make(map[string]string)
	}

	switch action {
	case "reset":
		delete(group.Annotations, "costdeck.io/manual-override")
	case "up":
		group.Annotations["costdeck.io/manual-override"] = "ScaledUp"
	case "down":
		group.Annotations["costdeck.io/manual-override"] = "ScaledDown"
	}

	return s.Client.Update(ctx, &group)
}

func (s *Server) executeScalingConfigAction(ctx context.Context, nsName string, action string) error {
	var config finopsv1.ScalingConfig
	if err := s.Client.Get(ctx, types.NamespacedName{Name: "costdeck-scaling", Namespace: nsName}, &config); err != nil {
		return err
	}

	if config.Annotations == nil {
		config.Annotations = make(map[string]string)
	}

	switch action {
	case "reset":
		delete(config.Annotations, "costdeck.io/manual-override")
	case "up":
		config.Annotations["costdeck.io/manual-override"] = "ScaledUp"
	case "down":
		config.Annotations["costdeck.io/manual-override"] = "ScaledDown"
	}

	return s.Client.Update(ctx, &config)
}

func (s *Server) triggerNamespaceOptimization(ctx context.Context, nsName string) error {
	var opt finopsv1.NamespaceOptimization
	err := s.Client.Get(ctx, types.NamespacedName{Name: nsName, Namespace: nsName}, &opt)

	// Create it if it doesn't exist to trigger reconciliation
	if err != nil {
		opt = finopsv1.NamespaceOptimization{
			ObjectMeta: metav1.ObjectMeta{
				Name:      nsName,
				Namespace: nsName,
			},
			Spec: finopsv1.NamespaceOptimizationSpec{
				TargetNamespace: nsName,
			},
		}
		if err := s.Client.Create(ctx, &opt); err != nil {
			return err
		}
	} else {
		// If it exists, nothing more to do to trigger it, maybe delete and recreate
		// Or just return it's already optimized.
		return fmt.Errorf("Optimization already exists for namespace: %s", nsName)
	}
	return nil
}
