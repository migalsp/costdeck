package api

import "net/http"

func handleOpenAPISpec(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/x-yaml")
	_, _ = w.Write(openapiSpec)
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
    .costdeck-badge {
      background: #10b981;
      color: #fff;
      width: 32px; height: 32px;
      border-radius: 10px;
      display: flex;
      align-items: center;
      justify-content: center;
      font: 700 16px/1 sans-serif;
    }
  </style>
</head>
<body>
  <div class="costdeck-header">
    <div class="costdeck-badge">K</div>
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
