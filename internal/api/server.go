package api

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"

	"k8s.io/client-go/kubernetes"
	metricsv "k8s.io/metrics/pkg/client/clientset/versioned"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/migalsp/costdeck-operator/internal/auth"
	"github.com/migalsp/costdeck-operator/internal/metrics"
	"github.com/migalsp/costdeck-operator/internal/pricing"
)

// Version is set at build time via ldflags
var Version = "dev"

// Permissions used by the REST API beyond what the controllers already request.
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch
// +kubebuilder:rbac:groups=metrics.k8s.io,resources=nodes,verbs=get;list;watch
// +kubebuilder:rbac:groups="",namespace=costdeck,resources=pods/log,verbs=get
// +kubebuilder:rbac:groups=finops.costdeck.io,namespace=costdeck,resources=namespaceoptimizations,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=finops.costdeck.io,namespace=costdeck,resources=namespaceoptimizations/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=coordination.k8s.io,namespace=costdeck,resources=leases,verbs=get;list;watch;create;update;patch;delete

// Server serves the REST API and the embedded dashboard.
type Server struct {
	Client        client.Client
	K8sClient     kubernetes.Interface
	MetricsClient metricsv.Interface
	Metrics       *metrics.Provider
	// Auth authenticates users. Start creates it when it is nil; handler tests leave it
	// nil, which skips authentication entirely.
	Auth *auth.Service
	Port string

	// Pricing resolves cost rates; built on first use when nil.
	Pricing     *pricing.Resolver
	pricingOnce sync.Once

	healthMu      sync.Mutex
	healthHistory []map[string]any

	// rootCtx lives as long as the server; background work started by a request uses it.
	rootCtx context.Context
}

//go:embed ui/*
var uiFS embed.FS

//go:embed openapi.yaml
var openapiSpec []byte

// NeedLeaderElection reports false: the API and the dashboard have to answer on every
// replica the Service routes to, not only on the elected leader. Without this a
// multi-replica Deployment fails every request that lands on a standby pod.
func (s *Server) NeedLeaderElection() bool { return false }

// Start implements manager.Runnable.
func (s *Server) Start(ctx context.Context) error {
	log := logf.FromContext(ctx).WithName("api-server")
	s.rootCtx = ctx

	if s.Auth == nil {
		svc, err := auth.NewService(ctx, s.Client)
		if err != nil {
			return fmt.Errorf("initialise authentication: %w", err)
		}
		s.Auth = svc
	}
	if s.Auth.Disabled(ctx) {
		log.Info("Authentication is disabled: set COSTDECK_AUTH_USER/COSTDECK_AUTH_PASSWORD or enable Entra SSO")
	}

	handler, err := s.Handler()
	if err != nil {
		return err
	}

	addr := ":" + s.Port
	if s.Port == "" {
		addr = ":8082"
	}

	server := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		// No WriteTimeout: AI chat and report generation stream for minutes.
	}

	log.Info("Starting API server", "addr", addr)

	go func() {
		<-ctx.Done()
		log.Info("Shutting down API server")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			log.Error(err, "Could not shut down API server cleanly")
		}
	}()

	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Handler builds the complete HTTP handler: API routes, the dashboard and middleware.
func (s *Server) Handler() (http.Handler, error) {
	ui, err := fs.Sub(uiFS, "ui")
	if err != nil {
		return nil, err
	}
	// API routes get a mux of their own: an unknown /api/ path must answer 404 (and a
	// wrong method 405) instead of falling through to the dashboard.
	root := http.NewServeMux()
	root.Handle("/api/", s.routes())
	root.Handle("/mcp", s.mcpHandler())
	root.Handle("/", spaHandler(ui))
	var h http.Handler = root
	if s.Auth != nil {
		h = s.Auth.Middleware(h)
	}
	return securityHeaders(h), nil
}

