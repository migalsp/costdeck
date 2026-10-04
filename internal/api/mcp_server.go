package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/migalsp/costdeck-operator/internal/ai"
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
	// The standalone MCP listener is unauthenticated, so it only exposes read-only tools.
	for _, t := range s.toolsFor(false) {
		tool := t
		schema, _ := json.Marshal(tool.Parameters)
		mcpServer.AddTool(mcp.NewToolWithRawSchema(tool.Name, tool.Description, schema),
			func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				args, _ := req.Params.Arguments.(map[string]any)
				if args == nil {
					args = map[string]any{}
				}
				if err := ai.ValidateArgs(tool.Parameters, args); err != nil {
					return mcp.NewToolResultError(err.Error()), nil
				}
				out, err := tool.Run(ctx, args)
				if err != nil {
					return mcp.NewToolResultError(err.Error()), nil
				}
				return mcp.NewToolResultText(out), nil
			})
	}

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
			config := s.currentConfig(ctx)
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
