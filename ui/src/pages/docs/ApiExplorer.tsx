import { useEffect, useMemo, useState } from 'react'
import { ChevronRight, ExternalLink, FileCode2, Loader2, Search } from 'lucide-react'
import { Badge, Tabs, type Tone } from '../../components/ui'
import { apiSetup } from './content'
import { CodeBlock, Inline } from './blocks'

// The reference is rendered from the same OpenAPI document as Swagger UI, and a Go test
// keeps that document in step with the server's routes, so this page cannot drift.

interface Schema {
  $ref?: string
  type?: string
  format?: string
  example?: unknown
  default?: unknown
  enum?: unknown[]
  properties?: Record<string, Schema>
  items?: Schema
  description?: string
}
interface MediaType {
  schema?: Schema
  example?: unknown
  examples?: Record<string, { summary?: string; value: unknown }>
}
interface Parameter {
  $ref?: string
  name: string
  in: string
  required?: boolean
  description?: string
  schema?: Schema
}
interface Operation {
  summary?: string
  description?: string
  tags?: string[]
  deprecated?: boolean
  'x-role'?: string
  security?: unknown[]
  parameters?: Parameter[]
  requestBody?: { content?: Record<string, MediaType> }
  responses?: Record<string, { description?: string; content?: Record<string, MediaType> }>
}
interface Spec {
  info?: { version?: string }
  tags?: { name: string; description?: string }[]
  paths: Record<string, Record<string, unknown>>
  components?: { schemas?: Record<string, Schema>; parameters?: Record<string, Parameter> }
}

interface Endpoint {
  method: string
  path: string
  op: Operation
  params: Parameter[]
}

const METHODS = ['get', 'post', 'put', 'patch', 'delete']

const methodStyle: Record<string, string> = {
  GET: 'text-brand-700 bg-brand-50 border-brand-200',
  POST: 'text-sky-700 bg-sky-50 border-sky-200',
  PUT: 'text-amber-800 bg-amber-50 border-amber-200',
  PATCH: 'text-amber-800 bg-amber-50 border-amber-200',
  DELETE: 'text-rose-700 bg-rose-50 border-rose-200',
}

const roleTone: Record<string, Tone> = { public: 'success', viewer: 'neutral', operator: 'info', admin: 'warning' }

