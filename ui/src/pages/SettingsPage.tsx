import { useState, useCallback, type ReactNode } from 'react'
import {
  Cloud, Bot, MessageSquare, Plus, Trash2, RefreshCw,
  CheckCircle2, XCircle, AlertTriangle, Eye, EyeOff, ChevronDown,
  ChevronUp, Sparkles, ExternalLink, Activity, Plug, Shield, Info, Users as UsersIcon, KeyRound, Coins, Receipt
} from 'lucide-react'
import { AWSLogo, AzureLogo, GCPLogo, WebexLogo } from '../components/ProviderLogos'
import ApiTokens from '../components/ApiTokens'
import UsersSettings from '../components/settings/UsersSettings'
import { usePolling } from '../lib/usePolling'
import { apiError, errorMessage } from '../lib/api'
import { Button, PageHeader } from '../components/ui'

// ─── Types ──────────────────────────────────────────────────────────────────

interface ProviderStatus {
  connected: boolean
  lastChecked?: string
  error?: string
  message?: string
  discoveredResources?: number
}

interface AWSSettings {
  enabled: boolean
  region: string
  hasCredentials: boolean
  discoveryTags?: Record<string, string>
  resourceTypes?: string[]
  status?: ProviderStatus
}

interface AzureSettings {
  enabled: boolean
  subscriptionId?: string
  tenantId?: string
  discoveryTags?: Record<string, string>
  resourceTypes?: string[]
  hasCredentials: boolean
  status?: ProviderStatus
}

interface GCPSettings {
  enabled: boolean
  projectId?: string
  discoveryLabels?: Record<string, string>
  resourceTypes?: string[]
  hasCredentials: boolean
  status?: ProviderStatus
}

interface AISettings {
  enabled: boolean
  provider?: string
  model?: string
  baseUrl?: string
  skipSslVerify?: boolean
  hasCredentials: boolean
}

interface WebexSettings {
  enabled: boolean
  notifyTransitions?: boolean
  roomId?: string
  hasCredentials: boolean
  status?: ProviderStatus
}

interface VictoriaMetricsSettings {
  enabled: boolean
  endpoint?: string
  labelSelector?: string
  skipSslVerify?: boolean
  retentionDays: number
  hasCredentials: boolean
  status?: ProviderStatus
}

interface MCPSettings {
  enabled: boolean
  path: string
}

interface EntraSettings {
  enabled: boolean
  tenantId?: string
  clientId?: string
  redirectUrl?: string
  authorityHost?: string
  defaultRole?: string
  autoProvision: boolean
  groupRoleMapping?: Record<string, string>
  skipSslVerify: boolean
  hasClientSecret: boolean
}

interface SettingsData {
  providers: {
    aws?: AWSSettings
    azure?: AzureSettings
    gcp?: GCPSettings
  }
  integrations: {
    ai?: AISettings
    messenger?: {
      webex?: WebexSettings
    }
    victoriaMetrics?: VictoriaMetricsSettings
    mcp?: MCPSettings
  }
  features?: {
    cloudPricingApi?: boolean;
  }
  auth?: {
    disableLocalLogin: boolean
    entra?: EntraSettings
  }
  pricing?: {
    cpuCoreHour?: string
    memoryGiBHour?: string
    storageGiBMonth?: string
    loadBalancerMonth?: string
    currency?: string
    effective: { cpuCoreHour: number; memoryGiBHour: number; currency: string; basis: string }
  }
  billing?: { enabled?: boolean }
}

// ─── Navigation ─────────────────────────────────────────────────────────────

type Section = 'users' | 'sso' | 'tokens' | 'pricing' | 'billing' | 'metrics' | 'clouds' | 'notifications' | 'ai' | 'mcp'
// Sections whose form is saved with their Save button; the others act at once.
type SavedSection = 'sso' | 'pricing' | 'metrics' | 'clouds' | 'notifications' | 'ai' | 'mcp'
const savedSections: Section[] = ['sso', 'pricing', 'metrics', 'clouds', 'notifications', 'ai', 'mcp']
const isSaved = (s: Section): s is SavedSection => savedSections.includes(s)

const navGroups: { title: string; items: { id: Section; label: string; icon: ReactNode }[] }[] = [
  { title: 'People & access', items: [
    { id: 'users', label: 'Users', icon: <UsersIcon size={16} /> },
    { id: 'sso', label: 'Single sign-on', icon: <Shield size={16} /> },
    { id: 'tokens', label: 'API tokens', icon: <KeyRound size={16} /> },
  ] },
  { title: 'Cost data', items: [
    { id: 'pricing', label: 'Prices', icon: <Coins size={16} /> },
    { id: 'billing', label: 'Cloud bill', icon: <Receipt size={16} /> },
    { id: 'metrics', label: 'Usage metrics', icon: <Activity size={16} /> },
  ] },
  { title: 'Integrations', items: [
    { id: 'clouds', label: 'Cloud accounts', icon: <Cloud size={16} /> },
    { id: 'notifications', label: 'Notifications', icon: <MessageSquare size={16} /> },
    { id: 'ai', label: 'AI assistant', icon: <Bot size={16} /> },
    { id: 'mcp', label: 'MCP server', icon: <Plug size={16} /> },
  ] },
]
const allSections = navGroups.flatMap(g => g.items.map(i => i.id))
const sectionKey = 'costdeck.settings.section'

function initialSection(): Section {
  try {
    const v = localStorage.getItem(sectionKey) as Section | null
    if (v && allSections.includes(v)) return v
  } catch {
    // Storage may be unavailable; start at the top.
  }
  return 'users'
}

interface NavStatus { text: string; on?: boolean }

// navStatus summarises each section from the saved settings, so the menu shows what is
// connected without opening every page.
function navStatus(st: SettingsData | null): Partial<Record<Section, NavStatus>> {
  if (!st) return {}
  const onOff = (on?: boolean): NavStatus => ({ text: on ? 'On' : 'Off', on: !!on })
  const clouds = [st.providers.aws?.enabled && 'AWS', st.providers.azure?.enabled && 'Azure', st.providers.gcp?.enabled && 'GCP'].filter(Boolean)
  const basis = st.pricing?.effective?.basis?.toLowerCase() || ''
  return {
    sso: onOff(st.auth?.entra?.enabled),
    pricing: { text: basis.startsWith('custom') ? 'Custom' : basis.includes('aws') || basis.includes('azure') ? 'List' : 'Estimate' },
    billing: onOff(st.billing?.enabled),
    metrics: { text: st.integrations.victoriaMetrics?.enabled ? 'VictoriaMetrics' : 'metrics-server' },
    clouds: { text: clouds.length ? clouds.join(', ') : 'None', on: clouds.length > 0 },
    notifications: onOff(st.integrations.messenger?.webex?.enabled),
    ai: onOff(st.integrations.ai?.enabled),
    mcp: onOff(st.integrations.mcp?.enabled),
  }
}

function SettingsNav({ value, onChange, status, dirty }: {
  value: Section; onChange: (s: Section) => void; status: Partial<Record<Section, NavStatus>>; dirty: Partial<Record<Section, boolean>>
}) {
  return (
    <nav className="lg:w-60 shrink-0" aria-label="Settings sections">
      <div className="lg:sticky lg:top-6 flex lg:flex-col gap-5 overflow-x-auto pb-2 lg:pb-0">
        {navGroups.map(g => (
          <div key={g.title} className="shrink-0">
            <div className="px-3 mb-1.5 text-[11px] font-semibold uppercase tracking-wider text-slate-400">{g.title}</div>
            <div className="flex lg:flex-col gap-0.5">
              {g.items.map(it => {
                const active = value === it.id
                const st = status[it.id]
                return (
                  <button key={it.id} type="button" onClick={() => onChange(it.id)} aria-current={active ? 'page' : undefined}
                    className={`flex items-center gap-2.5 px-3 py-2 rounded-lg text-sm text-left whitespace-nowrap border transition-colors ${active ? 'bg-white border-slate-200 shadow-sm font-semibold text-slate-900' : 'border-transparent text-slate-600 hover:bg-white/70'}`}>
                    <span className={active ? 'text-brand-600' : 'text-slate-400'}>{it.icon}</span>
                    <span className="flex-1">{it.label}</span>
                    {dirty[it.id]
                      ? <span className="w-2 h-2 rounded-full bg-amber-500" title="Unsaved changes" />
                      : st && <span className={`text-[11px] ${st.on ? 'text-brand-700 font-medium' : 'text-slate-400'}`}>{st.text}</span>}
                  </button>
                )
              })}
            </div>
          </div>
        ))}
      </div>
    </nav>
  )
}

// ─── Sub-Components ─────────────────────────────────────────────────────────

type BillingCloud = 'aws' | 'azure' | 'gcp'

interface BillingSettings {
  enabled?: boolean
  aws?: { tagKey?: string; tagValue: string }
  azure?: { resourceGroup: string }
  gcp?: { table: string; clusterName: string }
  status?: {
    source?: string; factor?: string; from?: string; to?: string; billed?: string; list?: string; currency?: string
    lastChecked?: string; lastReconciled?: string; error?: string
  }
}

const billingHelp: Record<BillingCloud, string> = {
  aws: 'Reads the amortized EC2 cost from Cost Explorer (needs ce:GetCostAndUsage). Activate the tag as a cost allocation tag in the billing console first.',
  azure: 'Reads the amortized virtual machine cost of the AKS node resource group from Cost Management (Cost Management Reader on that group).',
  gcp: 'Queries the billing export in BigQuery, credits included (BigQuery Job User, and BigQuery Data Viewer on the dataset).',
}

