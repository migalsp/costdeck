package api

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/migalsp/costdeck-operator/internal/ai"
	"github.com/migalsp/costdeck-operator/internal/auth"
	"github.com/migalsp/costdeck-operator/internal/config"
)

// mcpInstructions tells MCP clients what the server is for.
const mcpInstructions = `CostDeck exposes the FinOps view of one Kubernetes cluster: scaling groups and their schedules, namespace cost and waste estimates, right-sizing advice, and actions to scale groups and namespaces. Read tools are safe to call freely. Action tools change the cluster and require a token with the operator role.`

// mcpHandler serves the Model Context Protocol over Streamable HTTP at /mcp on the API
// port. It runs stateless, so any replica can answer any request, and it sits behind the
// same authentication as the REST API: a browser session or an API token. Read-only tools
// are offered to every caller; action tools only to operators and admins.
func (s *Server) mcpHandler() http.Handler {
	var once sync.Once
	var streamable *server.StreamableHTTPServer
	build := func() {
		srv := server.NewMCPServer("costdeck", Version,
			server.WithToolCapabilities(false),
			server.WithInstructions(mcpInstructions),
			server.WithToolFilter(func(ctx context.Context, tools []mcp.Tool) []mcp.Tool {
				if mcpCanMutate(ctx) {
					return tools
				}
				var out []mcp.Tool
				for _, t := range tools {
					if t.Annotations.ReadOnlyHint != nil && *t.Annotations.ReadOnlyHint {
						out = append(out, t)
					}
				}
				return out
			}),
		)
		for _, t := range s.toolRegistry() {
			s.registerMCPTool(srv, t)
		}
		streamable = server.NewStreamableHTTPServer(srv,
			server.WithStateLess(true),
			server.WithEndpointPath("/mcp"),
			server.WithHTTPContextFunc(func(ctx context.Context, r *http.Request) context.Context {
				return auth.WithIdentity(ctx, auth.FromContext(r.Context()))
			}),
		)
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cfg, err := config.Get(r.Context(), s.Client)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if m := cfg.Spec.Integrations.MCP; m == nil || !m.Enabled {
			writeError(w, http.StatusNotFound, "the MCP server is disabled; enable it under Settings → MCP Server")
			return
		}
		once.Do(build)
		streamable.ServeHTTP(w, r)
	})
}

// mcpCanMutate reports whether the caller may run action tools.
func mcpCanMutate(ctx context.Context) bool {
	id := auth.FromContext(ctx)
	return id == nil || id.Role.Allows(auth.RoleOperator)
}

func (s *Server) registerMCPTool(srv *server.MCPServer, t ai.Tool) {
	schema, err := json.Marshal(t.Parameters)
	if err != nil {
		return
	}
	tool := mcp.NewToolWithRawSchema(t.Name, t.Description, schema)
	tool.Annotations = mcp.ToolAnnotation{
		ReadOnlyHint:    new(!t.Mutating),
		DestructiveHint: new(t.Mutating),
		OpenWorldHint:   new(false),
	}

	srv.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		if args == nil {
			args = map[string]any{}
		}
		if t.Mutating {
			// MCP clients ask their user before calling a destructive tool; CostDeck
			// additionally requires the operator role.
			if !mcpCanMutate(ctx) {
				return mcp.NewToolResultError("this action needs a token with the operator role"), nil
			}
			out, err := s.executeMutatingTool(ctx, t.Name, args)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			who := "anonymous"
			if id := auth.FromContext(ctx); id != nil {
				who = id.Subject
			}
			logf.FromContext(ctx).Info("Executed MCP action", "action", t.Name, "args", args, "user", who)
			return mcp.NewToolResultText(out), nil
		}
		if err := ai.ValidateArgs(t.Parameters, args); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		out, err := t.Run(ctx, args)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText(out), nil
	})
}
