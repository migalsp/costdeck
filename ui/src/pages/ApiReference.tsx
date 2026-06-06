import { useState, type ReactNode } from 'react'
import { BookOpen, ChevronRight, Copy, Check, Lock, Book, Code2, Server, MessageSquare, Bell, Shield, BarChart3, Zap, Database, Eye, Wrench, Plug } from 'lucide-react'

// ─── Guides Data ─────────────────────────────────────────────────────────────

interface GuideSection {
  id: string;
  title: string;
  icon: ReactNode;
  sections: { heading: string; body: string; code?: string }[];
}

const guides: GuideSection[] = [
  {
    id: 'intro',
    title: 'Getting Started',
    icon: <Book size={18} />,
    sections: [
      {
        heading: 'What is CostDeck?',
        body: 'CostDeck is a Kubernetes-native FinOps operator that helps you understand, control, and reduce cloud spending. It runs inside your cluster and provides real-time cost visibility, automated scaling, AI-powered recommendations, and Webex notifications - all from a single dashboard.',
      },
      {
        heading: 'How it works',
        body: 'CostDeck watches every namespace in your cluster and automatically creates a NamespaceFinOps resource for each one that contains running pods. It then polls resource usage every minute and stores 60 minutes of history, giving you live cost and utilization data without any manual setup.',
      },
      {
        heading: 'Installation',
        body: 'CostDeck is distributed as a Helm chart. After installation, the operator automatically discovers namespaces and starts collecting metrics.',
        code: `# Install via Helm
helm install costdeck-operator ./deploy/helm/costdeck-operator \\
  -n costdeck --create-namespace \\
  -f costdeck-values.yaml

# Verify the operator is running
kubectl get pods -n costdeck`
      },
      {
        heading: 'Accessing the Dashboard',
        body: 'The UI is embedded directly in the operator. Forward the service port to your local machine and open it in a browser.',
        code: `kubectl port-forward svc/costdeck-operator-api -n costdeck 8082:8082

# Open http://localhost:8082 in your browser`
      },
      {
        heading: 'Default Credentials',
        body: 'If authentication is enabled, the default username is costdeck-admin. The password is auto-generated and stored in a Kubernetes Secret.',
        code: `# Retrieve the auto-generated password
kubectl get secret costdeck-operator-admin-credentials \\
  -n costdeck -o jsonpath='{.data.password}' | base64 -d`
      },
    ]
  },
  {
    id: 'monitoring',
    title: 'Monitoring & Insights',
    icon: <Eye size={18} />,
    sections: [
      {
        heading: 'Namespace Insights Dashboard',
        body: 'The main dashboard shows all discovered namespaces with real-time CPU and Memory usage, limits, and estimated monthly cost ($). Namespaces are automatically tagged with insights like "Overprovisioned", "Missing Requests", or "Underutilized".',
      },
      {
        heading: 'Via UI',
        body: 'Open the sidebar → Namespace Insights. You\'ll see a card for each namespace showing current CPU/Memory usage, monthly cost estimate, and insight tags. Click any card to drill into the Namespace Detail view.',
      },
      {
        heading: 'Via API',
        body: 'Use the /api/namespaces endpoint to get all monitored namespaces. For a specific namespace\'s historical data, pods, and workloads, use the sub-routes.',
        code: `# List all monitored namespaces
curl -b cookies.txt http://localhost:8082/api/namespaces

# Get 60-min usage history for a namespace
curl -b cookies.txt http://localhost:8082/api/namespaces/frontend/history

# List pods with resource metrics
curl -b cookies.txt http://localhost:8082/api/namespaces/frontend/pods

# List Deployments and StatefulSets
curl -b cookies.txt http://localhost:8082/api/namespaces/frontend/workloads`
      },
      {
        heading: 'Cluster Overview',
        body: 'The Cluster Dashboard shows a heatmap of per-node resource utilization, total cluster capacity, and aggregate cost breakdown. Use it to identify hot nodes and imbalanced workload distribution.',
      },
      {
        heading: 'Via UI',
        body: 'Open the sidebar → Cluster Overview. The heatmap shows CPU and Memory utilization for each node. Hover over a node to see detailed metrics. The cost breakdown at the top shows total cluster spend.',
      },
      {
        heading: 'Via API',
        body: 'Use /api/cluster-info for a quick summary and /api/cluster/nodes for per-node metrics.',
        code: `# Cluster summary (node count, total CPU/Memory)
curl -b cookies.txt http://localhost:8082/api/cluster-info

# Per-node resource metrics
curl -b cookies.txt http://localhost:8082/api/cluster/nodes`
      },
      {
        heading: 'Operator Health',
        body: 'Monitor the CostDeck operator itself.',
      },
      {
        heading: 'Via UI',
        body: 'Open the sidebar → Operator Health. You\'ll see the operator\'s own CPU/Memory consumption, goroutine count, managed namespace count, and a live history graph. Use the "View Logs" and "Download Logs" buttons to debug issues.',
      },
      {
        heading: 'Via API',
        body: 'Use /api/operator/health for runtime metrics and /api/operator/logs for log output.',
        code: `# Operator runtime metrics
curl -b cookies.txt http://localhost:8082/api/operator/health

# Last 100 lines of logs (plain text)
curl -b cookies.txt http://localhost:8082/api/operator/logs

# Download full log file
curl -b cookies.txt http://localhost:8082/api/operator/logs/download -o operator.log`
      },
    ]
  },
  {
    id: 'scaling',
    title: 'Scaling & Schedules',
    icon: <Zap size={18} />,
    sections: [
      {
        heading: 'How Scaling Works',
        body: 'When a ScalingGroup or ScalingConfig determines that a namespace should be "inactive" (outside its schedule), CostDeck scales all Deployments and StatefulSets to 0 replicas. The original replica counts are saved so they can be restored when the schedule becomes active again. On scale-down with a sequence defined, the order is automatically reversed.',
      },
      {
        heading: 'Creating a Scaling Group',
        body: 'A ScalingGroup manages multiple namespaces under one schedule.',
      },
      {
        heading: 'Via UI',
        body: 'Go to sidebar → Scaling Management → Groups tab → click "Create Group". Fill in the group name, category, select namespaces, and define your schedule (days, start/end time, timezone). Click Save.',
      },
      {
        heading: 'Via API',
        body: 'Send a POST request to /api/scaling/groups with the group definition.',
        code: `curl -b cookies.txt -X POST http://localhost:8082/api/scaling/groups \\
  -H "Content-Type: application/json" \\
  -d '{
  "metadata": { "name": "staging" },
  "spec": {
    "category": "Environment",
    "namespaces": ["staging-frontend", "staging-backend"],
    "schedules": [{
      "days": [1,2,3,4,5],
      "startTime": "08:00",
      "endTime": "20:00",
      "timezone": "America/New_York"
    }]
  }
}'`
      },
      {
        heading: 'Via Custom Resource',
        body: 'Apply a ScalingGroup YAML directly with kubectl.',
        code: `apiVersion: finops.costdeck.io/v1
kind: ScalingGroup
metadata:
  name: staging
  namespace: costdeck
spec:
  category: Environment
  namespaces:
    - staging-frontend
    - staging-backend
  schedules:
    - days: [1, 2, 3, 4, 5]
      startTime: "08:00"
      endTime: "20:00"
      timezone: "America/New_York"
  # Optional: define scaling order
  sequence:
    - staging-backend
    - staging-frontend`
      },
      {
        heading: 'Manual Override (Scale Up / Down)',
        body: 'Force a group or namespace to scale regardless of its schedule.',
      },
      {
        heading: 'Via UI',
        body: 'On the Scaling Management page, each group and config has an "Activate" and "Deactivate" button. Click them to force scale up or down immediately. A "Reset to Schedule" option returns to automatic behavior.',
      },
      {
        heading: 'Via API',
        body: 'Send a POST to the /manual endpoint of the group or config.',
        code: `# Force scale up a group
curl -b cookies.txt -X POST \\
  http://localhost:8082/api/scaling/groups/staging/manual \\
  -H "Content-Type: application/json" \\
  -d '{ "active": true }'

# Force scale down
curl -b cookies.txt -X POST \\
  http://localhost:8082/api/scaling/groups/staging/manual \\
  -d '{ "active": false }'`
      },
      {
        heading: 'Via Custom Resource',
        body: 'Patch the active field on the CRD. Set to null to return to schedule.',
        code: `# Force scale up
kubectl patch scalinggroup staging -n costdeck \\
  --type merge -p '{"spec":{"active": true}}'

# Return to schedule-driven behavior
kubectl patch scalinggroup staging -n costdeck \\
  --type merge -p '{"spec":{"active": null}}'`
      },
      {
        heading: 'Scaling Configs (Per-Namespace)',
        body: 'A ScalingConfig gives fine-grained control for a single namespace: exclusions (workloads that never scale down), workload-level sequences, and independent schedules.',
      },
      {
        heading: 'Via UI',
        body: 'Go to Scaling Management → Configs tab → click "Create Config". Select the target namespace, define its schedule, add exclusions (e.g., "apps/v1:Deployment/prometheus"), and optionally define a workload sequence.',
      },
      {
        heading: 'Via Custom Resource',
        body: 'Apply a ScalingConfig YAML.',
        code: `apiVersion: finops.costdeck.io/v1
kind: ScalingConfig
metadata:
  name: production-backend
  namespace: costdeck
spec:
  targetNamespace: production-backend
  exclusions:
    - "apps/v1:Deployment/prometheus"
    - "apps/v1:StatefulSet/grafana"
  sequence:
    - "apps/v1:Deployment/redis"
    - "apps/v1:Deployment/*"`
      },
      {
        heading: 'Feature Flags',
        body: 'ScalingGroups support optional feature flags. skipOnTimeout: if a namespace doesn\'t reach target state within timeoutMinutes (default 5, max 30), skip it instead of blocking the pipeline.',
        code: `# Add to a ScalingGroup spec:
featureFlags:
  skipOnTimeout: true
  timeoutMinutes: 5`
      },
      {
        heading: 'Scaling Phases',
        body: 'Each ScalingGroup and ScalingConfig reports its current phase in the status: ScaledUp (all workloads running), ScalingUp (starting), ScalingDown (terminating), ScaledDown (all at 0 replicas). You can see this in the UI cards, via the API, or by inspecting the CR status.',
      },
    ]
  },
  {
    id: 'optimization',
    title: 'Resource Optimization',
    icon: <Wrench size={18} />,
    sections: [
      {
        heading: 'What is Optimization?',
        body: 'CostDeck can automatically right-size CPU and Memory requests for all workloads in a namespace based on actual usage data. This reduces waste and directly lowers your cloud bill.',
      },
      {
        heading: 'Via UI',
        body: 'Go to Namespace Insights → click a namespace card → in the detail view, click the "Optimize" button. CostDeck will analyze usage and apply new resource requests. A green "Optimized" badge will appear. To undo, click "Revert".',
      },
      {
        heading: 'Via API',
        body: 'Use the optimize and revert endpoints.',
        code: `# Optimize a namespace
curl -b cookies.txt -X POST \\
  http://localhost:8082/api/namespaces/frontend/optimize

# Check current optimization status
curl -b cookies.txt \\
  http://localhost:8082/api/namespaces/frontend/optimization

# Revert to original values
curl -b cookies.txt -X POST \\
  http://localhost:8082/api/namespaces/frontend/revert`
      },
      {
        heading: 'VictoriaMetrics Integration',
        body: 'For more accurate optimization, connect CostDeck to VictoriaMetrics (or any PromQL-compatible backend). This extends the lookback window from the default 60 minutes (metrics-server) to days or weeks.',
      },
      {
        heading: 'Via UI',
        body: 'Go to Settings → Integrations → VictoriaMetrics. Toggle "Enabled", paste your PromQL endpoint URL, set the retention days (lookback period), and optionally add a Secret reference for authentication. Click Save.',
      },
      {
        heading: 'Via Custom Resource',
        body: 'Configure in the CostDeckConfig CRD.',
        code: `# In CostDeckConfig spec:
integrations:
  victoriaMetrics:
    enabled: true
    endpoint: "http://vmselect.monitoring.svc:8481/select/0/prometheus"
    retentionDays: 7
    secretRef: vm-creds  # Optional: Secret with BEARER_TOKEN`
      },
    ]
  },
  {
    id: 'costing',
    title: 'Cost Estimation',
    icon: <BarChart3 size={18} />,
    sections: [
      {
        heading: 'How Costs Are Calculated',
        body: 'CostDeck estimates infrastructure costs using a mathematical model based on CPU and Memory allocation. It auto-detects your cloud provider from node metadata and applies market-rate pricing.',
      },
      {
        heading: 'Via UI',
        body: 'Costs are displayed automatically throughout the UI: on each namespace card (monthly cost estimate), in the Cluster Dashboard (total cluster spend), and in the Node heatmap (per-node cost). No configuration needed - it works out of the box.',
      },
      {
        heading: 'Via API',
        body: 'Use /api/costing for on-demand cost calculations.',
        code: `# Calculate cost for a namespace
curl -b cookies.txt -X POST http://localhost:8082/api/costing \\
  -H "Content-Type: application/json" \\
  -d '{
  "targetType": "namespace",
  "targetName": "frontend"
}'

# Response:
# {
#   "hourlyCost": 0.096,
#   "monthlyCost": 70.08,
#   "currency": "USD",
#   "determinedBy": "Heuristic Math Pricing (aws)"
# }`
      },
      {
        heading: 'Default Pricing Rates',
        body: 'CostDeck auto-detects your provider from node.spec.providerID (aws://, azure://, gce://) and applies these hourly rates:',
        code: `# Provider     │ CPU ($/hr) │ Memory ($/GB/hr)
# ─────────────┼────────────┼─────────────────
# AWS          │  $0.040    │  $0.004
# Azure        │  $0.042    │  $0.005
# GCP          │  $0.038    │  $0.004
# Local/On-Prem│  $0.035    │  $0.003
#
# Monthly cost = (CPU cores × rate + Memory GB × rate) × 730 hrs`
      },
      {
        heading: 'Cloud Pricing API',
        body: 'For more accurate pricing, enable the Cloud Pricing API feature. This queries official APIs (AWS Pricing API, Azure Retail Prices, GCP Cloud Billing) instead of using heuristic math.',
      },
      {
        heading: 'Via UI',
        body: 'Go to Settings → Features → toggle "Cloud Pricing API" on. Save. Costs will be recalculated using live API rates on the next refresh.',
      },
    ]
  },
  {
    id: 'external',
    title: 'External Resources',
    icon: <Database size={18} />,
    sections: [
      {
        heading: 'What Are External Targets?',
        body: 'CostDeck can scale cloud resources outside Kubernetes alongside your in-cluster workloads. Currently supported: AWS Aurora clusters and EC2 instances. This lets you stop your dev database when you scale down your dev environment.',
      },
      {
        heading: 'Via UI',
        body: 'In Scaling Management → edit a group → scroll to "External Targets". Use the "Discover" button to auto-detect AWS resources by tag, or add them manually by specifying provider, type (aurora/ec2), identifier, and region.',
      },
      {
        heading: 'Via API',
        body: 'Use the discovery endpoint to find resources, then include them when creating/updating a ScalingGroup.',
        code: `# Discover AWS Aurora clusters
curl -b cookies.txt http://localhost:8082/api/discovery/aws?type=aurora

# Discover EC2 instances
curl -b cookies.txt http://localhost:8082/api/discovery/aws?type=ec2`
      },
      {
        heading: 'Via Custom Resource',
        body: 'Add externalTargets to your ScalingGroup spec.',
        code: `apiVersion: finops.costdeck.io/v1
kind: ScalingGroup
metadata:
  name: dev-environment
spec:
  category: Environment
  namespaces: [dev-frontend, dev-backend]
  externalTargets:
    - provider: aws
      type: aurora
      identifier: my-dev-db
      region: us-east-1
      executeAfter: dev-backend`
      },
      {
        heading: 'AWS Credentials',
        body: 'CostDeck uses the AWS SDK default credential chain. For EKS, IRSA (IAM Roles for Service Accounts) is recommended. Alternatively, configure static credentials.',
      },
      {
        heading: 'Via UI',
        body: 'Go to Settings → Providers → AWS. Toggle "Enabled", enter your Region, paste Access Key and Secret Key, and optionally add Discovery Tags to filter resources. Click "Save & Test" to verify connectivity.',
      },
      {
        heading: 'Via kubectl',
        body: 'Create a Kubernetes Secret with your AWS credentials.',
        code: `kubectl create secret generic aws-credentials -n costdeck \\
  --from-literal=AWS_ACCESS_KEY_ID=AKIA... \\
  --from-literal=AWS_SECRET_ACCESS_KEY=wJalr... \\
  --from-literal=AWS_REGION=us-east-1`
      },
    ]
  },
  {
    id: 'ai',
    title: 'AI Integration',
    icon: <MessageSquare size={18} />,
    sections: [
      {
        heading: 'Overview',
        body: 'CostDeck has a built-in AI assistant that can analyze your cluster, answer cost questions, and generate professional FinOps reports. It supports OpenAI, Anthropic, Gemini, and local LLMs (Ollama).',
      },
      {
        heading: 'Configuring an AI Provider',
        body: 'Before using AI features, you need to configure a provider and API key.',
      },
      {
        heading: 'Via UI',
        body: 'Go to Settings → Integrations → AI. Toggle "Enabled", select your provider (OpenAI, Anthropic, Gemini, or Local), enter the model name and API key. For Local/Ollama, also set the Base URL. Click Save.',
      },
      {
        heading: 'Via API',
        body: 'Update the AI settings via the settings endpoint.',
        code: `curl -b cookies.txt -X PUT http://localhost:8082/api/settings \\
  -H "Content-Type: application/json" \\
  -d '{
  "integrations": {
    "ai": {
      "enabled": true,
      "provider": "openai",
      "model": "gpt-4o",
      "apiKey": "sk-..."
    }
  }
}'`
      },
      {
        heading: 'AI Chat',
        body: 'Ask natural-language questions about your cluster. The AI has access to real-time topology, resource metrics, and scaling status.',
      },
      {
        heading: 'Via UI',
        body: 'Click the floating brain icon (💡) in the bottom-right corner of the UI. Type your question and press Enter. Responses are streamed in real-time. Example questions: "Which namespaces waste the most money?", "How much will I save if I scale down staging?", "Compare CPU efficiency across all namespaces".',
      },
      {
        heading: 'Via API',
        body: 'Send a POST to /api/ai/chat. The response is streamed as Server-Sent Events (SSE).',
        code: `curl -b cookies.txt -X POST http://localhost:8082/api/ai/chat \\
  -H "Content-Type: application/json" \\
  -d '{
  "prompt": "Which namespaces are wasting money?",
  "messages": []
}'`
      },
      {
        heading: 'AI FinOps Reports',
        body: 'Generate a comprehensive FinOps report with Executive Summary, per-namespace financial breakdown, waste hotspots, and right-sizing recommendations.',
      },
      {
        heading: 'Via UI',
        body: 'Go to sidebar → Reporting → AI Reports. Click "Generate New Report". The report streams in real-time and is automatically saved. On your next visit, the last generated report is shown. You can also export it as PDF.',
      },
      {
        heading: 'Via API',
        body: 'Use the report endpoints for generation and retrieval.',
        code: `# Generate a new report (streaming SSE)
curl -b cookies.txt -X POST \\
  http://localhost:8082/api/ai/report/generate

# Get the last saved report
curl -b cookies.txt http://localhost:8082/api/ai/report

# Save a report
curl -b cookies.txt -X PUT \\
  http://localhost:8082/api/ai/report/save \\
  -H "Content-Type: application/json" \\
  -d '{ "report": "# FinOps Report..." }'`
      },
      {
        heading: 'Local AI (Ollama)',
        body: 'For air-gapped or cost-sensitive environments, use a local LLM.',
      },
      {
        heading: 'Via UI',
        body: 'In Settings → AI, select provider "Local". Enter the model name (e.g., "llama3") and the Base URL of your OpenAI-compatible endpoint (e.g., "http://ollama.svc:11434/v1"). Enable "Skip SSL Verify" if using self-signed certificates.',
      },
    ]
  },
  {
    id: 'webex',
    title: 'Webex Integration',
    icon: <Bell size={18} />,
    sections: [
      {
        heading: 'Overview',
        body: 'CostDeck can send scaling notifications and respond to commands directly in Cisco Webex rooms. Your team can manage scaling operations without leaving their chat client.',
      },
      {
        heading: 'Setup',
        body: 'To enable Webex, you need a Bot Token and a Room ID.',
      },
      {
        heading: 'Via UI',
        body: 'Go to Settings → Messenger → Webex. Toggle "Enabled". Paste your Bot Token (from developer.webex.com) and the Room ID. Click Save. The bot will immediately introduce itself in the room.',
      },
      {
        heading: 'Via API',
        body: 'Update the Webex settings via the settings endpoint.',
        code: `curl -b cookies.txt -X PUT http://localhost:8082/api/settings \\
  -H "Content-Type: application/json" \\
  -d '{
  "integrations": {
    "messenger": {
      "webex": {
        "enabled": true,
        "roomId": "Y2lz...",
        "botToken": "ZGVh..."
      }
    }
  }
}'`
      },
      {
        heading: 'Bot Commands',
        body: 'Interact with the bot by mentioning it in the Webex room. All commands are case-insensitive.',
        code: `# Scaling
scale group <group-name> up/down
scale config <namespace> up/down

# Status
status group <group-name>
status config <namespace>
status namespaces              # All active vs. scaled-down

# Help
help`
      },
      {
        heading: 'Multi-Cluster',
        body: 'If multiple CostDeck operators share one Webex room, set a unique clusterName. Commands must then be prefixed with the cluster name.',
      },
      {
        heading: 'Via UI',
        body: 'Go to Settings → at the top, set the "Cluster Name" field (e.g., "staging"). Save. Commands in Webex now need the prefix: "staging scale group dev-env down".',
      },
      {
        heading: 'Via Custom Resource',
        body: 'Set the clusterName in the CostDeckConfig spec.',
        code: `# kubectl edit costdeckconfig default -n costdeck
spec:
  clusterName: "staging"`
      },
      {
        heading: 'Scaling Notifications',
        body: 'When a scaling command runs via Webex, the bot sends: 🚀 Initiated... → ⏳ Still scaling (progress every 1 min) → ✅ Successfully scaled. If a newer command overrides the current one, monitoring stops automatically.',
      },
    ]
  },
  {
    id: 'settings',
    title: 'Settings & Config',
    icon: <Server size={18} />,
    sections: [
      {
        heading: 'Overview',
        body: 'CostDeck stores all its configuration in a single CostDeckConfig Custom Resource named "default" in the costdeck namespace. You can manage it from the UI, via the API, or directly through kubectl.',
      },
      {
        heading: 'Via UI',
        body: 'Go to sidebar → Settings. The page has sections for Providers (AWS, Azure, GCP), Integrations (AI, Webex, VictoriaMetrics), and Features (Cloud Pricing API). All changes are saved to the CostDeckConfig CRD and related Secrets automatically.',
      },
      {
        heading: 'Via API',
        body: 'Use GET /api/settings to read and PUT /api/settings to update. Credentials are always masked in GET responses.',
        code: `# Read current settings (credentials are masked)
curl -b cookies.txt http://localhost:8082/api/settings

# Update settings
curl -b cookies.txt -X PUT http://localhost:8082/api/settings \\
  -H "Content-Type: application/json" \\
  -d '{
  "providers": {
    "aws": { "enabled": true, "region": "us-east-1", "accessKey": "AKIA..." }
  },
  "features": { "cloudPricingApi": true }
}'`
      },
      {
        heading: 'Via Custom Resource',
        body: 'Edit the CostDeckConfig directly with kubectl. Note: API keys and tokens should be stored in Kubernetes Secrets referenced by secretRef fields, not in the CRD itself.',
        code: `kubectl edit costdeckconfig default -n costdeck

# Or view the current config
kubectl get cdc default -n costdeck -o yaml`
      },
    ]
  },
  {
    id: 'auth',
    title: 'Authentication',
    icon: <Shield size={18} />,
    sections: [
      {
        heading: 'How Auth Works',
        body: 'CostDeck uses session-cookie authentication. When you log in, the server issues an HttpOnly cookie (costdeck-session) valid for 24 hours. All API endpoints (except /api/login and Swagger) require this cookie.',
      },
      {
        heading: 'Via UI',
        body: 'When auth is enabled, you\'ll see a login page. Enter your username (costdeck-admin) and password. The session lasts 24 hours. To log out, click the Logout button in the sidebar.',
      },
      {
        heading: 'Via API',
        body: 'Use POST /api/login to authenticate and save the cookie for subsequent requests.',
        code: `# Login and save session cookie
curl -X POST http://localhost:8082/api/login \\
  -H "Content-Type: application/json" \\
  -d '{"username":"costdeck-admin","password":"<your-password>"}' \\
  -c cookies.txt

# Use the cookie for authenticated requests
curl -b cookies.txt http://localhost:8082/api/version

# Logout
curl -b cookies.txt -X POST http://localhost:8082/api/logout`
      },
      {
        heading: 'Session Security',
        body: 'Sessions use HMAC-SHA256 signed tokens. Cookies are HttpOnly (not accessible via JavaScript), Secure (HTTPS only), SameSite=Strict (prevents CSRF), and expire after 24 hours.',
      },
      {
        heading: 'Public Endpoints',
        body: 'These endpoints do NOT require authentication: POST /api/login, GET /api/docs (Swagger UI), GET /api/openapi.yaml (OpenAPI spec), POST /api/webex/webhook, and all static assets (the React UI itself).',
      },
    ]
  },
  {
    id: 'mcp',
    title: 'MCP Integration',
    icon: <Plug size={18} />,
    sections: [
      {
        heading: 'What is MCP?',
        body: 'CostDeck functions as a Model Context Protocol (MCP) Server. This allows external AI clients (like Claude Desktop or Cursor) to securely connect to CostDeck and natively invoke cluster operations such as checking resource usage, scaling up workloads, or applying AI-driven cost optimizations without you having to run CLI commands or API calls manually.',
      },
      {
        heading: 'Via UI',
        body: 'Navigate to Settings and select the MCP tab. You can enable or disable the MCP server, configure its port (default 8083), and copy the pre-formatted JSON snippet to paste directly into your Claude Desktop or Cursor configuration files.',
        code: `{
  "mcpServers": {
    "costdeck": {
      "command": "curl",
      "args": ["-N", "http://127.0.0.1:8083/sse"]
    }
  }
}`
      },
      {
        heading: 'Via API',
        body: 'The MCP Server exposes a Server-Sent Events (SSE) endpoint directly on the configured port. This is a standard MCP HTTP transport endpoint. By default, clients connect to http://127.0.0.1:8083/sse to receive updates and execute tools.',
        code: `# Connect to the SSE stream
curl -N http://127.0.0.1:8083/sse

# The server will send a message with the /messages endpoint URL
# You can then POST to the /messages endpoint to execute tools`
      },
      {
        heading: 'Via CRs',
        body: 'The MCP Server port and enabled state are controlled by the CostDeckConfig Custom Resource. You can edit the `mcpConfig` block to set the `enabled` flag and `port` parameter.',
        code: `apiVersion: finops.costdeck.io/v1
kind: CostDeckConfig
metadata:
  name: default
spec:
  mcpConfig:
    enabled: true
    port: 8083`
      },
      {
        heading: 'Available Tools',
        body: 'The CostDeck MCP Server exposes tools like `get_namespace_status`, `scale_group`, `scale_config`, and `optimize_namespace` to automatically read data and perform scaling operations.',
        code: `[
  { "name": "get_namespace_status" },
  { "name": "scale_group" },
  { "name": "scale_config" },
  { "name": "optimize_namespace" }
]`
      },
    ]
  },
]