// BillingSection sets up reconciliation with the cloud bill and shows its last result.
function BillingSection({ onSaved }: { onSaved: (enabled: boolean) => void }) {
  const [b, setB] = useState<BillingSettings | null>(null)
  const [cloud, setCloud] = useState<BillingCloud>('aws')
  const [busy, setBusy] = useState(false)
  const [message, setMessage] = useState<string | null>(null)

  const load = () => fetch('/api/settings').then(r => r.json()).then(d => {
    const bs: BillingSettings = d.billing || {}
    setB(bs)
    setCloud(bs.azure ? 'azure' : bs.gcp ? 'gcp' : 'aws')
  }).catch(() => setB({}))
  usePolling(load, null)

  if (!b) return null
  const st = b.status
  const set = (patch: Partial<BillingSettings>) => setB({ ...b, ...patch })
  const field = 'w-full px-3 py-2 bg-white border border-slate-200 rounded-lg text-sm font-mono'

  const save = async () => {
    setBusy(true)
    setMessage(null)
    try {
      const body = {
        enabled: !!b.enabled,
        aws: cloud === 'aws' ? { tagKey: b.aws?.tagKey || 'aws:eks:cluster-name', tagValue: b.aws?.tagValue || '' } : undefined,
        azure: cloud === 'azure' ? { resourceGroup: b.azure?.resourceGroup || '' } : undefined,
        gcp: cloud === 'gcp' ? { table: b.gcp?.table || '', clusterName: b.gcp?.clusterName || '' } : undefined,
      }
      const res = await fetch('/api/settings', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ billing: body }) })
      if (!res.ok) throw new Error(await apiError(res))
      setMessage('Saved.')
      onSaved(!!b.enabled)
      await load()
    } catch (e) {
      setMessage(errorMessage(e))
    } finally {
      setBusy(false)
    }
  }
  const reconcile = async () => {
    setBusy(true)
    setMessage(null)
    try {
      const res = await fetch('/api/billing/reconcile', { method: 'POST' })
      if (!res.ok) throw new Error(await apiError(res))
      await load()
    } catch (e) {
      setMessage(errorMessage(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div>
      <div className="flex items-center justify-between">
        <div>
          <h4 className="font-bold text-slate-800">Reconcile with the cloud bill</h4>
          <p className="text-sm text-slate-500 mt-1">
            Compares a week of the real bill for the cluster's nodes with list prices and scales every compute cost by the ratio, so Savings Plans,
            Reserved Instances, committed use discounts and spot prices show in every figure. Checked every six hours; the bill lags by two days.
          </p>
        </div>
        <label className="relative inline-flex items-center cursor-pointer ml-4">
          <input type="checkbox" checked={!!b.enabled} onChange={e => set({ enabled: e.target.checked })} className="sr-only peer" />
          <div className={`w-11 h-6 rounded-full transition-colors ${b.enabled ? 'bg-brand-600' : 'bg-slate-300'}`}>
            <div className={`absolute top-0.5 left-0.5 w-5 h-5 bg-white rounded-full shadow-md transition-transform ${b.enabled ? 'translate-x-5' : 'translate-x-0'}`} />
          </div>
        </label>
      </div>
      <div className="mt-4 flex gap-1">
        {(['aws', 'azure', 'gcp'] as BillingCloud[]).map(c => (
          <button key={c} type="button" onClick={() => setCloud(c)}
            className={`px-3 py-1.5 rounded-lg text-sm font-semibold border ${cloud === c ? 'bg-slate-900 text-white border-slate-900' : 'bg-white text-slate-600 border-slate-200'}`}>
            {c === 'aws' ? 'AWS' : c === 'azure' ? 'Azure' : 'Google Cloud'}
          </button>
        ))}
      </div>
      <p className="mt-2 text-xs text-slate-500">{billingHelp[cloud]} It uses the credentials of the {cloud === 'aws' ? 'AWS' : cloud === 'azure' ? 'Azure' : 'Google Cloud'} provider above, or the pod identity.</p>
      <div className="mt-3 grid grid-cols-2 gap-3">
        {cloud === 'aws' && <>
          <div><label className="text-[10px] font-bold uppercase tracking-wider text-slate-500 mb-1 block">Tag key</label>
            <input className={field} value={b.aws?.tagKey || 'aws:eks:cluster-name'} onChange={e => set({ aws: { tagValue: b.aws?.tagValue || '', tagKey: e.target.value } })} /></div>
          <div><label className="text-[10px] font-bold uppercase tracking-wider text-slate-500 mb-1 block">Tag value (cluster name)</label>
            <input className={field} value={b.aws?.tagValue || ''} placeholder="prod-eu" onChange={e => set({ aws: { tagKey: b.aws?.tagKey, tagValue: e.target.value } })} /></div>
        </>}
        {cloud === 'azure' && (
          <div className="col-span-2"><label className="text-[10px] font-bold uppercase tracking-wider text-slate-500 mb-1 block">Node resource group</label>
            <input className={field} value={b.azure?.resourceGroup || ''} placeholder="MC_rg-prod_aks-prod_westeurope" onChange={e => set({ azure: { resourceGroup: e.target.value } })} /></div>
        )}
        {cloud === 'gcp' && <>
          <div><label className="text-[10px] font-bold uppercase tracking-wider text-slate-500 mb-1 block">Billing export table</label>
            <input className={field} value={b.gcp?.table || ''} placeholder="billing-project.billing.gcp_billing_export_resource_v1_XXXX" onChange={e => set({ gcp: { clusterName: b.gcp?.clusterName || '', table: e.target.value } })} /></div>
          <div><label className="text-[10px] font-bold uppercase tracking-wider text-slate-500 mb-1 block">GKE cluster name</label>
            <input className={field} value={b.gcp?.clusterName || ''} placeholder="prod-eu" onChange={e => set({ gcp: { table: b.gcp?.table || '', clusterName: e.target.value } })} /></div>
        </>}
      </div>
      {st && (st.factor || st.error) && (
        <div className={`mt-4 p-3 rounded-xl border text-xs ${st.error ? 'border-amber-200 bg-amber-50 text-amber-900' : 'border-slate-200 bg-slate-50 text-slate-600'}`}>
          {st.factor && <div><b className="text-slate-800">Billed {Math.round(Number(st.factor) * 100)}% of list price</b> ({st.from} to {st.to}, {st.billed} {st.currency} billed against {st.list} at list) · {st.source}</div>}
          {st.error && <div className="mt-0.5">Last attempt: {st.error}</div>}
          {st.lastChecked && <div className="mt-0.5 text-slate-400">Checked {new Date(st.lastChecked).toLocaleString()}</div>}
        </div>
      )}
      <div className="mt-4 flex items-center gap-2">
        <Button size="sm" variant="primary" onClick={save} disabled={busy}>Save reconciliation</Button>
        <Button size="sm" onClick={reconcile} disabled={busy || !b.enabled}>Reconcile now</Button>
        {message && <span className="text-xs text-slate-600">{message}</span>}
      </div>
    </div>
  )
}

const StatusBadge = ({ connected, error }: { connected: boolean; error?: string }) => (
  <div className={`inline-flex items-center gap-1.5 px-2.5 py-1 rounded-full text-[10px] font-bold uppercase tracking-wider ${connected
      ? 'bg-emerald-50 text-emerald-700 ring-1 ring-emerald-200'
      : error
        ? 'bg-rose-50 text-rose-600 ring-1 ring-rose-200'
        : 'bg-slate-100 text-slate-500 ring-1 ring-slate-200'
    }`}>
    {connected ? <CheckCircle2 size={12} /> : error ? <XCircle size={12} /> : <AlertTriangle size={12} />}
    {connected ? 'Connected' : error ? 'Error' : 'Not configured'}
  </div>
)

const ComingSoonBadge = () => (
  <span className="inline-flex items-center gap-1 px-2.5 py-1 rounded-full text-[10px] font-bold uppercase tracking-wider bg-brand-50 text-brand-600 ring-1 ring-brand-200">
    <Sparkles size={10} />
    Coming Soon
  </span>
)

const SecretInput = ({ value, onChange, placeholder }: { value: string; onChange: (v: string) => void; placeholder: string }) => {
  const [visible, setVisible] = useState(false)
  return (
    <div className="relative">
      <input
        type={visible ? 'text' : 'password'}
        value={value}
        onChange={e => onChange(e.target.value)}
        placeholder={placeholder}
        className="w-full px-4 py-2.5 bg-white border border-slate-200 rounded-xl text-sm focus:outline-none focus:ring-2 focus:ring-brand-500/30 focus:border-brand-400 transition-all pr-10 font-mono"
      />
      <button
        type="button"
        onClick={() => setVisible(!visible)}
        className="absolute right-3 top-1/2 -translate-y-1/2 text-slate-400 hover:text-slate-600 transition-colors"
      >
        {visible ? <EyeOff size={16} /> : <Eye size={16} />}
      </button>
    </div>
  )
}

const TagEditor = ({ tags, onChange }: { tags: Record<string, string>; onChange: (t: Record<string, string>) => void }) => {
  const [newKey, setNewKey] = useState('')
  const [newValue, setNewValue] = useState('')

  const addTag = () => {
    if (newKey.trim()) {
      onChange({ ...tags, [newKey.trim()]: newValue.trim() })
      setNewKey('')
      setNewValue('')
    }
  }

  const removeTag = (key: string) => {
    const updated = { ...tags }
    delete updated[key]
    onChange(updated)
  }

  return (
    <div className="space-y-3">
      <div className="flex flex-wrap gap-2">
        {Object.entries(tags).map(([k, v]) => (
          <div key={k} className="inline-flex items-center gap-1.5 px-3 py-1.5 bg-emerald-50 text-emerald-700 rounded-lg text-xs font-mono ring-1 ring-emerald-200">
            <span className="font-bold">{k}</span>
            <span className="text-emerald-400">=</span>
            <span>{v}</span>
            <button onClick={() => removeTag(k)} className="ml-1 text-emerald-400 hover:text-rose-500 transition-colors">
              <Trash2 size={12} />
            </button>
          </div>
        ))}
        {Object.keys(tags).length === 0 && (
          <span className="text-xs text-slate-400 italic">No tags configured - all resources will be discovered</span>
        )}
      </div>
      <div className="flex gap-2">
        <input
          value={newKey}
          onChange={e => setNewKey(e.target.value)}
          placeholder="Tag key"
          className="flex-1 px-3 py-2 bg-white border border-slate-200 rounded-lg text-xs font-mono focus:outline-none focus:ring-2 focus:ring-brand-500/30 focus:border-brand-400 transition-all"
          onKeyDown={e => e.key === 'Enter' && addTag()}
        />
        <input
          value={newValue}
          onChange={e => setNewValue(e.target.value)}
          placeholder="Tag value"
          className="flex-1 px-3 py-2 bg-white border border-slate-200 rounded-lg text-xs font-mono focus:outline-none focus:ring-2 focus:ring-brand-500/30 focus:border-brand-400 transition-all"
          onKeyDown={e => e.key === 'Enter' && addTag()}
        />
        <button
          onClick={addTag}
          disabled={!newKey.trim()}
          className="px-3 py-2 bg-brand-600 text-white rounded-lg text-xs font-bold hover:bg-brand-600 transition-colors disabled:opacity-50 disabled:cursor-not-allowed flex items-center gap-1"
        >
          <Plus size={14} /> Add
        </button>
      </div>
    </div>
  )
}

// ─── Section Components ─────────────────────────────────────────────────────

// SectionHeader titles a settings section; `action` (its Save button) stays in view while
// the section scrolls.
const SectionHeader = ({ icon, title, subtitle, action }: { icon: React.ReactNode; title: React.ReactNode; subtitle: string; action?: ReactNode }) => (
  <div className="sticky top-0 z-20 -mx-2 px-2 py-3 mb-2 bg-slate-50/95 backdrop-blur flex flex-wrap items-center gap-3">
    <div className="w-10 h-10 rounded-xl bg-brand-50 flex items-center justify-center shrink-0">
      {icon}
    </div>
    <div className="min-w-0 flex-1">
      <h3 className="text-lg font-bold text-slate-800">{title}</h3>
      <p className="text-sm text-slate-500">{subtitle}</p>
    </div>
    {action}
  </div>
)

// ResourceTypePicker chooses which resource types of a cloud are discovered and scaled.
const ResourceTypePicker = ({ options, value, onChange }: {
  options: { id: string; label: string; desc: string }[]
  value: string[]
  onChange: (v: string[]) => void
}) => (
  <div className="grid gap-2 sm:grid-cols-3">
    {options.map(rt => {
      const on = value.includes(rt.id)
      return (
        <label key={rt.id} className={`flex items-start gap-3 p-3 rounded-lg border cursor-pointer transition-colors ${on ? 'border-brand-300 bg-brand-50/60' : 'border-slate-200 hover:border-slate-300'}`}>
          <input type="checkbox" checked={on} onChange={() => onChange(on ? value.filter(v => v !== rt.id) : [...value, rt.id])} className="mt-0.5 accent-brand-600" />
          <span>
            <span className="block text-sm font-semibold text-slate-700">{rt.label}</span>
            <span className="block text-xs text-slate-500">{rt.desc}</span>
          </span>
        </label>
      )
    })}
  </div>
)

const FieldLabel = ({ children, done }: { children: React.ReactNode; done?: boolean }) => (
  <label className="text-xs font-semibold uppercase tracking-wider text-slate-500 mb-1.5 block">
    {children}
    {done && <span className="ml-2 text-emerald-600 normal-case font-medium">✓ Configured</span>}
  </label>
)

const textInput = 'w-full px-3 py-2 bg-white border border-slate-200 rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-brand-500/30 focus:border-brand-400'

const ConnectionTestRow = ({ provider, testing, result, onTest }: {
  provider: string
  testing: string | null
  result: { provider: string; connected: boolean; error?: string; message?: string } | null
  onTest: (provider: string) => void
}) => (
  <div className="flex flex-wrap items-center gap-3">
    <Button size="sm" onClick={() => onTest(provider)} disabled={testing === provider}
      icon={testing === provider ? <RefreshCw size={14} className="animate-spin" /> : <ExternalLink size={14} />}>
      Test connection
    </Button>
    {result?.provider === provider && (
      <span className={`text-xs font-medium flex items-center gap-1 ${result.connected ? 'text-emerald-700' : 'text-rose-600'}`}>
        {result.connected ? <CheckCircle2 size={14} /> : <XCircle size={14} />}
        {result.connected ? result.message || 'Connection successful' : result.error || 'Connection failed'}
      </span>
    )}
  </div>
)

const ProviderCard = ({
  name, logo, children, enabled, onToggle, comingSoon, expanded, onExpand, status
}: {
  name: string; logo: React.ReactNode; children: React.ReactNode; enabled: boolean;
  onToggle: (v: boolean) => void; comingSoon?: boolean; expanded: boolean;
  onExpand: () => void; status?: ProviderStatus
}) => (
  <div className={`bg-white rounded-xl border shadow-sm transition-all duration-300 ${enabled ? 'border-brand-200 ring-1 ring-brand-100' : 'border-slate-200'
    }`}>
    <div
      className="flex items-center justify-between p-5 cursor-pointer select-none"
      onClick={onExpand}
    >
      <div className="flex items-center gap-3">
        {logo}
        <div>
          <div className="flex items-center gap-2">
            <span className="font-bold text-slate-800">{name}</span>
            {comingSoon && <ComingSoonBadge />}
            {!comingSoon && status && <StatusBadge connected={status.connected} error={status.error} />}
          </div>
          {status?.discoveredResources !== undefined && status.connected && (
            <span className="text-[10px] text-slate-400 font-medium">{status.discoveredResources} resources discovered</span>
          )}
        </div>
      </div>
      <div className="flex items-center gap-3">
        <label className="relative inline-flex items-center cursor-pointer" onClick={e => e.stopPropagation()}>
          <input
            type="checkbox"
            checked={enabled}
            onChange={e => onToggle(e.target.checked)}
            className="sr-only peer"
            disabled={comingSoon}
          />
          <div className={`w-11 h-6 rounded-full peer-focus:outline-none peer-focus:ring-4 peer-focus:ring-brand-500/20 transition-colors ${enabled ? 'bg-brand-600' : 'bg-slate-300'
            } ${comingSoon ? 'opacity-50 cursor-not-allowed' : ''}`}>
            <div className={`absolute top-0.5 left-0.5 w-5 h-5 bg-white rounded-full shadow-md transition-transform ${enabled ? 'translate-x-5' : 'translate-x-0'
              }`} />
          </div>
        </label>
        {expanded ? <ChevronUp size={18} className="text-slate-400" /> : <ChevronDown size={18} className="text-slate-400" />}
      </div>
    </div>
    {expanded && !comingSoon && (
      <div className="px-5 pb-5 pt-2 border-t border-slate-100 animate-in slide-in-from-top-2 duration-200">
        {children}
      </div>
    )}
    {expanded && comingSoon && (
      <div className="px-5 pb-5 pt-2 border-t border-slate-100">
        <div className="flex items-center gap-3 py-8 justify-center text-slate-400">
          <Sparkles size={20} />
          <span className="text-sm font-medium">This integration will be available in a future release</span>
        </div>
      </div>
    )}
  </div>
)

// ─── Main Component ─────────────────────────────────────────────────────────

// Suggestions only: "Load models" asks the provider which models the key can actually use.
const MODEL_SUGGESTIONS: Record<string, string[]> = {
  anthropic: ['claude-opus-5-5', 'claude-sonnet-5-5', 'claude-haiku-4-5', 'claude-fable-5-1'],
  openai: [],
  gemini: ['gemini-2.5-flash', 'gemini-2.5-pro'],
  local: [],
}

const DEFAULT_MODEL: Record<string, string> = {
  anthropic: 'claude-opus-5-5',
  openai: 'gpt-4o',
  gemini: 'gemini-2.5-flash',
  local: '',
}

export default function SettingsPage() {
  const [settings, setSettings] = useState<SettingsData | null>(null)
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [testing, setTesting] = useState<string | null>(null)
  const [testResult, setTestResult] = useState<{ provider: string; connected: boolean; error?: string; message?: string } | null>(null)
  const [saveMessage, setSaveMessage] = useState<{ section: Section; text: string; ok: boolean } | null>(null)
  const [expandedProvider, setExpandedProvider] = useState<string | null>('aws')
  const [section, setSection] = useState<Section>(initialSection)
  const [dirty, setDirty] = useState<Partial<Record<Section, boolean>>>({})
  const go = (s: Section) => {
    setSection(s)
    try { localStorage.setItem(sectionKey, s) } catch { /* not remembered */ }
  }

  // AWS form state
  const [awsAccessKey, setAwsAccessKey] = useState('')
  const [awsSecretKey, setAwsSecretKey] = useState('')
  const [awsRegion, setAwsRegion] = useState('us-east-1')
  const [awsEnabled, setAwsEnabled] = useState(true)
  const [awsTags, setAwsTags] = useState<Record<string, string>>({})
  const [awsResourceTypes, setAwsResourceTypes] = useState<string[]>(['aurora'])

  // Azure form state
  const [azureEnabled, setAzureEnabled] = useState(false)
  const [azureSubscription, setAzureSubscription] = useState('')
  const [azureTenant, setAzureTenant] = useState('')
  const [azureClientId, setAzureClientId] = useState('')
  const [azureClientSecret, setAzureClientSecret] = useState('')
  const [azureTags, setAzureTags] = useState<Record<string, string>>({})
  const [azureTypes, setAzureTypes] = useState<string[]>(['vm', 'postgres', 'mysql'])

  // GCP form state
  const [gcpEnabled, setGcpEnabled] = useState(false)
  const [gcpProject, setGcpProject] = useState('')
  const [gcpKey, setGcpKey] = useState('')
  const [gcpLabels, setGcpLabels] = useState<Record<string, string>>({})
  const [gcpTypes, setGcpTypes] = useState<string[]>(['gce', 'cloudsql'])

  // AI form state
  const [aiEnabled, setAiEnabled] = useState(false)
  const [aiProvider, setAiProvider] = useState('openai')
  const [aiModel, setAiModel] = useState('')
  const [aiBaseUrl, setAiBaseUrl] = useState('')

  // Features state
  const [cloudPricingApi, setCloudPricingApi] = useState(false)
  const [priceCpu, setPriceCpu] = useState('')
  const [priceMem, setPriceMem] = useState('')
  const [priceCurrency, setPriceCurrency] = useState('')
  const [priceStorage, setPriceStorage] = useState('')
  const [priceLB, setPriceLB] = useState('')
  const [aiApiKey, setAiApiKey] = useState('')
  const [aiSkipSslVerify, setAiSkipSslVerify] = useState(false)
  const [aiModels, setAiModels] = useState<string[]>([])
  const [aiModelsLoading, setAiModelsLoading] = useState(false)
  const [aiModelsError, setAiModelsError] = useState<string | null>(null)

  // Webex form state
  const [webexEnabled, setWebexEnabled] = useState(false)
  const [webexRoomId, setWebexRoomId] = useState('')
  const [webexBotToken, setWebexBotToken] = useState('')
  const [webexWebhookSecret, setWebexWebhookSecret] = useState('')
  const [webexNotify, setWebexNotify] = useState(false)

  // VictoriaMetrics form state
  const [vmEnabled, setVmEnabled] = useState(false)
  const [vmEndpoint, setVmEndpoint] = useState('')
  const [vmRetentionDays, setVmRetentionDays] = useState(7)
  const [vmBearerToken, setVmBearerToken] = useState('')
  const [vmUsername, setVmUsername] = useState('')
  const [vmPassword, setVmPassword] = useState('')
  const [vmAuthMode, setVmAuthMode] = useState<'bearer' | 'basic'>('bearer')
  const [vmLabelSelector, setVmLabelSelector] = useState('')
  const [vmSkipSsl, setVmSkipSsl] = useState(false)
  const [vmCaCert, setVmCaCert] = useState('')

  // Access / SSO form state
  const [disableLocalLogin, setDisableLocalLogin] = useState(false)
  const [entraEnabled, setEntraEnabled] = useState(false)
  const [entraTenant, setEntraTenant] = useState('')
  const [entraClient, setEntraClient] = useState('')
  const [entraSecret, setEntraSecret] = useState('')
  const [entraRedirect, setEntraRedirect] = useState('')
  const [entraAuthority, setEntraAuthority] = useState('')
  const [entraDefaultRole, setEntraDefaultRole] = useState('viewer')
  const [entraAutoProvision, setEntraAutoProvision] = useState(true)
  const [entraSkipSsl, setEntraSkipSsl] = useState(false)
  const [entraMapping, setEntraMapping] = useState<{ group: string; role: string }[]>([])

  // MCP form state
  const [mcpEnabled, setMcpEnabled] = useState(false)

  const fetchSettings = useCallback(async () => {
    try {
      const res = await fetch('/api/settings')
      if (res.ok) {
        const data: SettingsData = await res.json()
        setSettings(data)
        // Populate form state from fetched data
        if (data.providers.aws) {
          setAwsEnabled(data.providers.aws.enabled)
          setAwsRegion(data.providers.aws.region || 'us-east-1')
          setAwsTags(data.providers.aws.discoveryTags || {})
          setAwsResourceTypes(data.providers.aws.resourceTypes || ['aurora'])
        }
        if (data.providers.azure) {
          setAzureEnabled(data.providers.azure.enabled)
          setAzureSubscription(data.providers.azure.subscriptionId || '')
          setAzureTenant(data.providers.azure.tenantId || '')
          setAzureTags(data.providers.azure.discoveryTags || {})
          if (data.providers.azure.resourceTypes?.length) setAzureTypes(data.providers.azure.resourceTypes)
        }
        if (data.providers.gcp) {
          setGcpEnabled(data.providers.gcp.enabled)
          setGcpProject(data.providers.gcp.projectId || '')
          setGcpLabels(data.providers.gcp.discoveryLabels || {})
          if (data.providers.gcp.resourceTypes?.length) setGcpTypes(data.providers.gcp.resourceTypes)
        }
        if (data.integrations.ai) {
          setAiEnabled(data.integrations.ai.enabled)
          setAiProvider(data.integrations.ai.provider || 'openai')
          setAiModel(data.integrations.ai.model || '')
          setAiBaseUrl(data.integrations.ai.baseUrl || '')
          setAiSkipSslVerify(data.integrations.ai.skipSslVerify || false)
        }
        if (data.integrations.messenger?.webex) {
          setWebexEnabled(data.integrations.messenger.webex.enabled)
          setWebexRoomId(data.integrations.messenger.webex.roomId || '')
          setWebexNotify(data.integrations.messenger.webex.notifyTransitions || false)
        }
        if (data.integrations.victoriaMetrics) {
          setVmEnabled(data.integrations.victoriaMetrics.enabled)
          setVmEndpoint(data.integrations.victoriaMetrics.endpoint || '')
          setVmRetentionDays(data.integrations.victoriaMetrics.retentionDays || 7)
          setVmLabelSelector(data.integrations.victoriaMetrics.labelSelector || '')
          setVmSkipSsl(data.integrations.victoriaMetrics.skipSslVerify || false)
        }
        if (data.integrations.mcp) {
          setMcpEnabled(data.integrations.mcp.enabled)
        }
        if (data.features) {
          setCloudPricingApi(data.features.cloudPricingApi || false)
        }
        if (data.pricing) {
          setPriceCpu(data.pricing.cpuCoreHour || '')
          setPriceMem(data.pricing.memoryGiBHour || '')
          setPriceCurrency(data.pricing.currency || '')
          setPriceStorage(data.pricing.storageGiBMonth || '')
          setPriceLB(data.pricing.loadBalancerMonth || '')
        }
        if (data.auth) {
          setDisableLocalLogin(data.auth.disableLocalLogin)
          const e = data.auth.entra
          if (e) {
            setEntraEnabled(e.enabled)
            setEntraTenant(e.tenantId || '')
            setEntraClient(e.clientId || '')
            setEntraRedirect(e.redirectUrl || '')
            setEntraAuthority(e.authorityHost || '')
            setEntraDefaultRole(e.defaultRole || 'viewer')
            setEntraAutoProvision(e.autoProvision)
            setEntraSkipSsl(e.skipSslVerify)
            setEntraMapping(Object.entries(e.groupRoleMapping || {}).map(([group, role]) => ({ group, role })))
          }
        }
      }
    } catch (err) {
      console.error('Failed to fetch settings:', err)
    } finally {
      setLoading(false)
    }
  }, [])

  usePolling(fetchSettings, null)

  const handleSave = async (sec: SavedSection) => {
    setSaving(true)
    setSaveMessage(null)
    try {
      const full = {
        providers: {
          aws: {
            enabled: awsEnabled,
            region: awsRegion,
            discoveryTags: awsTags,
            resourceTypes: awsResourceTypes,
            ...(awsAccessKey && awsSecretKey ? { accessKeyId: awsAccessKey, secretAccessKey: awsSecretKey } : {}),
          },
          azure: {
            enabled: azureEnabled,
            subscriptionId: azureSubscription,
            tenantId: azureTenant,
            discoveryTags: azureTags,
            resourceTypes: azureTypes,
            ...(azureClientId && azureClientSecret ? { clientId: azureClientId, clientSecret: azureClientSecret } : {}),
          },
          gcp: {
            enabled: gcpEnabled,
            projectId: gcpProject,
            discoveryLabels: gcpLabels,
            resourceTypes: gcpTypes,
            ...(gcpKey ? { serviceAccountJson: gcpKey } : {}),
          },
        },
        integrations: {
          ai: {
            enabled: aiEnabled,
            provider: aiProvider,
            model: aiModel,
            baseUrl: aiBaseUrl,
            skipSslVerify: aiSkipSslVerify,
            ...(aiApiKey ? { apiKey: aiApiKey } : {})
          },
          messenger: {
            webex: {
              enabled: webexEnabled,
              roomId: webexRoomId,
              notifyTransitions: webexNotify,
              ...(webexBotToken ? { botToken: webexBotToken } : {}),
              ...(webexWebhookSecret ? { webhookSecret: webexWebhookSecret } : {})
            }
          },
          victoriaMetrics: {
            enabled: vmEnabled,
            endpoint: vmEndpoint,
            retentionDays: vmRetentionDays,
            labelSelector: vmLabelSelector,
            skipSslVerify: vmSkipSsl,
            ...vmCredentials(),
          },
          mcp: {
            enabled: mcpEnabled,
          }
        },
        features: {
          cloudPricingApi: cloudPricingApi,
        },
        pricing: {
          cpuCoreHour: priceCpu,
          memoryGiBHour: priceMem,
          storageGiBMonth: priceStorage,
          loadBalancerMonth: priceLB,
          currency: priceCurrency,
        },
        auth: {
          disableLocalLogin,
          entra: {
            enabled: entraEnabled,
            tenantId: entraTenant,
            clientId: entraClient,
            redirectUrl: entraRedirect,
            authorityHost: entraAuthority,
            defaultRole: entraDefaultRole,
            autoProvision: entraAutoProvision,
            skipSslVerify: entraSkipSsl,
            groupRoleMapping: Object.fromEntries(entraMapping.filter(m => m.group.trim()).map(m => [m.group.trim(), m.role])),
            ...(entraSecret ? { clientSecret: entraSecret } : {}),
          },
        },
      }

      // Only the section's own part is sent; the server leaves every other section alone.
      const parts: Record<SavedSection, object> = {
        clouds: { providers: full.providers },
        ai: { integrations: { ai: full.integrations.ai } },
        notifications: { integrations: { messenger: full.integrations.messenger } },
        metrics: { integrations: { victoriaMetrics: full.integrations.victoriaMetrics } },
        mcp: { integrations: { mcp: full.integrations.mcp } },
        pricing: { features: full.features, pricing: full.pricing },
        sso: { auth: full.auth },
      }
      const res = await fetch('/api/settings', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(parts[sec]),
      })

      if (res.ok) {
        const data = await res.json()
        setSettings(data)
        setAwsAccessKey('')
        setAwsSecretKey('')
        setAzureClientSecret('')
        setGcpKey('')
        setAiApiKey('')
        setWebexBotToken('')
        setWebexWebhookSecret('')
        setVmBearerToken('')
        setVmUsername('')
        setVmPassword('')
        setVmCaCert('')
        setEntraSecret('')
        setDirty(d => ({ ...d, [sec]: false }))
        setSaveMessage({ section: sec, text: 'Saved', ok: true })
        setTimeout(() => setSaveMessage(m => (m?.section === sec && m.ok ? null : m)), 3000)
      } else {
        setSaveMessage({ section: sec, text: `Not saved: ${await apiError(res)}`, ok: false })
      }
    } catch (err) {
      setSaveMessage({ section: sec, text: `Not saved: ${errorMessage(err)}`, ok: false })
    } finally {
      setSaving(false)
    }
  }

  // Only credentials the user actually typed are sent; empty fields keep the stored Secret.
  const vmCredentials = () => ({
    ...(vmAuthMode === 'bearer' && vmBearerToken ? { bearerToken: vmBearerToken } : {}),
    ...(vmAuthMode === 'basic' && vmUsername ? { username: vmUsername, password: vmPassword } : {}),
    ...(vmCaCert ? { caCert: vmCaCert } : {}),
  })

  const handleTestConnection = async (provider: string) => {
    setTesting(provider)
    setTestResult(null)
    try {
      const body: Record<string, unknown> = {}
      if (provider === 'aws') {
        body.accessKeyId = awsAccessKey
        body.secretAccessKey = awsSecretKey
        body.region = awsRegion
      } else if (provider === 'azure') {
        Object.assign(body, {
          subscriptionId: azureSubscription,
          tenantId: azureTenant,
          ...(azureClientId && azureClientSecret ? { clientId: azureClientId, clientSecret: azureClientSecret } : {}),
        })
      } else if (provider === 'gcp') {
        Object.assign(body, { projectId: gcpProject, ...(gcpKey ? { serviceAccountJson: gcpKey } : {}) })
      } else if (provider === 'ai') {
        body.provider = aiProvider
        body.model = aiModel
        body.baseUrl = aiBaseUrl
        body.apiKey = aiApiKey
        body.skipSslVerify = aiSkipSslVerify
      } else if (provider === 'entra') {
        Object.assign(body, {
          tenantId: entraTenant,
          clientId: entraClient,
          authorityHost: entraAuthority,
          skipSslVerify: entraSkipSsl,
          ...(entraSecret ? { clientSecret: entraSecret } : {}),
        })
      } else if (provider === 'webex') {
        Object.assign(body, {
          roomId: webexRoomId,
          ...(webexBotToken ? { botToken: webexBotToken } : {}),
        })
      } else if (provider === 'victoriametrics') {
        Object.assign(body, {
          endpoint: vmEndpoint,
          labelSelector: vmLabelSelector,
          skipSslVerify: vmSkipSsl,
          ...vmCredentials(),
        })
      }

      const res = await fetch(`/api/settings/providers/${provider}/test`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
      })
      const data = await res.json()
      setTestResult({ provider, connected: data.connected, error: data.error, message: data.message })
    } catch (err) {
      setTestResult({ provider, connected: false, error: String(err) })
    } finally {
      setTesting(null)
    }
  }

  const loadAIModels = async () => {
    setAiModelsLoading(true)
    setAiModelsError(null)
    try {
      const res = await fetch('/api/settings/ai/models', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ provider: aiProvider, baseUrl: aiBaseUrl, skipSslVerify: aiSkipSslVerify, ...(aiApiKey ? { apiKey: aiApiKey } : {}) }),
      })
      const data = await res.json().catch(() => ({}))
      if (!res.ok) throw new Error(data.error || res.statusText)
      setAiModels(data.models || [])
    } catch (err) {
      setAiModelsError((err as Error).message)
    } finally {
      setAiModelsLoading(false)
    }
  }

  const toggleResourceType = (type: string) => {
    setAwsResourceTypes(prev =>
      prev.includes(type) ? prev.filter(t => t !== type) : [...prev, type]
    )
  }

  if (loading) {
    return (
      <div className="flex h-full items-center justify-center">
        <div className="animate-spin rounded-full h-12 w-12 border-b-2 border-brand-500" />
      </div>
    )
  }

  const awsRegions = [
    'us-east-1', 'us-east-2', 'us-west-1', 'us-west-2',
    'eu-west-1', 'eu-west-2', 'eu-west-3', 'eu-central-1', 'eu-north-1',
    'ap-southeast-1', 'ap-southeast-2', 'ap-northeast-1', 'ap-northeast-2', 'ap-south-1',
    'sa-east-1', 'ca-central-1',
  ]

  const saveAction = (sec: SavedSection) => (
    <div className="flex items-center gap-3 shrink-0">
      {saveMessage?.section === sec
        ? <span className={`text-xs font-semibold ${saveMessage.ok ? 'text-brand-700' : 'text-rose-600'}`}>{saveMessage.text}</span>
        : dirty[sec] && <span className="text-xs font-semibold text-amber-700">Unsaved changes</span>}
      <Button variant={dirty[sec] ? 'primary' : 'secondary'} onClick={() => handleSave(sec)} disabled={saving}
        icon={saving ? <RefreshCw size={15} className="animate-spin" /> : <CheckCircle2 size={15} />}>
        Save
      </Button>
    </div>
  )
  // Any edit inside a saved section marks it unsaved until its Save succeeds.
  const markDirty = () => {
    if (isSaved(section) && !dirty[section]) setDirty(d => ({ ...d, [section]: true }))
  }

  return (
    <div className="p-8 max-w-[1240px] mx-auto">
      <PageHeader title="Settings" subtitle="Who can sign in, where cost figures come from, and what Cost Deck connects to" />
      <div className="flex flex-col lg:flex-row gap-8 items-start">
        <SettingsNav value={section} onChange={go} status={navStatus(settings)} dirty={dirty} />
        <div className="flex-1 min-w-0 w-full" onChangeCapture={markDirty}>

      {section === 'users' && (
        <div className="space-y-4 animate-in fade-in duration-300">
          <SectionHeader
            icon={<UsersIcon className="text-brand-600" size={20} />}
            title="Users"
            subtitle="Who signs in with a password, with which role, and who signed in lately"
          />
          <UsersSettings onOpenSSO={() => go('sso')} />
        </div>
      )}

      {/* ─── Cloud Providers Section ─────────────────────────────────────── */}
      {section === 'clouds' && (
        <div className="space-y-4 animate-in fade-in slide-in-from-bottom-2 duration-300">
          <SectionHeader
            icon={<Cloud className="text-brand-600" size={20} />}
            title="Cloud accounts"
            subtitle="Databases and VMs that schedules start and stop, and the credentials for list prices and the bill"
            action={saveAction('clouds')}
          />

          {/* AWS */}
          <ProviderCard
            name="Amazon Web Services"
            logo={<AWSLogo />}
            enabled={awsEnabled}
            onToggle={setAwsEnabled}
            expanded={expandedProvider === 'aws'}
            onExpand={() => setExpandedProvider(expandedProvider === 'aws' ? null : 'aws')}
            status={settings?.providers.aws?.status}
          >
            <div className="space-y-5">
              {/* Region */}
              <div>
                <label className="text-xs font-bold uppercase tracking-wider text-slate-500 mb-1.5 block">Region</label>
                <select
                  value={awsRegion}
                  onChange={e => setAwsRegion(e.target.value)}
                  className="w-full px-4 py-2.5 bg-white border border-slate-200 rounded-xl text-sm focus:outline-none focus:ring-2 focus:ring-brand-500/30 focus:border-brand-400 transition-all appearance-none cursor-pointer"
                >
                  {awsRegions.map(r => <option key={r} value={r}>{r}</option>)}
                </select>
              </div>

              {/* Credentials */}
              <div>
                <label className="text-xs font-bold uppercase tracking-wider text-slate-500 mb-1.5 block">
                  Credentials
                  {settings?.providers.aws?.hasCredentials && (
                    <span className="ml-2 text-emerald-500 normal-case font-medium">✓ Configured</span>
                  )}
                </label>
                <div className="space-y-2">
                  <SecretInput
                    value={awsAccessKey}
                    onChange={setAwsAccessKey}
                    placeholder={settings?.providers.aws?.hasCredentials ? '••••••••••••••••••••' : 'AWS Access Key ID'}
                  />
                  <SecretInput
                    value={awsSecretKey}
                    onChange={setAwsSecretKey}
                    placeholder={settings?.providers.aws?.hasCredentials ? '••••••••••••••••••••' : 'AWS Secret Access Key'}
                  />
                </div>
                <p className="text-[10px] text-slate-400 mt-1.5">
                  Leave empty to keep existing credentials. Credentials are stored in a Kubernetes Secret.
                </p>
              </div>

              {/* Test Connection */}
              <div className="flex items-center gap-3">
                <button
                  onClick={() => handleTestConnection('aws')}
                  disabled={testing === 'aws'}
                  className="flex items-center gap-2 px-4 py-2 bg-slate-100 hover:bg-slate-200 text-slate-700 rounded-lg text-xs font-bold transition-all disabled:opacity-50"
                >
                  {testing === 'aws' ? <RefreshCw size={14} className="animate-spin" /> : <ExternalLink size={14} />}
                  Test Connection
                </button>
                {testResult?.provider === 'aws' && (
                  <span className={`text-xs font-bold flex items-center gap-1 ${testResult.connected ? 'text-emerald-600' : 'text-rose-500'}`}>
                    {testResult.connected ? <CheckCircle2 size={14} /> : <XCircle size={14} />}
                    {testResult.connected ? testResult.message || 'Connection successful' : testResult.error || 'Connection failed'}
                  </span>
                )}
              </div>

              {/* Discovery Tags */}
              <div>
                <label className="text-xs font-bold uppercase tracking-wider text-slate-500 mb-1.5 block">
                  Discovery Tags
                </label>
                <p className="text-[10px] text-slate-400 mb-2">
                  Only AWS resources matching ALL these tags will be discovered and available for scaling
                </p>
                <TagEditor tags={awsTags} onChange={setAwsTags} />
              </div>

              {/* Resource Types */}
              <div>
                <label className="text-xs font-bold uppercase tracking-wider text-slate-500 mb-2 block">
                  Resource Types
                </label>
                <div className="flex gap-3">
                  {[
                    { id: 'aurora', label: 'Aurora Clusters', desc: 'Start/stop RDS Aurora clusters' },
                    { id: 'ec2', label: 'EC2 Instances', desc: 'Start/stop EC2 instances' },
                  ].map(rt => (
                    <label
                      key={rt.id}
                      className={`flex-1 flex items-start gap-3 p-4 rounded-xl border cursor-pointer transition-all ${awsResourceTypes.includes(rt.id)
                          ? 'border-emerald-300 bg-emerald-50/50 ring-1 ring-emerald-200'
                          : 'border-slate-200 hover:border-slate-300'
                        }`}
                    >
                      <input
                        type="checkbox"
                        checked={awsResourceTypes.includes(rt.id)}
                        onChange={() => toggleResourceType(rt.id)}
                        className="mt-0.5 accent-brand-600"
                      />
                      <div>
                        <span className="text-sm font-bold text-slate-700">{rt.label}</span>
                        <p className="text-[10px] text-slate-400 mt-0.5">{rt.desc}</p>
                      </div>
                    </label>
                  ))}
                </div>
              </div>
            </div>
          </ProviderCard>

          {/* Azure */}
          <ProviderCard
            name="Microsoft Azure"
            logo={<AzureLogo />}
            enabled={azureEnabled}
            onToggle={setAzureEnabled}
            expanded={expandedProvider === 'azure'}
            onExpand={() => setExpandedProvider(expandedProvider === 'azure' ? null : 'azure')}
            status={settings?.providers.azure?.status}
          >
            <div className="space-y-5">
              <div className="grid gap-4 sm:grid-cols-2">
                <div>
                  <FieldLabel>Subscription ID</FieldLabel>
                  <input value={azureSubscription} onChange={e => setAzureSubscription(e.target.value.trim())} placeholder="00000000-0000-0000-0000-000000000000" className={`${textInput} font-mono`} />
                </div>
                <div>
                  <FieldLabel>Tenant ID</FieldLabel>
                  <input value={azureTenant} onChange={e => setAzureTenant(e.target.value.trim())} placeholder="Directory (tenant) ID" className={`${textInput} font-mono`} />
                </div>
              </div>
              <div>
                <FieldLabel done={settings?.providers.azure?.hasCredentials}>Service principal</FieldLabel>
                <div className="grid gap-2 sm:grid-cols-2">
                  <input value={azureClientId} onChange={e => setAzureClientId(e.target.value.trim())} placeholder="Application (client) ID" className={`${textInput} font-mono`} />
                  <SecretInput value={azureClientSecret} onChange={setAzureClientSecret}
                    placeholder={settings?.providers.azure?.hasCredentials ? '••••••••••••••••' : 'Client secret'} />
                </div>
                <p className="text-xs text-slate-400 mt-1.5">
                  Leave empty to keep the stored secret, or to use the pod identity (AKS workload identity or a managed identity).
                  The identity needs <code>Microsoft.Compute/virtualMachines/start|deallocate</code> and the flexible servers' <code>start|stop</code> actions; Virtual Machine Contributor covers VMs.
                </p>
              </div>
              <ConnectionTestRow provider="azure" testing={testing} result={testResult} onTest={handleTestConnection} />
              <div>
                <FieldLabel>Discovery tags</FieldLabel>
                <p className="text-xs text-slate-400 mb-2">Only resources carrying all of these tags are offered for scheduling.</p>
                <TagEditor tags={azureTags} onChange={setAzureTags} />
              </div>
              <div>
                <FieldLabel>Resource types</FieldLabel>
                <ResourceTypePicker value={azureTypes} onChange={setAzureTypes} options={[
                  { id: 'vm', label: 'Virtual machines', desc: 'Start, or deallocate so compute stops billing' },
                  { id: 'postgres', label: 'PostgreSQL', desc: 'Flexible servers: start and stop' },
                  { id: 'mysql', label: 'MySQL', desc: 'Flexible servers: start and stop' },
                ]} />
              </div>
            </div>
          </ProviderCard>

          {/* GCP */}
          <ProviderCard
            name="Google Cloud"
            logo={<GCPLogo />}
            enabled={gcpEnabled}
            onToggle={setGcpEnabled}
            expanded={expandedProvider === 'gcp'}
            onExpand={() => setExpandedProvider(expandedProvider === 'gcp' ? null : 'gcp')}
            status={settings?.providers.gcp?.status}
          >
            <div className="space-y-5">
              <div>
                <FieldLabel>Project ID</FieldLabel>
                <input value={gcpProject} onChange={e => setGcpProject(e.target.value.trim())} placeholder="Defaults to the service account's project" className={`${textInput} font-mono sm:w-96`} />
              </div>
              <div>
                <FieldLabel done={settings?.providers.gcp?.hasCredentials}>Service account key</FieldLabel>
                <textarea value={gcpKey} onChange={e => setGcpKey(e.target.value)} rows={4} spellCheck={false}
                  placeholder={settings?.providers.gcp?.hasCredentials ? 'Stored. Paste a new key to replace it.' : 'Paste the JSON key of a service account'}
                  className={`${textInput} font-mono text-xs`} />
                <p className="text-xs text-slate-400 mt-1.5">
                  Leave empty to keep the stored key, or to use GKE workload identity. The account needs <code>roles/compute.instanceAdmin.v1</code> and <code>roles/cloudsql.editor</code> (or narrower custom roles).
                </p>
              </div>
              <ConnectionTestRow provider="gcp" testing={testing} result={testResult} onTest={handleTestConnection} />
              <div>
                <FieldLabel>Discovery labels</FieldLabel>
                <p className="text-xs text-slate-400 mb-2">Only resources carrying all of these labels are offered for scheduling.</p>
                <TagEditor tags={gcpLabels} onChange={setGcpLabels} />
              </div>
              <div>
                <FieldLabel>Resource types</FieldLabel>
                <ResourceTypePicker value={gcpTypes} onChange={setGcpTypes} options={[
                  { id: 'gce', label: 'Compute Engine', desc: 'Start and stop VM instances' },
                  { id: 'cloudsql', label: 'Cloud SQL', desc: 'Stop and start through the activation policy' },
                ]} />
              </div>
            </div>
          </ProviderCard>
        </div>
      )}

      {/* ─── Monitoring Section (VictoriaMetrics) ──────────────────────────── */}
      {section === 'metrics' && (
        <div className="space-y-4 animate-in fade-in slide-in-from-bottom-2 duration-300">
          <SectionHeader
            icon={<Activity className="text-brand-600" size={20} />}
            title="Usage metrics"
            subtitle="Where namespace usage and right-sizing advice come from"
            action={saveAction('metrics')}
          />

          <div className="bg-white rounded-xl border border-slate-200 shadow-sm p-6">
            <div className="flex items-center justify-between mb-6">
              <div className="flex items-center gap-3">
                <div className="w-10 h-10 rounded-xl bg-brand-50 flex items-center justify-center">
                  <Activity className="text-brand-600" size={20} />
                </div>
                <div>
                  <span className="font-bold text-slate-800">VictoriaMetrics</span>
                  <p className="text-[10px] text-slate-400">PromQL-compatible metrics backend</p>
                </div>
              </div>
              <div className="flex items-center gap-3">
                {vmEnabled && settings?.integrations.victoriaMetrics?.status && (
                  <span title={settings.integrations.victoriaMetrics.status.error || ''}>
                    <StatusBadge
                      connected={settings.integrations.victoriaMetrics.status.connected}
                      error={settings.integrations.victoriaMetrics.status.error}
                    />
                  </span>
                )}
                <label className="relative inline-flex items-center cursor-pointer">
                  <input type="checkbox" checked={vmEnabled} onChange={e => setVmEnabled(e.target.checked)} className="sr-only peer" />
                  <div className={`w-11 h-6 rounded-full ${vmEnabled ? 'bg-brand-600' : 'bg-slate-300'}`}>
                    <div className={`absolute top-0.5 left-0.5 w-5 h-5 bg-white rounded-full shadow-md transition-transform ${vmEnabled ? 'translate-x-5' : 'translate-x-0'}`} />
                  </div>
                </label>
              </div>
            </div>

            <div className={`space-y-5 ${!vmEnabled ? 'opacity-50 pointer-events-none' : ''}`}>
              {/* Info banner */}
              <div className="flex items-start gap-3 p-4 bg-slate-50 rounded-xl border border-slate-200">
                <Info size={16} className="text-slate-400 mt-0.5 flex-shrink-0" />
                <div className="text-xs text-slate-600">
                  <p className="font-bold mb-1">Metrics Source Override</p>
                  <p>When enabled, namespace insights, pod usage and right-sizing advice come from VictoriaMetrics instead of the Kubernetes Metrics Server. Changes apply immediately — no operator restart. If VictoriaMetrics is unreachable, CostDeck falls back to metrics-server and reports why.</p>
                </div>
              </div>

              {/* Endpoint */}
              <div>
                <label className="text-xs font-bold uppercase tracking-wider text-slate-500 mb-1.5 block">Endpoint URL</label>
                <input
                  value={vmEndpoint}
                  onChange={e => setVmEndpoint(e.target.value)}
                  placeholder="http://vmselect.monitoring.svc:8481/select/0/prometheus"
                  className="w-full px-4 py-2.5 bg-white border border-slate-200 rounded-xl text-sm font-mono focus:outline-none focus:ring-2 focus:ring-brand-500/30 focus:border-brand-400 transition-all"
                />
                <p className="text-[10px] text-slate-400 mt-1">Single node: http://vmsingle.monitoring.svc:8428 • Cluster: http://vmselect.monitoring.svc:8481/select/0/prometheus • Prometheus also works</p>
              </div>

              {/* Label selector */}
              <div>
                <label className="text-xs font-bold uppercase tracking-wider text-slate-500 mb-1.5 block">Cluster label selector <span className="normal-case font-normal text-slate-400">(optional)</span></label>
                <input
                  value={vmLabelSelector}
                  onChange={e => setVmLabelSelector(e.target.value)}
                  placeholder='cluster="prod-eu"'
                  className="w-full px-4 py-2.5 bg-white border border-slate-200 rounded-xl text-sm font-mono focus:outline-none focus:ring-2 focus:ring-brand-500/30 focus:border-brand-400 transition-all"
                />
                <p className="text-[10px] text-slate-400 mt-1">Required when one VictoriaMetrics stores several clusters — otherwise namespaces with the same name are summed across clusters.</p>
              </div>

              {/* Retention Days */}
              <div>
                <label className="text-xs font-bold uppercase tracking-wider text-slate-500 mb-1.5 block">Lookback window (days)</label>
                <div className="flex items-center gap-3">
                  <input
                    type="number"
                    min={1}
                    max={90}
                    value={vmRetentionDays}
                    onChange={e => setVmRetentionDays(parseInt(e.target.value) || 7)}
                    className="w-24 px-4 py-2.5 bg-white border border-slate-200 rounded-xl text-sm text-center focus:outline-none focus:ring-2 focus:ring-brand-500/30 focus:border-brand-400 transition-all"
                  />
                  <span className="text-xs text-slate-400">days of history in the namespace usage charts and behind right-sizing advice (advice uses at most 14)</span>
                </div>
              </div>

              {/* Auth Mode */}
              <div>
                <label className="text-xs font-bold uppercase tracking-wider text-slate-500 mb-2 block">Authentication</label>
                <div className="flex gap-2 mb-3">
                  <button
                    onClick={() => setVmAuthMode('bearer')}
                    className={`px-4 py-2 rounded-lg text-xs font-bold transition-all ${vmAuthMode === 'bearer'
                        ? 'bg-brand-600 text-white shadow-sm'
                        : 'bg-slate-100 text-slate-600 hover:bg-slate-200'
                      }`}
                  >
                    Bearer Token
                  </button>
                  <button
                    onClick={() => setVmAuthMode('basic')}
                    className={`px-4 py-2 rounded-lg text-xs font-bold transition-all ${vmAuthMode === 'basic'
                        ? 'bg-brand-600 text-white shadow-sm'
                        : 'bg-slate-100 text-slate-600 hover:bg-slate-200'
                      }`}
                  >
                    Basic Auth
                  </button>
                </div>
                {vmAuthMode === 'bearer' ? (
                  <SecretInput value={vmBearerToken} onChange={setVmBearerToken} placeholder="Enter Bearer Token" />
                ) : (
                  <div className="grid grid-cols-2 gap-3">
                    <div>
                      <label className="text-[10px] font-bold text-slate-400 mb-1 block">Username</label>
                      <input
                        value={vmUsername}
                        onChange={e => setVmUsername(e.target.value)}
                        placeholder="Username"
                        className="w-full px-4 py-2.5 bg-white border border-slate-200 rounded-xl text-sm focus:outline-none focus:ring-2 focus:ring-brand-500/30 focus:border-brand-400 transition-all"
                      />
                    </div>
                    <div>
                      <label className="text-[10px] font-bold text-slate-400 mb-1 block">Password</label>
                      <SecretInput value={vmPassword} onChange={setVmPassword} placeholder="Password" />
                    </div>
                  </div>
                )}
                <p className="text-[10px] text-slate-400 mt-1.5">Optional. Leave empty if your VictoriaMetrics does not require authentication.</p>
              </div>

              {/* TLS */}
              <div>
                <label className="text-xs font-bold uppercase tracking-wider text-slate-500 mb-1.5 block">TLS</label>
                <textarea
                  value={vmCaCert}
                  onChange={e => setVmCaCert(e.target.value)}
                  rows={3}
                  placeholder="-----BEGIN CERTIFICATE----- (optional custom CA, PEM)"
                  className="w-full px-4 py-2.5 bg-white border border-slate-200 rounded-xl text-xs font-mono focus:outline-none focus:ring-2 focus:ring-brand-500/30 focus:border-brand-400 transition-all"
                />
                <label className="flex items-center gap-2 mt-2 text-xs font-bold text-slate-500 cursor-pointer">
                  <input type="checkbox" checked={vmSkipSsl} onChange={e => setVmSkipSsl(e.target.checked)} className="accent-brand-600" />
                  Skip TLS verification (insecure — prefer a custom CA)
                </label>
              </div>

              {/* Test Connection */}
              <div className="flex items-center gap-3">
                <button
                  onClick={() => handleTestConnection('victoriametrics')}
                  disabled={testing === 'victoriametrics' || !vmEndpoint}
                  className="flex items-center gap-2 px-4 py-2 bg-slate-100 hover:bg-slate-200 text-slate-700 rounded-lg text-xs font-bold transition-all disabled:opacity-50"
                >
                  {testing === 'victoriametrics' ? <RefreshCw size={14} className="animate-spin" /> : <ExternalLink size={14} />}
                  Test Connection
                </button>
                {testResult?.provider === 'victoriametrics' && (
                  <span className={`text-xs font-bold flex items-center gap-1 ${testResult.connected ? 'text-emerald-600' : 'text-rose-500'}`}>
                    {testResult.connected ? <CheckCircle2 size={14} /> : <XCircle size={14} />}
                    {testResult.connected ? 'Connected — container metrics found' : testResult.error || 'Connection failed'}
                  </span>
                )}
              </div>
            </div>

            {vmEnabled && vmEndpoint && (
              <div className="flex items-center gap-3 mt-6 pt-4 border-t border-slate-100 justify-center text-brand-600">
                <Activity size={16} />
                <span className="text-xs font-medium">Namespace insights will use VictoriaMetrics at {vmEndpoint}</span>
              </div>
            )}
          </div>
        </div>
      )}

      {/* ─── AI Models Section ───────────────────────────────────────────── */}
      {section === 'ai' && (
        <div className="space-y-4 animate-in fade-in slide-in-from-bottom-2 duration-300">
          <SectionHeader
            icon={<Bot className="text-brand-500" size={20} />}
            title="AI assistant"
            subtitle="The model behind the assistant and the cost reports"
            action={saveAction('ai')}
          />

          <div className="bg-white rounded-xl border border-slate-200 shadow-sm p-6">
            <div className="flex items-center justify-between mb-6">
              <div className="flex items-center gap-3">
                <div className="w-10 h-10 rounded-xl bg-brand-500/10 flex items-center justify-center">
                  <Sparkles className="text-brand-500" size={20} />
                </div>
                <div>
                  <span className="font-bold text-slate-800">AI-Powered Insights</span>
                </div>
              </div>
              <label className="relative inline-flex items-center cursor-pointer">
                <input type="checkbox" checked={aiEnabled} onChange={e => setAiEnabled(e.target.checked)} className="sr-only peer" />
                <div className={`w-11 h-6 rounded-full ${aiEnabled ? 'bg-brand-500' : 'bg-slate-300'}`}>
                  <div className={`absolute top-0.5 left-0.5 w-5 h-5 bg-white rounded-full shadow-md transition-transform ${aiEnabled ? 'translate-x-5' : 'translate-x-0'}`} />
                </div>
              </label>
            </div>

            <div className={`space-y-4 ${!aiEnabled ? 'opacity-50 pointer-events-none' : ''}`}>
              <div className="grid grid-cols-2 gap-4">
                <div>
                  <label className="text-xs font-bold uppercase tracking-wider text-slate-500 mb-1.5 block">Provider</label>
                  <select
                    value={aiProvider}
                    onChange={e => {
                      setAiProvider(e.target.value)
                      setAiModel('')
                      setAiModels([])
                    }}
                    className="w-full px-4 py-2.5 bg-white border border-slate-200 rounded-xl text-sm appearance-none focus:outline-none focus:ring-2 focus:ring-brand-500/30 focus:border-brand-400 transition-all"
                  >
                    <option value="anthropic">Anthropic (Claude)</option>
                    <option value="openai">OpenAI</option>
                    <option value="gemini">Google Gemini</option>
                    <option value="local">OpenAI-compatible (Ollama, vLLM, gateway)</option>
                  </select>
                </div>
                <div>
                  <label className="text-xs font-bold uppercase tracking-wider text-slate-500 mb-1.5 flex items-center justify-between">
                    <span>Model</span>
                    <button type="button" onClick={loadAIModels} disabled={aiModelsLoading}
                      className="normal-case tracking-normal text-[10px] font-bold text-brand-600 hover:text-brand-800 disabled:opacity-50">
                      {aiModelsLoading ? 'Loading…' : 'Load available models'}
                    </button>
                  </label>
                  <input
                    list="ai-model-options"
                    value={aiModel}
                    onChange={e => setAiModel(e.target.value)}
                    placeholder={DEFAULT_MODEL[aiProvider] ? `${DEFAULT_MODEL[aiProvider]} (default)` : 'e.g. llama3.1'}
                    className="w-full px-4 py-2.5 bg-white border border-slate-200 rounded-xl text-sm font-mono focus:outline-none focus:ring-2 focus:ring-brand-500/30 focus:border-brand-400 transition-all"
                  />
                  <datalist id="ai-model-options">
                    {(aiModels.length > 0 ? aiModels : MODEL_SUGGESTIONS[aiProvider] || []).map(m => <option key={m} value={m} />)}
                  </datalist>
                  {aiModelsError && <p className="text-[10px] text-rose-500 mt-1">{aiModelsError}</p>}
                  {aiModels.length > 0 && <p className="text-[10px] text-slate-400 mt-1">{aiModels.length} models available to this key</p>}
                </div>
              </div>

              <div>
                <label className="text-xs font-bold uppercase tracking-wider text-slate-500 mb-1 block">API Key</label>
                <p className="text-[10px] text-slate-400 mb-1.5">{aiProvider === 'local' ? 'Leave blank if the endpoint needs no authentication.' : 'Stored in a Kubernetes Secret; leave empty to keep the current key.'}</p>
                <SecretInput value={aiApiKey} onChange={setAiApiKey} placeholder={settings?.integrations.ai?.hasCredentials ? '••••••••••••••••••••' : 'Enter API key'} />
              </div>

              <div className="grid grid-cols-2 gap-4">
                <div>
                  <label className="text-xs font-bold uppercase tracking-wider text-slate-500 mb-1 block">
                    API base URL <span className="text-[10px] normal-case text-slate-400 font-normal">({aiProvider === 'local' ? 'required' : 'optional — gateways and proxies'})</span>
                  </label>
                  <input
                    value={aiBaseUrl}
                    onChange={e => setAiBaseUrl(e.target.value)}
                    placeholder={aiProvider === 'local' ? 'http://ollama.ai.svc:11434/v1' : aiProvider === 'anthropic' ? 'https://api.anthropic.com' : aiProvider === 'gemini' ? 'https://generativelanguage.googleapis.com/v1beta' : 'https://api.openai.com/v1'}
                    className="w-full px-4 py-2.5 bg-white border border-slate-200 rounded-xl text-sm font-mono focus:outline-none focus:ring-2 focus:ring-brand-500/30 focus:border-brand-400 transition-all"
                  />
                </div>
                <label className="flex items-center gap-2 mt-6 text-xs font-bold text-slate-500 cursor-pointer">
                  <input type="checkbox" checked={aiSkipSslVerify} onChange={e => setAiSkipSslVerify(e.target.checked)} className="accent-brand-500" />
                  Skip TLS verification (insecure)
                </label>
              </div>

              {/* Test Connection */}
              <div className="flex items-center gap-3 mt-4">
                <button
                  onClick={() => handleTestConnection('ai')}
                  disabled={testing === 'ai'}
                  className="flex items-center gap-2 px-4 py-2 bg-slate-100 hover:bg-slate-200 text-slate-700 rounded-lg text-xs font-bold transition-all disabled:opacity-50"
                >
                  {testing === 'ai' ? <RefreshCw size={14} className="animate-spin" /> : <ExternalLink size={14} />}
                  Test Connection
                </button>
                {testResult?.provider === 'ai' && (
                  <span className={`text-xs font-bold flex items-center gap-1 ${testResult.connected ? 'text-emerald-600' : 'text-rose-500'}`}>
                    {testResult.connected ? <CheckCircle2 size={14} /> : <XCircle size={14} />}
                    {testResult.connected ? testResult.message || 'Connection successful' : testResult.error || 'Connection failed'}
                  </span>
                )}
              </div>
            </div>

            {aiEnabled && (
              <div className="flex items-center gap-3 mt-6 pt-4 border-t border-slate-100 justify-center text-brand-500">
                <Sparkles size={16} />
                <span className="text-xs font-medium">The assistant reads live cluster data through tools; changes it proposes run only after a user with the operator role confirms them.</span>
              </div>
            )}
          </div>
        </div>
      )}

      {/* ─── Messengers Section ──────────────────────────────────────────── */}
      {section === 'notifications' && (
        <div className="space-y-4 animate-in fade-in slide-in-from-bottom-2 duration-300">
          <SectionHeader
            icon={<MessageSquare className="text-brand-500" size={20} />}
            title="Notifications"
            subtitle="Where alerts, digests and scaling changes are posted, and the chat bot that answers there"
            action={saveAction('notifications')}
          />

          <div className="bg-white rounded-xl border border-slate-200 shadow-sm p-6">
            <div className="flex items-center justify-between mb-6">
              <div className="flex items-center gap-3">
                <WebexLogo />
                <div>
                  <span className="font-bold text-slate-800">Cisco Webex</span>
                  {webexEnabled && settings?.integrations?.messenger?.webex?.status && (
                    <p className="text-[10px] text-slate-400 mt-0.5">
                      {settings.integrations.messenger.webex.status.connected
                        ? settings.integrations.messenger.webex.status.message
                        : settings.integrations.messenger.webex.status.error}
                    </p>
                  )}
                </div>
                {webexEnabled && settings?.integrations?.messenger?.webex?.status && (
                  <StatusBadge
                    connected={settings.integrations.messenger.webex.status.connected}
                    error={settings.integrations.messenger.webex.status.error}
                  />
                )}
              </div>
              <label className="relative inline-flex items-center cursor-pointer">
                <input type="checkbox" checked={webexEnabled} onChange={e => setWebexEnabled(e.target.checked)} className="sr-only peer" />
                <div className={`w-11 h-6 rounded-full ${webexEnabled ? 'bg-brand-500' : 'bg-slate-300'}`}>
                  <div className={`absolute top-0.5 left-0.5 w-5 h-5 bg-white rounded-full shadow-md transition-transform ${webexEnabled ? 'translate-x-5' : 'translate-x-0'}`} />
                </div>
              </label>
            </div>

            <div className={`space-y-4 ${!webexEnabled ? 'opacity-50 pointer-events-none' : ''}`}>
              <div>
                <label className="text-[10px] font-bold uppercase tracking-wider text-slate-500 mb-1.5 block">
                  Bot Token
                  {settings?.integrations?.messenger?.webex?.hasCredentials && (
                    <span className="ml-2 text-emerald-500 normal-case font-medium text-xs">✓ Configured</span>
                  )}
                </label>
                <SecretInput
                  value={webexBotToken}
                  onChange={setWebexBotToken}
                  placeholder={settings?.integrations?.messenger?.webex?.hasCredentials ? '••••••••••••••••••••' : 'Enter Webex Bot Token'}
                />
              </div>

              <div>
                <label className="text-xs font-bold uppercase tracking-wider text-slate-500 mb-1.5 block">Space ID <span className="normal-case font-normal text-slate-400">(needed to scale from chat)</span></label>
                <input
                  value={webexRoomId}
                  onChange={e => setWebexRoomId(e.target.value)}
                  placeholder="The space whose members may scale"
                  className="w-full px-4 py-2.5 bg-white border border-slate-200 rounded-xl text-sm"
                />
                <p className="mt-1 text-[11px] text-slate-400">
                  Scaling commands are accepted only in this space, so its members are who may scale. Without it the bot answers <code>list</code> and <code>status</code> but changes nothing: anyone on Webex can message a bot directly.
                </p>
              </div>

              <label className="flex items-start gap-3 p-3 rounded-xl border border-slate-200 bg-slate-50 cursor-pointer">
                <input type="checkbox" checked={webexNotify} onChange={e => setWebexNotify(e.target.checked)} className="mt-0.5 accent-brand-600" />
                <span>
                  <span className="block text-sm font-bold text-slate-700">Announce scaling transitions</span>
                  <span className="block text-[11px] text-slate-400">Post to the space above whenever a group or namespace finishes scaling up or down, with who triggered it and the estimated savings.</span>
                </span>
              </label>

              <div>
                <label className="text-[10px] font-bold uppercase tracking-wider text-slate-500 mb-1.5 block">
                  Webhook secret <span className="normal-case font-normal text-slate-400">(optional — switches from polling to signed webhooks)</span>
                </label>
                <SecretInput value={webexWebhookSecret} onChange={setWebexWebhookSecret} placeholder="Secret used when registering the webhook" />
                <p className="text-[10px] text-slate-400 mt-1.5">
                  Register a webhook for <code>messages:created</code> pointing to <code>https://&lt;costdeck-host&gt;/api/webex/webhook</code> with this secret. Without it, CostDeck polls Webex every 10 seconds.
                </p>
              </div>

              <div className="flex items-center gap-3">
                <button
                  onClick={() => handleTestConnection('webex')}
                  disabled={testing === 'webex'}
                  className="flex items-center gap-2 px-4 py-2 bg-slate-100 hover:bg-slate-200 text-slate-700 rounded-lg text-xs font-bold transition-all disabled:opacity-50"
                >
                  {testing === 'webex' ? <RefreshCw size={14} className="animate-spin" /> : <ExternalLink size={14} />}
                  Test Connection
                </button>
                {testResult?.provider === 'webex' && (
                  <span className={`text-xs font-bold flex items-center gap-1 ${testResult.connected ? 'text-emerald-600' : 'text-rose-500'}`}>
                    {testResult.connected ? <CheckCircle2 size={14} /> : <XCircle size={14} />}
                    {testResult.connected ? testResult.message : testResult.error || 'Connection failed'}
                  </span>
                )}
              </div>
            </div>

            {webexEnabled && (
              <div className="flex items-center gap-3 mt-6 pt-4 border-t border-slate-100 justify-center text-emerald-500">
                <MessageSquare size={16} />
                <span className="text-xs font-medium">Mention the bot with <code>help</code>, <code>list</code>, <code>scale group &lt;name&gt; up [for 4h]</code> or <code>resume group &lt;name&gt;</code>.</span>
              </div>
            )}
          </div>
        </div>
      )}

      {/* ─── MCP Server Section ────────────────────────────────────────────── */}
      {section === 'mcp' && (
        <div className="space-y-4 animate-in fade-in slide-in-from-bottom-2 duration-300">
          <SectionHeader
            icon={<Plug className="text-brand-600" size={20} />}
            title="MCP server"
            subtitle="Expose Cost Deck's data and actions as tools to AI assistants such as Claude or Cursor"
            action={saveAction('mcp')}
          />

          <div className="bg-white rounded-xl border border-slate-200 shadow-sm p-6">
            <div className="flex items-center justify-between mb-6">
              <div className="flex items-center gap-3">
                <div className="w-10 h-10 rounded-xl bg-brand-50 flex items-center justify-center">
                  <Plug className="text-brand-600" size={20} />
                </div>
                <div>
                  <span className="font-bold text-slate-800">MCP Server</span>
                </div>
              </div>
              <label className="relative inline-flex items-center cursor-pointer">
                <input type="checkbox" checked={mcpEnabled} onChange={e => setMcpEnabled(e.target.checked)} className="sr-only peer" />
                <div className={`w-11 h-6 rounded-full ${mcpEnabled ? 'bg-brand-600' : 'bg-slate-300'}`}>
                  <div className={`absolute top-0.5 left-0.5 w-5 h-5 bg-white rounded-full shadow-md transition-transform ${mcpEnabled ? 'translate-x-5' : 'translate-x-0'}`} />
                </div>
              </label>
            </div>

            <div className={`space-y-4 ${!mcpEnabled ? 'opacity-50 pointer-events-none' : ''}`}>
              <div>
                <label className="text-xs font-bold uppercase tracking-wider text-slate-500 mb-1.5 block">Endpoint</label>
                <code className="block px-4 py-2.5 bg-slate-50 border border-slate-200 rounded-xl text-sm font-mono">{window.location.origin}/mcp</code>
                <p className="text-[10px] text-slate-400 mt-1.5">Streamable HTTP on the dashboard's own host — no extra port or ingress. Clients authenticate with an API token (Access &amp; SSO → API tokens). Viewer tokens see read-only tools; operator tokens can also scale and right-size.</p>
              </div>

              <div className="bg-brand-50 border border-brand-100 rounded-xl p-4 space-y-3">
                <div>
                  <h4 className="text-sm font-bold text-brand-800 mb-1">Cursor / any client with remote MCP support</h4>
                  <pre className="bg-slate-900 rounded-lg p-3 overflow-x-auto text-xs text-slate-200">{`{
  "mcpServers": {
    "costdeck": {
      "url": "${window.location.origin}/mcp",
      "headers": { "Authorization": "Bearer cdk_..." }
    }
  }
}`}</pre>
                </div>
                <div>
                  <h4 className="text-sm font-bold text-brand-800 mb-1">Claude Code</h4>
                  <pre className="bg-slate-900 rounded-lg p-3 overflow-x-auto text-xs text-slate-200">{`claude mcp add --transport http costdeck ${window.location.origin}/mcp \
  --header "Authorization: Bearer cdk_..."`}</pre>
                </div>
              </div>
            </div>
          </div>
        </div>
      )}

      {/* ─── Single sign-on ──────────────────────────────────────────────── */}
      {section === 'sso' && (
        <div className="space-y-4 animate-in fade-in slide-in-from-bottom-2 duration-300">
          <SectionHeader
            icon={<Shield className="text-brand-600" size={20} />}
            title="Single sign-on"
            subtitle="Let people sign in with Microsoft Entra ID; their groups decide their role"
            action={saveAction('sso')}
          />

          <div className="bg-white rounded-xl border border-slate-200 shadow-sm p-6 space-y-5">
            <div className="flex items-center justify-between">
              <div>
                <span className="font-bold text-slate-800">Microsoft Entra ID</span>
                <p className="text-[10px] text-slate-400">OpenID Connect · authorization code flow with PKCE</p>
              </div>
              <label className="relative inline-flex items-center cursor-pointer">
                <input type="checkbox" checked={entraEnabled} onChange={e => setEntraEnabled(e.target.checked)} className="sr-only peer" />
                <div className={`w-11 h-6 rounded-full ${entraEnabled ? 'bg-brand-600' : 'bg-slate-300'}`}>
                  <div className={`absolute top-0.5 left-0.5 w-5 h-5 bg-white rounded-full shadow-md transition-transform ${entraEnabled ? 'translate-x-5' : 'translate-x-0'}`} />
                </div>
              </label>
            </div>

            <div className="p-4 bg-slate-50 border border-slate-200 rounded-xl text-xs text-slate-600 space-y-1.5">
              <p className="font-bold">App registration redirect URI (add one of them in the Azure portal):</p>
              <p><code className="bg-white px-1.5 py-0.5 rounded">{window.location.origin}/api/auth/entra/callback</code> — web platform, recommended</p>
              <p><code className="bg-white px-1.5 py-0.5 rounded">{window.location.origin}/auth/callback</code> — single-page application</p>
              <p className="text-slate-500">Add the optional <b>groups</b> claim (Token configuration → Security groups) or define app roles named admin / operator / viewer.</p>
            </div>

            <div className="grid grid-cols-2 gap-4">
              <div>
                <label className="text-xs font-bold uppercase tracking-wider text-slate-500 mb-1.5 block">Directory (tenant) ID</label>
                <input value={entraTenant} onChange={e => setEntraTenant(e.target.value)} placeholder="00000000-0000-0000-0000-000000000000"
                  className="w-full px-4 py-2.5 bg-white border border-slate-200 rounded-xl text-sm font-mono" />
              </div>
              <div>
                <label className="text-xs font-bold uppercase tracking-wider text-slate-500 mb-1.5 block">Application (client) ID</label>
                <input value={entraClient} onChange={e => setEntraClient(e.target.value)} placeholder="00000000-0000-0000-0000-000000000000"
                  className="w-full px-4 py-2.5 bg-white border border-slate-200 rounded-xl text-sm font-mono" />
              </div>
            </div>

            <div>
              <label className="text-xs font-bold uppercase tracking-wider text-slate-500 mb-1.5 block">
                Client secret
                {settings?.auth?.entra?.hasClientSecret && <span className="ml-2 text-emerald-500 normal-case font-medium">✓ Configured</span>}
              </label>
              <SecretInput value={entraSecret} onChange={setEntraSecret}
                placeholder={settings?.auth?.entra?.hasClientSecret ? '••••••••••••••••••••' : 'Client secret value'} />
              <p className="text-[10px] text-slate-400 mt-1.5">Stored in a Kubernetes Secret and never returned to the browser.</p>
            </div>

            <div className="grid grid-cols-2 gap-4">
              <div>
                <label className="text-xs font-bold uppercase tracking-wider text-slate-500 mb-1.5 block">Redirect URI <span className="normal-case font-normal text-slate-400">(optional)</span></label>
                <input value={entraRedirect} onChange={e => setEntraRedirect(e.target.value)} placeholder={`${window.location.origin}/api/auth/entra/callback`}
                  className="w-full px-4 py-2.5 bg-white border border-slate-200 rounded-xl text-sm font-mono" />
              </div>
              <div>
                <label className="text-xs font-bold uppercase tracking-wider text-slate-500 mb-1.5 block">Authority <span className="normal-case font-normal text-slate-400">(sovereign clouds)</span></label>
                <input value={entraAuthority} onChange={e => setEntraAuthority(e.target.value)} placeholder="https://login.microsoftonline.com"
                  className="w-full px-4 py-2.5 bg-white border border-slate-200 rounded-xl text-sm font-mono" />
              </div>
            </div>

            <div className="grid grid-cols-2 gap-4 items-start">
              <div>
                <label className="text-xs font-bold uppercase tracking-wider text-slate-500 mb-1.5 block">Default role</label>
                <select value={entraDefaultRole} onChange={e => setEntraDefaultRole(e.target.value)}
                  className="w-full px-4 py-2.5 bg-white border border-slate-200 rounded-xl text-sm">
                  <option value="viewer">Viewer — read only</option>
                  <option value="operator">Operator — scale and override schedules</option>
                  <option value="admin">Admin — full access</option>
                </select>
              </div>
              <label className="flex items-start gap-3 p-3 rounded-xl border border-slate-200 bg-slate-50 cursor-pointer mt-6">
                <input type="checkbox" checked={entraAutoProvision} onChange={e => setEntraAutoProvision(e.target.checked)} className="mt-0.5 accent-brand-600" />
                <span>
                  <span className="block text-sm font-bold text-slate-700">Auto-provision users</span>
                  <span className="block text-[11px] text-slate-400">Anyone in the tenant may sign in with the default role. Off: only mapped groups.</span>
                </span>
              </label>
            </div>

            <div>
              <label className="text-xs font-bold uppercase tracking-wider text-slate-500 mb-2 block">Group → role mapping</label>
              <div className="space-y-2">
                {entraMapping.map((m, i) => (
                  <div key={i} className="flex gap-2">
                    <input value={m.group} placeholder="Entra group object ID"
                      onChange={e => setEntraMapping(prev => prev.map((x, j) => j === i ? { ...x, group: e.target.value } : x))}
                      className="flex-1 px-3 py-2 bg-white border border-slate-200 rounded-lg text-xs font-mono" />
                    <select value={m.role}
                      onChange={e => setEntraMapping(prev => prev.map((x, j) => j === i ? { ...x, role: e.target.value } : x))}
                      className="w-36 px-3 py-2 bg-white border border-slate-200 rounded-lg text-xs">
                      <option value="viewer">viewer</option>
                      <option value="operator">operator</option>
                      <option value="admin">admin</option>
                    </select>
                    <button onClick={() => setEntraMapping(prev => prev.filter((_, j) => j !== i))}
                      className="px-2 text-slate-300 hover:text-rose-500"><Trash2 size={14} /></button>
                  </div>
                ))}
                <button onClick={() => setEntraMapping(prev => [...prev, { group: '', role: 'operator' }])}
                  className="px-3 py-2 bg-slate-100 hover:bg-slate-200 rounded-lg text-xs font-bold text-slate-600 flex items-center gap-1">
                  <Plus size={12} /> Add mapping
                </button>
              </div>
              <p className="text-[10px] text-slate-400 mt-1.5">The most privileged match wins. App roles named admin, operator or viewer are honoured without a mapping.</p>
            </div>

            <label className="flex items-center gap-2 text-xs font-bold text-slate-500 cursor-pointer">
              <input type="checkbox" checked={entraSkipSsl} onChange={e => setEntraSkipSsl(e.target.checked)} />
              Skip TLS verification (only behind SSL-inspecting proxies)
            </label>

            <div className="flex items-center gap-3">
              <button onClick={() => handleTestConnection('entra')} disabled={testing === 'entra' || !entraTenant || !entraClient}
                className="flex items-center gap-2 px-4 py-2 bg-slate-100 hover:bg-slate-200 text-slate-700 rounded-lg text-xs font-bold transition-all disabled:opacity-50">
                {testing === 'entra' ? <RefreshCw size={14} className="animate-spin" /> : <ExternalLink size={14} />}
                Test Connection
              </button>
              {testResult?.provider === 'entra' && (
                <span className={`text-xs font-bold flex items-center gap-1 ${testResult.connected ? 'text-emerald-600' : 'text-rose-500'}`}>
                  {testResult.connected ? <CheckCircle2 size={14} /> : <XCircle size={14} />}
                  {testResult.connected ? testResult.message : testResult.error || 'Connection failed'}
                </span>
              )}
            </div>
          </div>

          <div className="bg-white rounded-xl border border-slate-200 shadow-sm p-6">
            <label className="flex items-start gap-3 cursor-pointer">
              <input type="checkbox" checked={disableLocalLogin} onChange={e => setDisableLocalLogin(e.target.checked)} className="mt-1 accent-brand-600" />
              <span>
                <span className="block text-sm font-bold text-slate-700">Sign in with Microsoft only</span>
                <span className="block text-xs text-slate-500">Hides the password form, and local users can no longer sign in. Only takes effect while Microsoft sign-in is enabled, so nobody is locked out; the built-in admin keeps working through the API as break-glass access.</span>
              </span>
            </label>
          </div>
        </div>
      )}

      {/* ─── Features Section ────────────────────────────────────────────── */}
      {section === 'pricing' && (
        <div className="space-y-4 animate-in fade-in slide-in-from-bottom-2 duration-300">
          <SectionHeader
            icon={<Coins className="text-brand-600" size={20} />}
            title="Prices"
            subtitle="What a core, a GiB of memory, a volume and a load balancer cost"
            action={saveAction('pricing')}
          />

          <div className="bg-white rounded-xl border border-slate-200 shadow-sm p-6 space-y-6">
            {settings?.pricing?.effective && (
              <div className="p-4 rounded-xl bg-slate-50 border border-slate-200 text-xs text-slate-600">
                <span className="font-bold text-slate-800">Rates in effect:</span>{' '}
                {settings.pricing.effective.cpuCoreHour.toFixed(4)} {settings.pricing.effective.currency}/core-hour ·{' '}
                {settings.pricing.effective.memoryGiBHour.toFixed(4)} {settings.pricing.effective.currency}/GiB-hour
                <div className="text-slate-400 mt-1">{settings.pricing.effective.basis}</div>
              </div>
            )}

            <div className="flex items-center justify-between">
              <div>
                <h4 className="font-bold text-slate-800">Cloud list prices</h4>
                <p className="text-sm text-slate-500 mt-1">
                  Price each node at its list price (Linux, by instance type and region) and derive the per-core and per-GiB rates from the real hourly bill.
                  On AWS this uses the Price List API and needs the <code>pricing:GetProducts</code> permission; on Azure the public Retail Prices API, with no credentials.
                  Spot, reservations and savings plans are not applied. Google Cloud clusters keep the estimate.
                </p>
              </div>
              <label className="relative inline-flex items-center cursor-pointer ml-4">
                <input type="checkbox" checked={cloudPricingApi} onChange={e => setCloudPricingApi(e.target.checked)} className="sr-only peer" />
                <div className={`w-11 h-6 rounded-full peer-focus:outline-none peer-focus:ring-4 peer-focus:ring-brand-500/20 transition-colors ${cloudPricingApi ? 'bg-brand-600' : 'bg-slate-300'}`}>
                  <div className={`absolute top-0.5 left-0.5 w-5 h-5 bg-white rounded-full shadow-md transition-transform ${cloudPricingApi ? 'translate-x-5' : 'translate-x-0'}`} />
                </div>
              </label>
            </div>

            <div>
              <h4 className="font-bold text-slate-800">Custom rates</h4>
              <p className="text-sm text-slate-500 mt-1 mb-3">For on-premises clusters or negotiated prices. When both rates are set they override every other source.</p>
              <div className="grid grid-cols-3 gap-3">
                <div>
                  <label className="text-[10px] font-bold uppercase tracking-wider text-slate-500 mb-1 block">Per core-hour</label>
                  <input value={priceCpu} onChange={e => setPriceCpu(e.target.value)} placeholder="0.031" className="w-full px-3 py-2 bg-white border border-slate-200 rounded-lg text-sm font-mono" />
                </div>
                <div>
                  <label className="text-[10px] font-bold uppercase tracking-wider text-slate-500 mb-1 block">Per GiB-hour</label>
                  <input value={priceMem} onChange={e => setPriceMem(e.target.value)} placeholder="0.0042" className="w-full px-3 py-2 bg-white border border-slate-200 rounded-lg text-sm font-mono" />
                </div>
                <div>
                  <label className="text-[10px] font-bold uppercase tracking-wider text-slate-500 mb-1 block">Currency</label>
                  <input value={priceCurrency} onChange={e => setPriceCurrency(e.target.value)} placeholder="USD" maxLength={3} className="w-full px-3 py-2 bg-white border border-slate-200 rounded-lg text-sm font-mono uppercase" />
                </div>
              </div>
              <p className="text-sm text-slate-500 mt-4 mb-3">Volumes and load balancers are priced at the cloud's list price. Without a cloud price list, network volumes are estimated at 0.08 USD a GiB-month and load balancers at nothing. Set your own here, and always when the currency is not USD.</p>
              <div className="grid grid-cols-3 gap-3">
                <div>
                  <label className="text-[10px] font-bold uppercase tracking-wider text-slate-500 mb-1 block">Per GiB-month of volume</label>
                  <input value={priceStorage} onChange={e => setPriceStorage(e.target.value)} placeholder="0.08" className="w-full px-3 py-2 bg-white border border-slate-200 rounded-lg text-sm font-mono" />
                </div>
                <div>
                  <label className="text-[10px] font-bold uppercase tracking-wider text-slate-500 mb-1 block">Per load balancer-month</label>
                  <input value={priceLB} onChange={e => setPriceLB(e.target.value)} placeholder="18" className="w-full px-3 py-2 bg-white border border-slate-200 rounded-lg text-sm font-mono" />
                </div>
              </div>
            </div>
          </div>
        </div>
      )}

      {section === 'billing' && (
        <div className="space-y-4 animate-in fade-in duration-300">
          <SectionHeader
            icon={<Receipt className="text-brand-600" size={20} />}
            title="Cloud bill"
            subtitle="Bring discounts, reservations and spot prices into every figure"
          />
          <div className="bg-white rounded-xl border border-slate-200 shadow-sm p-6">
            <BillingSection onSaved={enabled => setSettings(st => (st ? { ...st, billing: { ...st.billing, enabled } } : st))} />
          </div>
        </div>
      )}

      {section === 'tokens' && (
        <div className="space-y-4 animate-in fade-in duration-300">
          <SectionHeader
            icon={<KeyRound className="text-brand-600" size={20} />}
            title="API tokens"
            subtitle="For MCP clients, CI pipelines and scripts that call the API without a browser"
          />
          <ApiTokens />
        </div>
      )}
        </div>
      </div>
    </div>
  )
}
