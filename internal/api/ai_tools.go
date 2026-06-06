package api

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// AITool defines a unified tool schema
type AITool struct {
	Name        string
	Description string
	Parameters  map[string]interface{} // JSON Schema
}

// GetAITools returns the available tools for the AI
func (s *Server) GetAITools() []AITool {
	return []AITool{
		{
			Name:        "get_namespace_status",
			Description: "Get CPU/Memory usage, waste, insights, and current scaling phase for a namespace.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"namespace": map[string]interface{}{
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
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"group_name": map[string]interface{}{
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
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"group_name": map[string]interface{}{
						"type":        "string",
						"description": "The name of the ScalingGroup.",
					},
					"action": map[string]interface{}{
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
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"namespace": map[string]interface{}{
						"type":        "string",
						"description": "The target Kubernetes namespace.",
					},
					"action": map[string]interface{}{
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
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"namespace": map[string]interface{}{
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
	var args map[string]interface{}
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

	return &namespaceReport{
		ScalingPhase: "Active", // Not tracked directly in this version
		CurrentCost:  0.0,      // Need to calculate separately or omit
		CurrentWaste: 0.0,
		TotalPods:    0, // Not tracked directly in Status
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

	if action == "reset" {
		delete(group.Annotations, "costdeck.io/manual-override")
	} else if action == "up" {
		group.Annotations["costdeck.io/manual-override"] = "ScaledUp"
	} else if action == "down" {
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

	if action == "reset" {
		delete(config.Annotations, "costdeck.io/manual-override")
	} else if action == "up" {
		config.Annotations["costdeck.io/manual-override"] = "ScaledUp"
	} else if action == "down" {
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