// ─── API Reference Data ──────────────────────────────────────────────────────

interface Endpoint {
  method: string;
  path: string;
  description: string;
  auth: boolean;
  requestBody?: string;
  responseExample?: string;
}

const apiGroups: { section: string; items: Endpoint[] }[] = [
  {
    section: 'Authentication',
    items: [
      {
        method: 'POST', path: '/api/login', description: 'Authenticate and obtain a session cookie',
        auth: false,
        requestBody: '{\n  "username": "costdeck-admin",\n  "password": "<from-secret>"\n}',
        responseExample: '{ "status": "ok" }\n\n// Sets HttpOnly cookie: costdeck-session (24h TTL)'
      },
      {
        method: 'POST', path: '/api/logout', description: 'Clear session cookie and log out',
        auth: true,
        responseExample: '{ "status": "ok" }'
      },
    ]
  },
  {
    section: 'System & Settings',
    items: [
      {
        method: 'GET', path: '/api/version', description: 'Operator build version', auth: true,
        responseExample: '{ "version": "1.0.0" }'
      },
      {
        method: 'GET', path: '/api/settings', description: 'Get CostDeck configuration (credentials masked)', auth: true,
        responseExample: '{\n  "providers": { "aws": { "enabled": true, "region": "us-east-1", "hasCredentials": true } },\n  "integrations": { "ai": { "enabled": true, "provider": "openai" } },\n  "features": { "cloudPricingApi": false }\n}'
      },
      {
        method: 'PUT', path: '/api/settings', description: 'Update CostDeck configuration', auth: true,
        requestBody: '{\n  "integrations": {\n    "ai": { "enabled": true, "provider": "openai", "apiKey": "sk-..." }\n  }\n}'
      },
      { method: 'GET', path: '/api/openapi.yaml', description: 'OpenAPI V3 specification (YAML)', auth: false },
      { method: 'GET', path: '/api/docs', description: 'Swagger UI interactive documentation', auth: false },
    ]
  },
  {
    section: 'Cluster Metrics',
    items: [
      {
        method: 'GET', path: '/api/cluster-info', description: 'Cluster summary (nodes, total CPU, total memory)', auth: true,
        responseExample: '{\n  "nodes": 3,\n  "totalCPU": "8",\n  "totalMemory": "32Gi"\n}'
      },
      {
        method: 'GET', path: '/api/cluster/nodes', description: 'Per-node resource metrics for the cluster heatmap', auth: true,
        responseExample: '[\n  {\n    "name": "node-1",\n    "cpuCapacity": "4",\n    "cpuUsage": "1.2",\n    "memoryCapacity": "16Gi",\n    "memoryUsage": "8.5Gi",\n    "pods": 24\n  }\n]'
      },
      {
        method: 'POST', path: '/api/costing', description: 'Calculate estimated hourly and monthly costs for a target', auth: true,
        requestBody: '{\n  "targetType": "namespace",\n  "targetName": "frontend",\n  "totalCpu": 2,\n  "totalMemoryGb": 4\n}',
        responseExample: '{\n  "hourlyCost": 0.096,\n  "monthlyCost": 70.08,\n  "currency": "USD",\n  "determinedBy": "Heuristic Math Pricing (aws)"\n}'
      },
    ]
  },
  {
    section: 'Operator Health',
    items: [
      {
        method: 'GET', path: '/api/operator/health', description: 'Runtime metrics, resource usage, and history', auth: true,
        responseExample: '{\n  "current": {\n    "status": "healthy",\n    "goroutines": 134,\n    "cpuUsage": 0.007,\n    "memoryUsage": 19.0,\n    "managedNamespaces": 4\n  },\n  "history": [...]\n}'
      },
      { method: 'GET', path: '/api/operator/logs', description: 'Last 100 lines of operator logs (plain text)', auth: true },
      { method: 'GET', path: '/api/operator/logs/download', description: 'Download full log file as attachment', auth: true },
    ]
  },
  {
    section: 'Namespace Insights',
    items: [
      {
        method: 'GET', path: '/api/namespaces', description: 'List all monitored NamespaceFinOps CRDs', auth: true,
        responseExample: '[\n  {\n    "metadata": { "name": "default" },\n    "spec": { "targetNamespace": "default" },\n    "status": { "insights": ["Overprovisioned"] }\n  }\n]'
      },
      { method: 'GET', path: '/api/namespaces/{ns}/history', description: 'Resource usage history (last 60 minutes)', auth: true },
      { method: 'GET', path: '/api/namespaces/{ns}/pods', description: 'Pod-level resource metrics', auth: true },
      { method: 'GET', path: '/api/namespaces/{ns}/workloads', description: 'List Deployments and StatefulSets', auth: true },
      {
        method: 'PUT', path: '/api/namespaces/{ns}/workloads/{name}', description: 'Manually scale a specific workload', auth: true,
        requestBody: '{ "kind": "Deployment", "replicas": 3 }'
      },
    ]
  },
  {
    section: 'Optimization',
    items: [
      { method: 'POST', path: '/api/namespaces/{ns}/optimize', description: 'Right-size workload resources based on actual usage', auth: true },
      { method: 'POST', path: '/api/namespaces/{ns}/revert', description: 'Revert to original resource values', auth: true },
      {
        method: 'GET', path: '/api/namespaces/{ns}/optimization', description: 'Get current optimization status and original values', auth: true,
        responseExample: '{\n  "active": true,\n  "optimizedAt": "2026-06-01T10:00:00Z",\n  "workloads": [\n    {\n      "name": "nginx",\n      "kind": "Deployment",\n      "original": { "cpuRequest": "100m", "memoryRequest": "128Mi" },\n      "optimized": { "cpuRequest": "50m", "memoryRequest": "64Mi" }\n    }\n  ]\n}'
      },
    ]
  },
  {
    section: 'Scaling Management',
    items: [
      { method: 'GET', path: '/api/scaling/groups', description: 'List all scaling groups', auth: true },
      {
        method: 'POST', path: '/api/scaling/groups', description: 'Create a new scaling group', auth: true,
        requestBody: '{\n  "metadata": { "name": "production" },\n  "spec": {\n    "category": "Solution",\n    "namespaces": ["frontend", "backend"],\n    "active": true,\n    "schedules": [{\n      "days": [1,2,3,4,5],\n      "startTime": "08:00",\n      "endTime": "20:00"\n    }]\n  }\n}'
      },
      { method: 'GET', path: '/api/scaling/groups/{name}', description: 'Get a specific group', auth: true },
      { method: 'PUT', path: '/api/scaling/groups/{name}', description: 'Update a group', auth: true },
      { method: 'DELETE', path: '/api/scaling/groups/{name}', description: 'Delete a group', auth: true },
      {
        method: 'POST', path: '/api/scaling/groups/{name}/manual', description: 'Manual override: activate or deactivate', auth: true,
        requestBody: '{ "active": true }'
      },
      { method: 'GET', path: '/api/scaling/configs', description: 'List all scaling configs', auth: true },
      { method: 'POST', path: '/api/scaling/configs', description: 'Create a new scaling config', auth: true },
      { method: 'GET', path: '/api/scaling/configs/{name}', description: 'Get a specific config', auth: true },
      { method: 'PUT', path: '/api/scaling/configs/{name}', description: 'Update a config', auth: true },
      { method: 'DELETE', path: '/api/scaling/configs/{name}', description: 'Delete a config', auth: true },
      { method: 'POST', path: '/api/scaling/configs/{name}/manual', description: 'Manual override', auth: true },
    ]
  },
  {
    section: 'AI & Reporting',
    items: [
      {
        method: 'POST', path: '/api/ai/chat', description: 'Stream AI assistant response (SSE)', auth: true,
        requestBody: '{\n  "prompt": "Analyze cluster cost efficiency",\n  "messages": [\n    { "role": "user", "content": "Which namespaces waste money?" }\n  ]\n}'
      },
      { method: 'GET', path: '/api/ai/report', description: 'Get the last saved FinOps report', auth: true },
      { method: 'POST', path: '/api/ai/report/generate', description: 'Generate a new FinOps report (streaming)', auth: true },
      {
        method: 'PUT', path: '/api/ai/report/save', description: 'Save generated report to ConfigMap for persistence', auth: true,
        requestBody: '{ "report": "# FinOps Report\\n..." }'
      },
    ]
  },
  {
    section: 'Webex & Discovery',
    items: [
      { method: 'POST', path: '/api/webex/webhook', description: 'Receive incoming Webex messages (bot commands)', auth: false },
      { method: 'GET', path: '/api/discovery/{provider}', description: 'Discover external resources (e.g., AWS Aurora, EC2)', auth: true },
      { method: 'GET', path: '/api/settings/providers/{provider}/status', description: 'Check provider connectivity status', auth: true },
    ]
  },
]