// routes registers every API endpoint with the minimum role it requires. Method-qualified
// patterns make the mux answer 405 for a wrong method and expose path parameters through
// r.PathValue.
func (s *Server) routes() *http.ServeMux {
	mux := http.NewServeMux()
	viewer := func(p string, h http.HandlerFunc) { mux.HandleFunc(p, auth.Require(auth.RoleViewer, h)) }
	operator := func(p string, h http.HandlerFunc) { mux.HandleFunc(p, auth.Require(auth.RoleOperator, h)) }
	admin := func(p string, h http.HandlerFunc) { mux.HandleFunc(p, auth.Require(auth.RoleAdmin, h)) }

	// Session and single sign-on (public; the auth middleware lets them through).
	if s.Auth != nil {
		mux.HandleFunc("POST /api/login", s.Auth.HandleLogin)
		mux.HandleFunc("POST /api/logout", s.Auth.HandleLogout)
		mux.HandleFunc("GET /api/auth/config", s.Auth.HandleConfig)
		mux.HandleFunc("GET /api/auth/me", s.Auth.HandleMe)
		mux.HandleFunc("GET /api/auth/entra/login", s.Auth.Entra.HandleLogin)
		mux.HandleFunc("GET /api/auth/entra/callback", s.Auth.Entra.HandleCallback)
		mux.HandleFunc("POST /api/auth/entra/callback-spa", s.Auth.Entra.HandleSPACallback)
	}
	viewer("GET /api/version", s.handleVersion)

	// Cluster and operator
	viewer("GET /api/cluster-info", s.handleClusterInfo)
	viewer("GET /api/cluster/nodes", s.handleClusterNodes)
	viewer("GET /api/operator/health", s.handleOperatorHealth)
	operator("GET /api/operator/logs", s.handleOperatorLogs)
	admin("GET /api/operator/logs/download", s.handleOperatorLogsDownload)

	// Namespace insights and right-sizing
	viewer("GET /api/namespaces", s.handleNamespaces)
	viewer("GET /api/namespaces/{ns}/history", s.serveHistory)
	viewer("GET /api/namespaces/{ns}/pods", s.servePods)
	viewer("GET /api/namespaces/{ns}/workloads", s.serveWorkloads)
	operator("PUT /api/namespaces/{ns}/workloads/{name}", s.serveWorkloadAction)
	operator("POST /api/namespaces/{ns}/optimize", s.handleNamespaceOptimize)
	operator("POST /api/namespaces/{ns}/revert", s.handleNamespaceRevert)
	viewer("GET /api/namespaces/{ns}/optimization", s.handleNamespaceOptimizationInfo)
	viewer("POST /api/costing", s.handleCosting)

	// Scaling
	viewer("GET /api/scaling/groups", s.listScalingGroups)
	admin("POST /api/scaling/groups", s.createScalingGroup)
	viewer("GET /api/scaling/groups/{name}", s.getScalingGroup)
	admin("PUT /api/scaling/groups/{name}", s.updateScalingGroup)
	admin("DELETE /api/scaling/groups/{name}", s.deleteScalingGroup)
	operator("POST /api/scaling/groups/{name}/manual", s.handleScalingGroupManual)
	viewer("GET /api/scaling/groups/{name}/events", s.handleScalingGroupEvents)
	viewer("GET /api/scaling/configs", s.listScalingConfigs)
	admin("POST /api/scaling/configs", s.createScalingConfig)
	viewer("GET /api/scaling/configs/{name}", s.getScalingConfig)
	admin("PUT /api/scaling/configs/{name}", s.updateScalingConfig)
	admin("DELETE /api/scaling/configs/{name}", s.deleteScalingConfig)
	operator("POST /api/scaling/configs/{name}/manual", s.handleScalingConfigManual)
	viewer("GET /api/discovery/{provider}/{type}", s.handleDiscovery)

	// Settings
	viewer("GET /api/settings", s.handleGetSettings)
	admin("PUT /api/settings", s.handleUpdateSettings)
	admin("POST /api/settings/providers/{provider}/test", s.handleTestProvider)
	viewer("GET /api/settings/providers/{provider}/status", s.handleProviderStatus)
	admin("POST /api/settings/ai/models", s.handleAIModels)
	admin("GET /api/tokens", s.listTokens)
	admin("POST /api/tokens", s.createToken)
	admin("DELETE /api/tokens/{name}", s.deleteToken)

	// Integrations
	mux.HandleFunc("POST /api/webex/webhook", s.handleWebexWebhook) // HMAC-authenticated
	viewer("POST /api/ai/chat", s.handleAIChat)
	operator("POST /api/ai/tools/{name}", s.handleAIExecuteTool) // runs an action the user confirmed
	viewer("GET /api/ai/report", s.handleAIReportGet)
	operator("POST /api/ai/report/save", s.handleAIReportSave)
	operator("POST /api/ai/report/generate", s.handleAIReportGenerate)

	// Documentation
	mux.HandleFunc("GET /api/openapi.yaml", handleOpenAPISpec)
	mux.HandleFunc("GET /api/docs", handleSwaggerUI)

	return mux
}

// spaHandler serves the embedded single-page app. Paths that are not a file fall back to
// index.html so client-side routes (for example the SSO callback) survive a reload.
func spaHandler(ui fs.FS) http.Handler {
	files := http.FileServerFS(ui)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name == "" {
			name = "index.html"
		}
		if info, err := fs.Stat(ui, name); err != nil || info.IsDir() {
			w.Header().Set("Cache-Control", "no-cache")
			http.ServeFileFS(w, r, ui, "index.html")
			return
		}
		if strings.HasPrefix(name, "assets/") {
			// Vite fingerprints everything under assets/, so it can be cached forever.
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		files.ServeHTTP(w, r)
	})
}

// securityHeaders adds baseline browser hardening headers to every response.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"version": strings.TrimPrefix(Version, "v")})
}
