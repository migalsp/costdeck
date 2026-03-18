import { useState } from 'react'
import { BookOpen, ChevronRight, Copy, Check, Lock } from 'lucide-react'

interface Endpoint {
  method: string;
  path: string;
  description: string;
  auth: boolean;
  requestBody?: string;
  responseExample?: string;
}

const endpoints: { section: string; items: Endpoint[] }[] = [
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
    section: 'System',
    items: [
      { method: 'GET', path: '/api/version', description: 'Operator build version', auth: true,
        responseExample: '{ "version": "v1.0.0" }' },
      { method: 'GET', path: '/api/cluster-info', description: 'Cluster summary (nodes, CPU, memory)', auth: true,
        responseExample: '{\n  "nodes": 1,\n  "totalCPU": "2",\n  "totalMemory": "4Gi"\n}' },
      { method: 'GET', path: '/api/cluster/nodes', description: 'Per-node resource metrics for heatmap', auth: true,
        responseExample: '[\n  {\n    "name": "minikube",\n    "cpuCapacity": "2",\n    "cpuUsage": "0.5",\n    "memoryCapacity": "4Gi",\n    "memoryUsage": "1.2Gi",\n    "pods": 12\n  }\n]' },
    ]
  },
  {
    section: 'Operator Health',
    items: [
      { method: 'GET', path: '/api/operator/health', description: 'Runtime metrics and resource usage history', auth: true,
        responseExample: '{\n  "current": {\n    "status": "healthy",\n    "goroutines": 134,\n    "cpuUsage": 0.007,\n    "memoryUsage": 19.0,\n    "managedNamespaces": 4\n  },\n  "history": [...]\n}' },
      { method: 'GET', path: '/api/operator/logs', description: 'Trailing 100 lines of operator logs (plain text)', auth: true },
      { method: 'GET', path: '/api/operator/logs/download', description: 'Download full log file', auth: true },
    ]
  },
  {
    section: 'Namespace Insights',
    items: [
      { method: 'GET', path: '/api/namespaces', description: 'List all monitored NamespaceFinOps CRDs', auth: true,
        responseExample: '[\n  {\n    "metadata": { "name": "default" },\n    "spec": { "targetNamespace": "default" },\n    "status": { "insights": ["Overprovisioned"] }\n  }\n]' },
      { method: 'GET', path: '/api/namespaces/{ns}/history', description: 'Resource usage history (last 60 min)', auth: true },
      { method: 'GET', path: '/api/namespaces/{ns}/pods', description: 'Pod-level resource metrics', auth: true },
      { method: 'GET', path: '/api/namespaces/{ns}/workloads', description: 'List Deployments and StatefulSets', auth: true },
      { method: 'PUT', path: '/api/namespaces/{ns}/workloads/{name}', description: 'Scale a specific workload', auth: true,
        requestBody: '{ "kind": "Deployment", "replicas": 3 }' },
    ]
  },
  {
    section: 'Optimization',
    items: [
      { method: 'POST', path: '/api/namespaces/{ns}/optimize', description: 'Right-size workload resources based on usage', auth: true },
      { method: 'POST', path: '/api/namespaces/{ns}/revert', description: 'Revert to original resource values', auth: true },
      { method: 'GET', path: '/api/namespaces/{ns}/optimization', description: 'Current optimization status', auth: true,
        responseExample: '{\n  "active": true,\n  "optimizedAt": "2026-02-26T22:00:00Z",\n  "workloads": [\n    {\n      "name": "nginx",\n      "kind": "Deployment",\n      "original": { "cpuRequest": "100m" },\n      "optimized": { "cpuRequest": "50m" }\n    }\n  ]\n}' },
    ]
  },
  {
    section: 'Scaling Management',
    items: [
      { method: 'GET', path: '/api/scaling/groups', description: 'List all scaling groups', auth: true },
      { method: 'POST', path: '/api/scaling/groups', description: 'Create a new scaling group', auth: true,
        requestBody: '{\n  "metadata": { "name": "production" },\n  "spec": {\n    "category": "Solution",\n    "namespaces": ["frontend", "backend"],\n    "active": true,\n    "schedules": [{\n      "days": [1,2,3,4,5],\n      "startTime": "08:00",\n      "endTime": "20:00"\n    }]\n  }\n}' },
      { method: 'GET', path: '/api/scaling/groups/{name}', description: 'Get a specific group', auth: true },
      { method: 'PUT', path: '/api/scaling/groups/{name}', description: 'Update a group', auth: true },
      { method: 'DELETE', path: '/api/scaling/groups/{name}', description: 'Delete a group', auth: true },
      { method: 'POST', path: '/api/scaling/groups/{name}/manual', description: 'Manual override (activate/deactivate)', auth: true,
        requestBody: '{ "active": true }' },
      { method: 'GET', path: '/api/scaling/configs', description: 'List all scaling configs', auth: true },
      { method: 'POST', path: '/api/scaling/configs', description: 'Create a new scaling config', auth: true },
      { method: 'GET', path: '/api/scaling/configs/{name}', description: 'Get a specific config', auth: true },
      { method: 'PUT', path: '/api/scaling/configs/{name}', description: 'Update a config', auth: true },
      { method: 'DELETE', path: '/api/scaling/configs/{name}', description: 'Delete a config', auth: true },
      { method: 'POST', path: '/api/scaling/configs/{name}/manual', description: 'Manual override', auth: true },
    ]
  },
]

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