function resolve<T>(spec: Spec, ref: string): T | undefined {
  // Only local references (#/components/...) occur in the spec.
  let node: unknown = spec
  for (const part of ref.replace(/^#\//, '').split('/')) node = (node as Record<string, unknown> | undefined)?.[part]
  return node as T | undefined
}

// sample builds an example value from a schema, preferring the examples the spec gives.
function sample(spec: Spec, schema: Schema | undefined, depth = 0): unknown {
  if (!schema || depth > 6) return undefined
  if (schema.$ref) return sample(spec, resolve<Schema>(spec, schema.$ref), depth + 1)
  if (schema.example !== undefined) return schema.example
  if (schema.default !== undefined) return schema.default
  if (schema.enum?.length) return schema.enum[0]
  if (schema.type === 'object' || schema.properties) {
    const out: Record<string, unknown> = {}
    for (const [k, v] of Object.entries(schema.properties || {})) {
      const value = sample(spec, v, depth + 1)
      if (value !== undefined) out[k] = value
    }
    return out
  }
  if (schema.type === 'array') {
    const item = sample(spec, schema.items, depth + 1)
    return item === undefined ? [] : [item]
  }
  if (schema.type === 'string') return schema.format === 'date-time' ? '2026-10-05T18:00:00Z' : 'string'
  if (schema.type === 'integer' || schema.type === 'number') return 0
  if (schema.type === 'boolean') return true
  return undefined
}

// bodyExamples lists the request bodies worth showing: the named examples, or one built
// from the schema.
function bodyExamples(spec: Spec, op: Operation): { label: string; value: unknown }[] {
  const media = op.requestBody?.content?.['application/json']
  if (!media) return []
  if (media.examples) return Object.values(media.examples).map(e => ({ label: e.summary || 'Example', value: e.value }))
  const value = media.example ?? sample(spec, media.schema)
  return value === undefined ? [] : [{ label: 'Request body', value }]
}

function responseExample(spec: Spec, op: Operation): unknown {
  for (const code of ['200', '201']) {
    const media = op.responses?.[code]?.content?.['application/json']
    if (media) return media.example ?? sample(spec, media.schema)
  }
  return undefined
}

// Sensible values for path parameters, so the curl line runs as printed.
function pathValue(path: string, key: string): string {
  if (path.startsWith('/api/tokens')) return 'ci-pipeline'
  if (path.startsWith('/api/ai/tools')) return 'scale_group'
  if (path.startsWith('/api/settings/providers')) return 'victoriametrics'
  return ({ ns: 'dev-backend', name: 'dev', provider: 'aws', type: 'ec2' } as Record<string, string>)[key] || key
}

function curlFor(ep: Endpoint, body: unknown): string {
  const url = ep.path.replace(/\{(\w+)\}/g, (_, k: string) => pathValue(ep.path, k))
  const stream = Object.values(ep.op.responses || {}).some(r => r.content?.['text/event-stream'])
  const method = ep.method.toUpperCase()
  const lines = [`curl${method === 'GET' ? '' : ` -X ${method}`}${stream ? ' -N' : ''} "$COSTDECK${url}"`]
  if (ep.op['x-role']) lines.push('  -H "Authorization: Bearer $TOKEN"')
  if (body !== undefined) {
    lines.push('  -H "Content-Type: application/json"')
    lines.push(`  -d '${JSON.stringify(body).replace(/'/g, "'\\''")}'`)
  }
  return lines.join(' \\\n')
}

function EndpointDetails({ spec, ep }: { spec: Spec; ep: Endpoint }) {
  const bodies = bodyExamples(spec, ep.op)
  const [bodyIdx, setBodyIdx] = useState('0')
  const body = bodies[Number(bodyIdx)]
  const response = responseExample(spec, ep.op)
  return (
    <div className="px-4 pb-4 pt-3 border-t border-slate-100 space-y-3">
      {ep.op.description && <p className="text-sm text-slate-600 leading-relaxed"><Inline text={ep.op.description} /></p>}
      {ep.params.length > 0 && (
        <div className="overflow-x-auto">
          <table className="w-full text-sm">
            <thead><tr className="text-left text-[11px] uppercase tracking-wider text-slate-400"><th className="py-1 pr-3 font-semibold">Parameter</th><th className="py-1 pr-3 font-semibold">In</th><th className="py-1 font-semibold">Description</th></tr></thead>
            <tbody>
              {ep.params.map(p => (
                <tr key={p.in + p.name} className="border-t border-slate-100 align-top">
                  <td className="py-1.5 pr-3 font-mono text-xs text-slate-800 whitespace-nowrap">{p.name}{p.required && <span className="text-rose-500">*</span>}</td>
                  <td className="py-1.5 pr-3 text-xs text-slate-500">{p.in}</td>
                  <td className="py-1.5 text-xs text-slate-600">{p.description || ''}{p.schema?.enum && <span className="text-slate-400"> One of {p.schema.enum.join(', ')}.</span>}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {bodies.length > 1 && (
        <Tabs value={bodyIdx} onChange={setBodyIdx} tabs={bodies.map((b, i) => ({ id: String(i), label: b.label }))} />
      )}
      <CodeBlock label="curl" code={curlFor(ep, body?.value)} />
      {body && <CodeBlock label={bodies.length > 1 ? 'Body' : body.label} code={JSON.stringify(body.value, null, 2)} />}
      {response !== undefined && <CodeBlock label="Response" code={JSON.stringify(response, null, 2)} />}
    </div>
  )
}

export default function ApiExplorer() {
  const [spec, setSpec] = useState<Spec | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [query, setQuery] = useState('')
  const [open, setOpen] = useState<string | null>(null)

  useEffect(() => {
    let cancelled = false
    fetch('/api/openapi.json')
      .then(r => (r.ok ? r.json() : Promise.reject(new Error(`HTTP ${r.status}`))))
      .then((s: Spec) => { if (!cancelled) setSpec(s) })
      .catch((e: Error) => { if (!cancelled) setError(e.message) })
    return () => { cancelled = true }
  }, [])

  const groups = useMemo(() => {
    if (!spec) return []
    const endpoints: Endpoint[] = []
    for (const [path, item] of Object.entries(spec.paths)) {
      const shared = (item.parameters as Parameter[] | undefined) || []
      for (const method of METHODS) {
        const op = item[method] as Operation | undefined
        if (!op) continue
        const params = [...shared, ...(op.parameters || [])].map(p => (p.$ref ? resolve<Parameter>(spec, p.$ref) : p)).filter((p): p is Parameter => !!p)
        endpoints.push({ method, path, op, params })
      }
    }
    const q = query.trim().toLowerCase()
    const shown = q ? endpoints.filter(e => `${e.method} ${e.path} ${e.op.summary || ''} ${(e.op.tags || []).join(' ')}`.toLowerCase().includes(q)) : endpoints
    const order = (spec.tags || []).map(t => t.name)
    const byTag = new Map<string, Endpoint[]>()
    for (const e of shown) {
      const tag = e.op.tags?.[0] || 'Other'
      byTag.set(tag, [...(byTag.get(tag) || []), e])
    }
    return [...byTag.entries()]
      .sort(([a], [b]) => (order.indexOf(a) + 1 || 99) - (order.indexOf(b) + 1 || 99))
      .map(([tag, items]) => ({
        tag,
        description: spec.tags?.find(t => t.name === tag)?.description,
        // JSON objects lose the document's order, so sort by path, then GET before writes.
        items: items.sort((a, b) => a.path.localeCompare(b.path) || METHODS.indexOf(a.method) - METHODS.indexOf(b.method)),
      }))
  }, [spec, query])

  return (
    <div>
      <div className="flex items-center gap-3 mb-2">
        <div className="w-10 h-10 bg-brand-50 border border-brand-100 rounded-xl flex items-center justify-center text-brand-600"><FileCode2 size={18} /></div>
        <div>
          <h1 className="text-2xl font-bold tracking-tight text-slate-900">REST API</h1>
          {spec?.info?.version && <p className="text-xs text-slate-400">Version {spec.info.version}</p>}
        </div>
      </div>
      <p className="text-[15px] text-slate-600 leading-relaxed mt-4">
        Everything the dashboard does goes through this API. Authenticate with an API token from Settings → Access &amp; SSO; each endpoint shows the
        role it needs. Set these once and every example below runs as printed:
      </p>
      <CodeBlock code={apiSetup} />
      <div className="flex flex-wrap gap-4 mt-3 text-sm">
        <a href="/api/docs" target="_blank" rel="noreferrer" className="inline-flex items-center gap-1 font-semibold text-brand-700 hover:text-brand-800">Try requests in Swagger UI <ExternalLink size={13} /></a>
        <a href="/api/openapi.yaml" target="_blank" rel="noreferrer" className="inline-flex items-center gap-1 font-semibold text-brand-700 hover:text-brand-800">OpenAPI document <ExternalLink size={13} /></a>
      </div>

      <div className="relative mt-8 mb-6">
        <Search size={15} className="absolute left-3 top-1/2 -translate-y-1/2 text-slate-400" />
        <input value={query} onChange={e => setQuery(e.target.value)} placeholder="Filter endpoints, e.g. schedule, tokens, POST"
          className="w-full pl-9 pr-3 py-2 text-sm bg-white border border-slate-200 rounded-lg outline-none focus:border-brand-500" />
      </div>

      {!spec && !error && <div className="flex items-center gap-2 text-sm text-slate-400"><Loader2 size={15} className="animate-spin" /> Loading the API description…</div>}
      {error && <p className="text-sm text-rose-600">Could not load /api/openapi.json ({error}).</p>}
      {spec && groups.length === 0 && <p className="text-sm text-slate-400">No endpoint matches “{query}”.</p>}

      {groups.map(g => (
        <section key={g.tag} className="mb-8">
          <h2 className="text-base font-bold text-slate-800">{g.tag}</h2>
          {g.description && <p className="text-sm text-slate-500 mb-3">{g.description}</p>}
          <div className="space-y-2">
            {g.items.map(ep => {
              const key = `${ep.method} ${ep.path}`
              const role = ep.op['x-role'] || 'public'
              const isOpen = open === key
              return (
                <div key={key} className="rounded-xl border border-slate-200 bg-white overflow-hidden">
                  <button onClick={() => setOpen(isOpen ? null : key)} className="w-full flex items-center gap-3 px-4 py-2.5 text-left hover:bg-slate-50">
                    <span className={`w-16 shrink-0 text-center py-0.5 rounded-md border text-[11px] font-bold ${methodStyle[ep.method.toUpperCase()]}`}>{ep.method.toUpperCase()}</span>
                    <code className={`text-[13px] font-mono shrink-0 ${ep.op.deprecated ? 'text-slate-400 line-through' : 'text-slate-800'}`}>{ep.path}</code>
                    <span className="text-sm text-slate-500 truncate flex-1">{ep.op.summary}</span>
                    {ep.op.deprecated && <Badge tone="neutral">Deprecated</Badge>}
                    <Badge tone={roleTone[role] || 'neutral'}>{role === 'public' ? 'Public' : role}</Badge>
                    <ChevronRight size={15} className={`shrink-0 text-slate-400 transition-transform ${isOpen ? 'rotate-90' : ''}`} />
                  </button>
                  {isOpen && spec && <EndpointDetails spec={spec} ep={ep} />}
                </div>
              )
            })}
          </div>
        </section>
      ))}
    </div>
  )
}