// ─── UI Components ───────────────────────────────────────────────────────────

const methodColors: Record<string, string> = {
  GET: 'bg-emerald-500/10 text-emerald-600 border-emerald-200',
  POST: 'bg-blue-500/10 text-blue-600 border-blue-200',
  PUT: 'bg-amber-500/10 text-amber-600 border-amber-200',
  DELETE: 'bg-red-500/10 text-red-600 border-red-200',
}

function CodeBlock({ code, label }: { code: string, label: string }) {
  const [copied, setCopied] = useState(false)
  const copy = () => {
    navigator.clipboard.writeText(code)
    setCopied(true)
    setTimeout(() => setCopied(false), 1500)
  }
  return (
    <div className="mt-3">
      <div className="flex items-center justify-between text-[10px] text-slate-400 uppercase tracking-wider font-bold mb-1">
        <span>{label}</span>
        <button onClick={copy} className="flex items-center gap-1 hover:text-slate-600 transition-colors">
          {copied ? <Check size={10} /> : <Copy size={10} />}
          {copied ? 'Copied' : 'Copy'}
        </button>
      </div>
      <pre className="bg-slate-900 text-slate-300 rounded-lg p-3 text-xs font-mono overflow-x-auto whitespace-pre leading-relaxed">
        {code}
      </pre>
    </div>
  )
}