export default function ApiReference() {
  const [expanded, setExpanded] = useState<string | null>(null)

  return (
    <div className="p-8 max-w-[1000px] mx-auto animate-in fade-in duration-500">
      <div className="mb-8">
        <h2 className="text-3xl font-bold tracking-tight text-slate-800 flex items-center gap-3">
          <BookOpen className="text-blue-500" size={32} />
          API Reference
        </h2>
        <p className="text-slate-500 mt-1">Interactive documentation for the Cost Deck REST API</p>
        <div className="mt-4 p-3 bg-slate-100 border border-slate-200 rounded-lg text-xs text-slate-500">
          <strong className="text-slate-700">Authentication:</strong> All endpoints (except <code className="bg-slate-200 px-1 rounded">/api/login</code>) require a valid <code className="bg-slate-200 px-1 rounded">costdeck-session</code> cookie. 
          Retrieve your password: <code className="bg-slate-200 px-1 rounded text-[11px]">kubectl get secret costdeck-operator-admin-credentials -n costdeck -o jsonpath='&#123;.data.password&#125;' | base64 -d</code>
        </div>
      </div>

      {endpoints.map(group => (
        <div key={group.section} className="mb-8">
          <h3 className="text-xs font-black uppercase tracking-widest text-slate-400 mb-3 px-1">{group.section}</h3>
          <div className="space-y-2">
            {group.items.map(ep => {
              const key = `${ep.method}:${ep.path}`
              const isOpen = expanded === key
              return (
                <div key={key} className="bg-white rounded-xl border border-slate-200 shadow-sm overflow-hidden transition-all">
                  <button
                    onClick={() => setExpanded(isOpen ? null : key)}
                    className="w-full flex items-center gap-3 p-4 text-left hover:bg-slate-50 transition-colors"
                  >
                    <span className={`px-2.5 py-1 rounded-md text-[11px] font-black border ${methodColors[ep.method] || 'bg-slate-100 text-slate-600'}`}>
                      {ep.method}
                    </span>
                    <code className="text-sm font-mono text-slate-700 flex-1">{ep.path}</code>
                    {ep.auth && <Lock size={12} className="text-slate-300" />}
                    <ChevronRight size={16} className={`text-slate-400 transition-transform ${isOpen ? 'rotate-90' : ''}`} />
                  </button>

                  {isOpen && (
                    <div className="px-4 pb-4 border-t border-slate-100">
                      <p className="text-sm text-slate-600 mt-3">{ep.description}</p>
                      {ep.requestBody && <CodeBlock code={ep.requestBody} label="Request Body" />}
                      {ep.responseExample && <CodeBlock code={ep.responseExample} label="Response" />}
                      {!ep.requestBody && !ep.responseExample && (
                        <p className="text-xs text-slate-400 mt-3 italic">Returns status code only (200 OK / 204 No Content)</p>
                      )}
                    </div>
                  )}
                </div>
              )
            })}
          </div>
        </div>
      ))}
    </div>
  )
}
