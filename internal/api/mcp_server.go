package api

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

var (
	mcpServer  *server.MCPServer
	sseServer  *server.SSEServer
	mcpHttpSrv *http.Server
)

func (s *Server) initMCPServer() {
	if mcpServer != nil {
		return
	}

	mcpServer = server.NewMCPServer("costdeck-mcp", "1.0.0")

	// Tool: get_namespace_status
	toolGetNamespaceStatus := mcp.NewTool("get_namespace_status",
		mcp.WithDescription("Get CPU/Memory usage, waste, insights, and current scaling phase for a namespace."),
		mcp.WithString("namespace", mcp.Required(), mcp.Description("The target Kubernetes namespace.")),
	)
	mcpServer.AddTool(toolGetNamespaceStatus, s.mcpGetNamespaceStatusHandler)

	// Tool: scale_group
	toolScaleGroup := mcp.NewTool("scale_group",
		mcp.WithDescription("Force a ScalingGroup to scale up, down, or reset its state."),
		mcp.WithString("group_name", mcp.Required(), mcp.Description("The name of the ScalingGroup.")),
		mcp.WithString("action", mcp.Required(), mcp.Description("Action to perform: 'up', 'down', or 'reset'.")),
	)
	mcpServer.AddTool(toolScaleGroup, s.mcpScaleGroupHandler)

	// Tool: scale_config
	toolScaleConfig := mcp.NewTool("scale_config",
		mcp.WithDescription("Force a ScalingConfig (namespace level) to scale up, down, or reset its state."),
		mcp.WithString("namespace", mcp.Required(), mcp.Description("The target Kubernetes namespace containing the ScalingConfig.")),
		mcp.WithString("action", mcp.Required(), mcp.Description("Action to perform: 'up', 'down', or 'reset'.")),
	)
	mcpServer.AddTool(toolScaleConfig, s.mcpScaleConfigHandler)

	// Tool: optimize_namespace
	toolOptimizeNamespace := mcp.NewTool("optimize_namespace",
		mcp.WithDescription("Trigger a one-click optimization for a namespace to reduce resource waste based on AI recommendations."),
		mcp.WithString("namespace", mcp.Required(), mcp.Description("The target Kubernetes namespace.")),
	)
	mcpServer.AddTool(toolOptimizeNamespace, s.mcpOptimizeNamespaceHandler)

	sseServer = server.NewSSEServer(mcpServer)
	logf.Log.Info("MCP Server initialized internally")
}

// StartMCPServerLoop watches the config and starts/stops the MCP HTTP server
func (s *Server) StartMCPServerLoop(ctx context.Context) {
	s.initMCPServer()

	log := logf.Log.WithName("mcp-server")
	var currentPort int
	var currentEnabled bool

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			if mcpHttpSrv != nil {
				mcpHttpSrv.Shutdown(context.Background())
			}
			return
		case <-ticker.C:
			config := s.getOrCreateDefaultConfig(ctx)
			enabled := false
			port := 8083

			if config.Spec.Integrations.MCP != nil {
				enabled = config.Spec.Integrations.MCP.Enabled
				if config.Spec.Integrations.MCP.Port > 0 {
					port = config.Spec.Integrations.MCP.Port
				}
			}

			if enabled != currentEnabled || port != currentPort {
				if mcpHttpSrv != nil {
					log.Info("Shutting down existing MCP server due to config change")
					mcpHttpSrv.Shutdown(context.Background())
					mcpHttpSrv = nil
				}

				if enabled {
					log.Info("Starting MCP Server", "port", port)
					mux := http.NewServeMux()
					mux.Handle("/sse", sseServer.SSEHandler())
					mux.Handle("/messages", sseServer.MessageHandler())

					mcpHttpSrv = &http.Server{
						Addr:    fmt.Sprintf(":%d", port),
						Handler: mux,
					}

					go func() {
						if err := mcpHttpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
							log.Error(err, "MCP Server failed")
						}
					}()
				}

				currentEnabled = enabled
				currentPort = port
			}
		}
	}
}

// ─── Tool Execution Handlers ────────────────────────────────────────────────

func (s *Server) mcpGetNamespaceStatusHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args, ok := request.Params.Arguments.(map[string]any)
	if !ok {
		return mcp.NewToolResultError("invalid arguments format"), nil
	}
	namespace, ok := args["namespace"].(string)
	if !ok || namespace == "" {
		return mcp.NewToolResultError("namespace is required"), nil
	}

	report, err := s.generateNamespaceReport(ctx, namespace)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to get namespace report: %v", err)), nil
	}

	res := fmt.Sprintf("Namespace: %s\nStatus: %s\nCost: $%.2f/month\nWaste: $%.2f/month\nPods: %d\nInsights: %s",
		namespace, report.ScalingPhase, report.CurrentCost, report.CurrentWaste, report.TotalPods, report.AIInsight)

	return mcp.NewToolResultText(res), nil
}

func (s *Server) mcpScaleGroupHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args, ok := request.Params.Arguments.(map[string]any)
	if !ok {
		return mcp.NewToolResultError("invalid arguments format"), nil
	}
	groupName, _ := args["group_name"].(string)
	action, _ := args["action"].(string)
	if groupName == "" || action == "" {
		return mcp.NewToolResultError("group_name and action are required"), nil
	}

	if action != "up" && action != "down" && action != "reset" {
		return mcp.NewToolResultError("action must be 'up', 'down', or 'reset'"), nil
	}

	err := s.executeScalingGroupAction(ctx, groupName, action)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("Failed to scale group %s: %v", groupName, err)), nil
	}

	return mcp.NewToolResultText(fmt.Sprintf("Successfully initiated '%s' action on ScalingGroup '%s'.", action, groupName)), nil
}

func (s *Server) mcpScaleConfigHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args, ok := request.Params.Arguments.(map[string]any)
	if !ok {
		return mcp.NewToolResultError("invalid arguments format"), nil
	}
	namespace, _ := args["namespace"].(string)
	action, _ := args["action"].(string)
	if namespace == "" || action == "" {
		return mcp.NewToolResultError("namespace and action are required"), nil
	}

	if action != "up" && action != "down" && action != "reset" {
		return mcp.NewToolResultError("action must be 'up', 'down', or 'reset'"), nil
	}

	err := s.executeScalingConfigAction(ctx, namespace, action)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("Failed to scale config in namespace %s: %v", namespace, err)), nil
	}

	return mcp.NewToolResultText(fmt.Sprintf("Successfully initiated '%s' action on ScalingConfig in namespace '%s'.", action, namespace)), nil
}

func (s *Server) mcpOptimizeNamespaceHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args, ok := request.Params.Arguments.(map[string]any)
	if !ok {
		return mcp.NewToolResultError("invalid arguments format"), nil
	}
	namespace, _ := args["namespace"].(string)
	if namespace == "" {
		return mcp.NewToolResultError("namespace is required"), nil
	}

	err := s.triggerNamespaceOptimization(ctx, namespace)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("Failed to optimize namespace %s: %v", namespace, err)), nil
	}

	return mcp.NewToolResultText(fmt.Sprintf("Optimization triggered for namespace '%s'. Recommendations are being applied.", namespace)), nil
}
