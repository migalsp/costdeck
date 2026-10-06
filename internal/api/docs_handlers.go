package api

import (
	"net/http"
	"sync"

	"sigs.k8s.io/yaml"
)

func handleOpenAPISpec(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/x-yaml")
	_, _ = w.Write(openapiSpec)
}

// openapiJSON is the embedded spec converted once, for clients that read JSON. The
// dashboard renders its API reference from it.
var openapiJSON = sync.OnceValues(func() ([]byte, error) { return yaml.YAMLToJSON(openapiSpec) })

func handleOpenAPIJSON(w http.ResponseWriter, r *http.Request) {
	spec, err := openapiJSON()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(spec)
}

func handleSwaggerUI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(swaggerPage))
}

const swaggerPage = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <title>Cost Deck API - Swagger UI</title>
  <link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@5/swagger-ui.css">
  <style>
    body { margin: 0; background: #fafafa; }
    .swagger-ui .topbar { display: none; }
    .costdeck-header {
      background: linear-gradient(135deg, #0f172a, #1e293b);
      padding: 16px 32px;
      display: flex;
      align-items: center;
      gap: 12px;
    }
    .costdeck-header h1 {
      color: #fff;
      font: 700 20px/1 -apple-system, BlinkMacSystemFont, 'Segoe UI', system-ui, sans-serif;
      margin: 0;
      letter-spacing: -0.5px;
    }
    .costdeck-header span {
      color: #34d399;
      font: 800 10px/1 -apple-system, BlinkMacSystemFont, sans-serif;
      text-transform: uppercase;
      letter-spacing: 2px;
    }
  </style>
</head>
<body>
  <div class="costdeck-header">
    <img src="/brand/cost-deck-icon-64.png" width="32" height="32" alt="">
    <div>
      <h1>Cost Deck</h1>
      <span>API Documentation</span>
    </div>
  </div>
  <div id="swagger-ui"></div>
  <script src="https://unpkg.com/swagger-ui-dist@5/swagger-ui-bundle.js"></script>
  <script>
    SwaggerUIBundle({
      url: '/api/openapi.yaml',
      dom_id: '#swagger-ui',
      presets: [
        SwaggerUIBundle.presets.apis,
        SwaggerUIBundle.SwaggerUIStandalonePreset
      ],
      layout: 'BaseLayout',
      deepLinking: true,
      defaultModelsExpandDepth: 1,
    });
  </script>
</body>
</html>`
