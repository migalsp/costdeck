package api

import (
	"context"
	"embed"
	"errors"
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
)

// Version is set at build time via ldflags
var Version = "dev"

// Server serves the REST API and the embedded dashboard.
type Server struct {
	Client        client.Client
	K8sClient     kubernetes.Interface
	MetricsClient metricsv.Interface
	Port          string

	healthMu      sync.Mutex
	healthHistory []map[string]any
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

	go s.StartMCPServerLoop(ctx)

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
	root.Handle("/", spaHandler(ui))
	return securityHeaders(AuthMiddleware(root)), nil
}

// routes registers every API endpoint. Method-qualified patterns make the mux answer
// 405 for a wrong method and expose path parameters through r.PathValue.
func (s *Server) routes() *http.ServeMux {
	mux := http.NewServeMux()

	// Session
	mux.HandleFunc("POST /api/login", HandleLogin)
	mux.HandleFunc("POST /api/logout", HandleLogout)
	mux.HandleFunc("GET /api/version", s.handleVersion)

	// Cluster and operator
	mux.HandleFunc("GET /api/cluster-info", s.handleClusterInfo)
	mux.HandleFunc("GET /api/cluster/nodes", s.handleClusterNodes)
	mux.HandleFunc("GET /api/operator/health", s.handleOperatorHealth)
	mux.HandleFunc("GET /api/operator/logs", s.handleOperatorLogs)
	mux.HandleFunc("GET /api/operator/logs/download", s.handleOperatorLogsDownload)

	// Namespace insights and right-sizing
	mux.HandleFunc("GET /api/namespaces", s.handleNamespaces)
	mux.HandleFunc("GET /api/namespaces/{ns}/history", s.serveHistory)
	mux.HandleFunc("GET /api/namespaces/{ns}/pods", s.servePods)
	mux.HandleFunc("GET /api/namespaces/{ns}/workloads", s.serveWorkloads)
	mux.HandleFunc("PUT /api/namespaces/{ns}/workloads/{name}", s.serveWorkloadAction)
	mux.HandleFunc("POST /api/namespaces/{ns}/optimize", s.handleNamespaceOptimize)
	mux.HandleFunc("POST /api/namespaces/{ns}/revert", s.handleNamespaceRevert)
	mux.HandleFunc("GET /api/namespaces/{ns}/optimization", s.handleNamespaceOptimizationInfo)
	mux.HandleFunc("POST /api/costing", s.handleCosting)

	// Scaling
	mux.HandleFunc("GET /api/scaling/groups", s.listScalingGroups)
	mux.HandleFunc("POST /api/scaling/groups", s.createScalingGroup)
	mux.HandleFunc("GET /api/scaling/groups/{name}", s.getScalingGroup)
	mux.HandleFunc("PUT /api/scaling/groups/{name}", s.updateScalingGroup)
	mux.HandleFunc("DELETE /api/scaling/groups/{name}", s.deleteScalingGroup)
	mux.HandleFunc("POST /api/scaling/groups/{name}/manual", s.handleScalingGroupManual)
	mux.HandleFunc("GET /api/scaling/groups/{name}/events", s.handleScalingGroupEvents)
	mux.HandleFunc("GET /api/scaling/configs", s.listScalingConfigs)
	mux.HandleFunc("POST /api/scaling/configs", s.createScalingConfig)
	mux.HandleFunc("GET /api/scaling/configs/{name}", s.getScalingConfig)
	mux.HandleFunc("PUT /api/scaling/configs/{name}", s.updateScalingConfig)
	mux.HandleFunc("DELETE /api/scaling/configs/{name}", s.deleteScalingConfig)
	mux.HandleFunc("POST /api/scaling/configs/{name}/manual", s.handleScalingConfigManual)
	mux.HandleFunc("GET /api/discovery/{provider}/{type}", s.handleDiscovery)

	// Settings
	mux.HandleFunc("GET /api/settings", s.handleGetSettings)
	mux.HandleFunc("PUT /api/settings", s.handleUpdateSettings)
	mux.HandleFunc("POST /api/settings/providers/{provider}/test", s.handleTestProvider)
	mux.HandleFunc("GET /api/settings/providers/{provider}/status", s.handleProviderStatus)

	// Integrations
	mux.HandleFunc("POST /api/webex/webhook", s.handleWebexWebhook)
	mux.HandleFunc("POST /api/ai/chat", s.handleAIChat)
	mux.HandleFunc("GET /api/ai/report", s.handleAIReportGet)
	mux.HandleFunc("POST /api/ai/report/save", s.handleAIReportSave)
	mux.HandleFunc("POST /api/ai/report/generate", s.handleAIReportGenerate)

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