// ─── Main Component ──────────────────────────────────────────────────────────

export default function ApiReference() {
  const [activeTab, setActiveTab] = useState<string>('intro')
  const [expandedApi, setExpandedApi] = useState<string | null>(null)

  const isApiMode = activeTab === 'api-docs'
  const currentGuide = guides.find(g => g.id === activeTab)

  return (
    <div className="flex h-full w-full bg-white">
      {/* Documentation Sidebar */}
      <div className="w-64 min-w-[256px] border-r border-slate-200 bg-slate-50/50 flex flex-col h-full overflow-y-auto">
        <div className="p-6 pb-2">
          <h2 className="text-lg font-black tracking-tight text-slate-900 flex items-center gap-2">
            <BookOpen className="text-blue-500" size={20} />
            Documentation
          </h2>
          <p className="text-[11px] text-slate-400 mt-1">Everything you need to know about CostDeck</p>
        </div>

        <div className="px-4 py-4 space-y-6">
          <div>
            <h3 className="text-[10px] font-black uppercase tracking-widest text-slate-400 mb-2 px-2">Guides</h3>
            <div className="space-y-0.5">
              {guides.map(g => (
                <button
                  key={g.id}
                  onClick={() => setActiveTab(g.id)}
                  className={`w-full flex items-center gap-3 px-3 py-2 rounded-lg transition-colors text-[13px] font-semibold ${activeTab === g.id
                      ? 'bg-blue-50 text-blue-600'
                      : 'text-slate-600 hover:bg-slate-100 hover:text-slate-900'
                    }`}
                >
                  <span className={activeTab === g.id ? 'text-blue-500' : 'text-slate-400'}>{g.icon}</span>
                  {g.title}
                </button>
              ))}
            </div>
          </div>

          <div>
            <h3 className="text-[10px] font-black uppercase tracking-widest text-slate-400 mb-2 px-2">Reference</h3>
            <div className="space-y-0.5">
              <button
                onClick={() => setActiveTab('api-docs')}
                className={`w-full flex items-center gap-3 px-3 py-2 rounded-lg transition-colors text-[13px] font-semibold ${activeTab === 'api-docs'
                    ? 'bg-blue-50 text-blue-600'
                    : 'text-slate-600 hover:bg-slate-100 hover:text-slate-900'
                  }`}
              >
                <Code2 size={18} className={activeTab === 'api-docs' ? 'text-blue-500' : 'text-slate-400'} />
                REST API
              </button>
            </div>
          </div>
        </div>
      </div>

      {/* Main Content */}
      <div className="flex-1 h-full overflow-y-auto p-10 bg-white">
        <div className="max-w-3xl mx-auto animate-in fade-in duration-300">

          {/* Guide Rendering */}
          {!isApiMode && currentGuide && (
            <div>
              <div className="flex items-center gap-3 mb-8">
                <div className="w-10 h-10 bg-blue-50 border border-blue-100 rounded-xl flex items-center justify-center text-blue-500">
                  {currentGuide.icon}
                </div>
                <div>
                  <h1 className="text-2xl font-black tracking-tight text-slate-900">{currentGuide.title}</h1>
                </div>
              </div>

              <div className="space-y-8">
                {currentGuide.sections.map((section, i) => (
                  <div key={i}>
                    <h2 className="text-lg font-bold text-slate-800 mb-2">{section.heading}</h2>
                    <p className="text-[14px] text-slate-600 leading-relaxed">{section.body}</p>
                    {section.code && <CodeBlock code={section.code} label="Example" />}
                  </div>
                ))}
              </div>

              {/* Navigation */}
              <div className="mt-12 pt-6 border-t border-slate-100 flex justify-between">
                {(() => {
                  const allItems = [...guides.map(g => g.id), 'api-docs']
                  const currentIdx = allItems.indexOf(activeTab)
                  const prevItem = currentIdx > 0 ? allItems[currentIdx - 1] : null
                  const nextItem = currentIdx < allItems.length - 1 ? allItems[currentIdx + 1] : null
                  const getName = (id: string) => id === 'api-docs' ? 'REST API' : guides.find(g => g.id === id)?.title || ''
                  return (
                    <>
                      {prevItem ? (
                        <button onClick={() => setActiveTab(prevItem)} className="text-sm text-blue-600 font-semibold hover:text-blue-700">
                          ← {getName(prevItem)}
                        </button>
                      ) : <div />}
                      {nextItem ? (
                        <button onClick={() => setActiveTab(nextItem)} className="text-sm text-blue-600 font-semibold hover:text-blue-700">
                          {getName(nextItem)} →
                        </button>
                      ) : <div />}
                    </>
                  )
                })()}
              </div>
            </div>
          )}

          {/* API Reference Rendering */}
          {isApiMode && (
            <div>
              <div className="mb-8">
                <div className="flex items-center gap-3 mb-4">
                  <div className="w-10 h-10 bg-blue-50 border border-blue-100 rounded-xl flex items-center justify-center text-blue-500">
                    <Code2 size={18} />
                  </div>
                  <h1 className="text-2xl font-black tracking-tight text-slate-900">REST API Reference</h1>
                </div>
                <p className="text-slate-500 text-sm mb-4">Interactive documentation for all CostDeck API endpoints.</p>
                <div className="p-4 bg-slate-50 border border-slate-200 rounded-xl text-sm text-slate-600">
                  <strong className="text-slate-900">Authentication:</strong> All endpoints (except <code className="bg-slate-200 px-1.5 py-0.5 text-emerald-700 rounded font-mono text-xs">/api/login</code>) require a valid <code className="bg-slate-200 px-1.5 py-0.5 text-emerald-700 rounded font-mono text-xs">costdeck-session</code> cookie.
                  <code className="block mt-2 bg-slate-900 p-2 text-emerald-400 rounded-lg font-mono text-xs">
                    kubectl get secret costdeck-operator-admin-credentials -n costdeck -o jsonpath='&#123;.data.password&#125;' | base64 -d
                  </code>
                </div>
              </div>

              {apiGroups.map(group => (
                <div key={group.section} className="mb-10">
                  <h3 className="text-sm font-black uppercase tracking-widest text-slate-400 mb-4 px-1">{group.section}</h3>
                  <div className="space-y-3">
                    {group.items.map(ep => {
                      const key = `${ep.method}:${ep.path}`
                      const isOpen = expandedApi === key
                      return (
                        <div key={key} className="bg-white rounded-xl border border-slate-200 shadow-sm overflow-hidden transition-all hover:border-slate-300">
                          <button
                            onClick={() => setExpandedApi(isOpen ? null : key)}
                            className="w-full flex items-center gap-4 p-4 text-left bg-slate-50/50 hover:bg-slate-50 transition-colors"
                          >
                            <span className={`w-16 text-center px-2 py-1 rounded-md text-xs font-black border ${methodColors[ep.method] || 'bg-slate-100 text-slate-600'}`}>
                              {ep.method}
                            </span>
                            <code className="text-sm font-mono text-slate-700 flex-1">{ep.path}</code>
                            {ep.auth && <Lock size={14} className="text-slate-300 flex-shrink-0" />}
                            <ChevronRight size={16} className={`text-slate-400 transition-transform ${isOpen ? 'rotate-90' : ''}`} />
                          </button>

                          {isOpen && (
                            <div className="px-5 pb-5 border-t border-slate-100 bg-white">
                              <p className="text-sm text-slate-600 mt-4 mb-2">{ep.description}</p>
                              {ep.requestBody && <CodeBlock code={ep.requestBody} label="Request Body" />}
                              {ep.responseExample && <CodeBlock code={ep.responseExample} label="Response" />}
                              {!ep.requestBody && !ep.responseExample && (
                                <p className="text-xs text-slate-400 mt-4 italic bg-slate-50 p-2 rounded-lg border border-slate-100">Returns status code only (200 OK / 204 No Content)</p>
                              )}
                            </div>
                          )}
                        </div>
                      )
                    })}
                  </div>
                </div>
              ))}

              {/* Back navigation */}
              <div className="mt-12 pt-6 border-t border-slate-100">
                <button onClick={() => setActiveTab('auth')} className="text-sm text-blue-600 font-semibold hover:text-blue-700">
                  ← Authentication
                </button>
              </div>
            </div>
          )}
        </div>
      </div>
    </div>
  )
}
