import { useState, useCallback, type ReactNode } from 'react'
import {
  Cloud, Bot, MessageSquare, Plus, Trash2, RefreshCw, PieChart, Globe2, Wand2,
  CheckCircle2, XCircle, AlertTriangle, ChevronDown,
  ChevronUp, Sparkles, Activity, Plug, Shield, X, Users as UsersIcon, KeyRound, Coins, Receipt, CalendarDays
} from 'lucide-react'
import { AWSLogo, AzureLogo, GCPLogo, WebexLogo } from '../components/ProviderLogos'
import ApiTokens from '../components/ApiTokens'
import CalendarsEditor from '../components/settings/CalendarsEditor'
import ModelPricesEditor from '../components/settings/ModelPricesEditor'
import OIDCSettings from '../components/settings/OIDCSettings'
import NotificationChannels from '../components/settings/NotificationChannels'
import PreviewCleanup from '../components/settings/PreviewCleanup'
import AllocationSettings from '../components/settings/AllocationSettings'
import ClustersSettings from '../components/settings/ClustersSettings'
import RightsizingSettings from '../components/settings/RightsizingSettings'
import { rightsizingForm, rightsizingProblem, rightsizingRequest, type RightsizingForm, type RightsizingSettings as RightsizingData } from '../lib/rightsizing'
import { clustersForm, clustersProblem, clustersRequest, type ClusterForm, type ClusterSettings } from '../lib/clusters'
import { allocationForm, allocationProblem, allocationRequest, type AllocationForm, type AllocationSettings as AllocationData } from '../lib/allocation'
import { cleanupForm, cleanupProblem, cleanupRequest, type CleanupForm, type TTLSettings } from '../lib/cleanup'
import { CardIcon, checkClass, Configured, Optional, SecretInput, Segmented, SettingsCard, TestButton, Toggle, hintClass, inputClass, labelClass } from '../components/settings/fields'
import { channelsForm, channelsProblem, channelsRequest, type ChannelsForm, type NotificationsSettings } from '../lib/notifications'
import { emptyOIDC, oidcProblem, oidcRequest, type OIDCForm } from '../lib/oidc'
import { llmModelProblem } from '../lib/llm'
import { tokens } from '../lib/finops'
import { formatMoney } from '../lib/format'
import { calendarProblem } from '../lib/exceptions'
import type { HolidayCalendar, LLMModelPrice } from '../lib/types'
import UsersSettings from '../components/settings/UsersSettings'
import { usePolling } from '../lib/usePolling'
import { navigate, segment, useLeaveGuard } from '../lib/router'
import { apiError, errorMessage, getJSON } from '../lib/api'
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
  monthlyTokenBudget?: number
  usage?: { month: string; inputTokens: number; outputTokens: number; requests: number; estimated?: boolean }
  usageCost?: number
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

interface OIDCSettingsData {
  enabled: boolean
  displayName?: string
  issuerUrl?: string
  clientId?: string
  redirectUrl?: string
  scopes?: string[]
  groupsClaim?: string
  rolesClaim?: string
  defaultRole?: string
  autoProvision: boolean
  groupRoleMapping?: Record<string, string>
  skipSslVerify: boolean
  hasClientSecret: boolean
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
  dashboardUrl?: string
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
    notifications?: NotificationsSettings
  }
  features?: {
    cloudPricingApi?: boolean;
  }
  auth?: {
    disableLocalLogin: boolean
    entra?: EntraSettings
    oidc?: OIDCSettingsData
  }
  pricing?: {
    cpuCoreHour?: string
    memoryGiBHour?: string
    storageGiBMonth?: string
    loadBalancerMonth?: string
    gpuHour?: string
    llmModels?: LLMModelPrice[] | null
    currency?: string
    effective: { cpuCoreHour: number; memoryGiBHour: number; gpuHour?: number; currency: string; basis: string }
  }
  billing?: { enabled?: boolean }
  calendars?: HolidayCalendar[]
  namespaceTTL?: TTLSettings
  allocation?: AllocationData
  clusters?: ClusterSettings[]
  rightsizing?: RightsizingData
}

// ─── Navigation ─────────────────────────────────────────────────────────────

type Section = 'users' | 'sso' | 'tokens' | 'pricing' | 'allocation' | 'rightsizing' | 'billing' | 'metrics' | 'calendars' | 'cleanup' | 'clouds' | 'clusters' | 'notifications' | 'ai' | 'mcp'
// Sections whose form is saved with their Save button; the others act at once.
type SavedSection = 'sso' | 'pricing' | 'allocation' | 'rightsizing' | 'metrics' | 'calendars' | 'cleanup' | 'clouds' | 'clusters' | 'notifications' | 'ai' | 'mcp'
const savedSections: Section[] = ['sso', 'pricing', 'allocation', 'rightsizing', 'metrics', 'calendars', 'cleanup', 'clouds', 'clusters', 'notifications', 'ai', 'mcp']
const isSaved = (s: Section): s is SavedSection => savedSections.includes(s)

const navGroups: { title: string; items: { id: Section; label: string; icon: ReactNode }[] }[] = [
  { title: 'People & access', items: [
    { id: 'users', label: 'Users', icon: <UsersIcon size={16} /> },
    { id: 'sso', label: 'Single sign-on', icon: <Shield size={16} /> },
    { id: 'tokens', label: 'API tokens', icon: <KeyRound size={16} /> },
  ] },
  { title: 'Cost data', items: [
    { id: 'pricing', label: 'Prices', icon: <Coins size={16} /> },
    { id: 'allocation', label: 'Cost allocation', icon: <PieChart size={16} /> },
    { id: 'billing', label: 'Cloud bill', icon: <Receipt size={16} /> },
    { id: 'metrics', label: 'Usage metrics', icon: <Activity size={16} /> },
  ] },
  { title: 'Automation', items: [
    { id: 'calendars', label: 'Holiday calendars', icon: <CalendarDays size={16} /> },
    { id: 'cleanup', label: 'Preview cleanup', icon: <Trash2 size={16} /> },
    { id: 'rightsizing', label: 'Right-sizing', icon: <Wand2 size={16} /> },
  ] },
  { title: 'Integrations', items: [
    { id: 'clouds', label: 'Cloud accounts', icon: <Cloud size={16} /> },
    { id: 'clusters', label: 'Clusters', icon: <Globe2 size={16} /> },
    { id: 'notifications', label: 'Notifications', icon: <MessageSquare size={16} /> },
    { id: 'ai', label: 'AI assistant', icon: <Bot size={16} /> },
    { id: 'mcp', label: 'MCP server', icon: <Plug size={16} /> },
  ] },
]
const allSections = navGroups.flatMap(g => g.items.map(i => i.id))
const sectionLabels = Object.fromEntries(navGroups.flatMap(g => g.items.map(i => [i.id, i.label]))) as Record<Section, string>
// Moving between sections keeps every section's input, so only leaving Settings is held.
const staysInSettings = (path: string) => path === '/settings' || path.startsWith('/settings/')

// listOf joins names as prose: "A", "A and B", "A, B and C".
const listOf = (names: string[]) => (names.length > 1 ? `${names.slice(0, -1).join(', ')} and ${names.at(-1)}` : names[0])
const sectionKey = 'costdeck.settings.section'

// lastSection is the section opened last in this browser, shown for a bare /settings.
function lastSection(): Section {
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
  const channels = st.integrations.notifications
  return {
    sso: onOff(st.auth?.entra?.enabled || st.auth?.oidc?.enabled),
    pricing: { text: basis.startsWith('custom') ? 'Custom' : basis.includes('aws') || basis.includes('azure') ? 'List' : 'Estimate' },
    allocation: { text: st.allocation?.sharedNamespaces?.length ? `${st.allocation.sharedNamespaces.length} shared` : 'Default', on: !!st.allocation?.sharedNamespaces?.length || st.allocation?.idle === 'proportional' },
    billing: onOff(st.billing?.enabled),
    metrics: { text: st.integrations.victoriaMetrics?.enabled ? 'VictoriaMetrics' : 'metrics-server' },
    calendars: { text: st.calendars?.length ? String(st.calendars.length) : 'None', on: !!st.calendars?.length },
    rightsizing: { text: st.rightsizing?.apply ? (st.rightsizing.git ? 'Apply, PRs' : 'Apply') : st.rightsizing?.git ? 'PRs' : 'Advice', on: !!st.rightsizing?.apply || !!st.rightsizing?.git },
    cleanup: { text: !st.namespaceTTL?.enabled ? 'Off' : st.namespaceTTL.dryRun ? 'Dry run' : 'On', on: !!st.namespaceTTL?.enabled },
    clouds: { text: clouds.length ? clouds.join(', ') : 'None', on: clouds.length > 0 },
    clusters: { text: st.clusters?.length ? String(st.clusters.length + 1) : 'One', on: !!st.clusters?.length },
    notifications: onOff(st.integrations.messenger?.webex?.enabled || channels?.slack.enabled || channels?.teams.enabled || channels?.email.enabled),
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

const billingClouds = [['aws', 'AWS'], ['azure', 'Azure'], ['gcp', 'Google Cloud']] as const

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

  const load = () => getJSON<{ billing?: BillingSettings }>('/api/settings').then(d => {
    const bs: BillingSettings = d.billing || {}
    setB(bs)
    setCloud(bs.azure ? 'azure' : bs.gcp ? 'gcp' : 'aws')
  }).catch(() => setB({}))
  usePolling(load, null)

  if (!b) return null
  const st = b.status
  const set = (patch: Partial<BillingSettings>) => setB({ ...b, ...patch })
  const field = `${inputClass} font-mono`

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
      <div className="flex items-center justify-between gap-4">
        <div>
          <h4 className="font-bold text-slate-800">Reconcile with the cloud bill</h4>
          <p className="text-sm text-slate-500 mt-1">
            Compares a week of the real bill for the cluster's nodes with list prices and scales every compute cost by the ratio, so Savings Plans,
            Reserved Instances, committed use discounts and spot prices show in every figure. Checked every six hours; the bill lags by two days.
          </p>
        </div>
        <Toggle checked={!!b.enabled} onChange={v => set({ enabled: v })} label="Enable Reconcile with the cloud bill" />
      </div>
      <div className="mt-4">
        <Segmented label="Cloud" value={cloud} onChange={setCloud} options={billingClouds} />
      </div>
      <p className="mt-2 text-xs text-slate-500">{billingHelp[cloud]} It uses the {billingClouds.find(([c]) => c === cloud)![1]} credentials under <b>Cloud accounts</b>, or the pod identity.</p>
      <div className="mt-3 grid grid-cols-2 gap-3">
        {cloud === 'aws' && <>
          <div><label className={labelClass}>Tag key</label>
            <input className={field} value={b.aws?.tagKey || 'aws:eks:cluster-name'} onChange={e => set({ aws: { tagValue: b.aws?.tagValue || '', tagKey: e.target.value } })} /></div>
          <div><label className={labelClass}>Tag value (cluster name)</label>
            <input className={field} value={b.aws?.tagValue || ''} placeholder="prod-eu" onChange={e => set({ aws: { tagKey: b.aws?.tagKey, tagValue: e.target.value } })} /></div>
        </>}
        {cloud === 'azure' && (
          <div className="col-span-2"><label className={labelClass}>Node resource group</label>
            <input className={field} value={b.azure?.resourceGroup || ''} placeholder="MC_rg-prod_aks-prod_westeurope" onChange={e => set({ azure: { resourceGroup: e.target.value } })} /></div>
        )}
        {cloud === 'gcp' && <>
          <div><label className={labelClass}>Billing export table</label>
            <input className={field} value={b.gcp?.table || ''} placeholder="billing-project.billing.gcp_billing_export_resource_v1_XXXX" onChange={e => set({ gcp: { clusterName: b.gcp?.clusterName || '', table: e.target.value } })} /></div>
          <div><label className={labelClass}>GKE cluster name</label>
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
          className={`${inputClass} flex-1 font-mono`}
          onKeyDown={e => e.key === 'Enter' && addTag()}
        />
        <input
          value={newValue}
          onChange={e => setNewValue(e.target.value)}
          placeholder="Tag value"
          className={`${inputClass} flex-1 font-mono`}
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
  <label className={labelClass}>{children}{done && <Configured />}</label>
)

type TestResult = { provider: string; connected: boolean; error?: string; message?: string } | null

// outcome is what TestButton shows for one provider's last connection test.
const outcome = (result: TestResult, provider: string, connected = 'Connection successful') =>
  result?.provider === provider ? { ok: result.connected, text: result.connected ? result.message || connected : result.error || 'Connection failed' } : null

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
            <span className="text-xs text-slate-400">{status.discoveredResources} resources discovered</span>
          )}
        </div>
      </div>
      <div className="flex items-center gap-3">
        <span className="contents" onClick={e => e.stopPropagation()}>
          <Toggle checked={enabled} onChange={onToggle} label={`Enable ${name}`} disabled={comingSoon} />
        </span>
        {expanded ? <ChevronUp size={18} className="text-slate-400" /> : <ChevronDown size={18} className="text-slate-400" />}
      </div>
    </div>
    {expanded && !comingSoon && (
      <div className="px-5 pb-5 pt-2 border-t border-slate-100 animate-drop">
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

// SettingsPage shows the section named in the path (/settings/<section>).
export default function SettingsPage({ section: requested }: { section?: string }) {
  const [settings, setSettings] = useState<SettingsData | null>(null)
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [testing, setTesting] = useState<string | null>(null)
  const [testResult, setTestResult] = useState<TestResult>(null)
  const [saveMessage, setSaveMessage] = useState<{ section: Section; text: string; ok: boolean } | null>(null)
  const [expandedProvider, setExpandedProvider] = useState<string | null>('aws')
  const section = requested && allSections.includes(requested as Section) ? requested as Section : lastSection()
  const [dirty, setDirty] = useState<Partial<Record<Section, boolean>>>({})
  const unsaved = allSections.filter(s => dirty[s]).map(s => sectionLabels[s])
  useLeaveGuard(unsaved.length ? listOf(unsaved) : null, staysInSettings)
  const go = (s: Section) => {
    try { localStorage.setItem(sectionKey, s) } catch { /* not remembered */ }
    navigate(`/settings/${segment(s)}`)
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
  const [calendars, setCalendars] = useState<HolidayCalendar[]>([])
  const [priceCpu, setPriceCpu] = useState('')
  const [priceMem, setPriceMem] = useState('')
  const [priceCurrency, setPriceCurrency] = useState('')
  const [priceStorage, setPriceStorage] = useState('')
  const [priceLB, setPriceLB] = useState('')
  const [priceGPU, setPriceGPU] = useState('')
  const [llmModels, setLLMModels] = useState<LLMModelPrice[]>([])
  const [aiApiKey, setAiApiKey] = useState('')
  const [aiSkipSslVerify, setAiSkipSslVerify] = useState(false)
  const [aiBudget, setAiBudget] = useState('')
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
  const [oidc, setOIDC] = useState<OIDCForm>(emptyOIDC)
  const [channels, setChannels] = useState<ChannelsForm>(() => channelsForm())
  const [cleanup, setCleanup] = useState<CleanupForm>(() => cleanupForm())
  const [allocation, setAllocation] = useState<AllocationForm>(() => allocationForm())
  const [clusters, setClusters] = useState<ClusterForm[]>([])
  const [rightsizing, setRightsizing] = useState<RightsizingForm>(() => rightsizingForm())
  const [dashboardUrl, setDashboardUrl] = useState('')
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
          setAiBudget(data.integrations.ai.monthlyTokenBudget ? String(data.integrations.ai.monthlyTokenBudget) : '')
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
        setCalendars(data.calendars || [])
        if (data.pricing) {
          setPriceCpu(data.pricing.cpuCoreHour || '')
          setPriceMem(data.pricing.memoryGiBHour || '')
          setPriceCurrency(data.pricing.currency || '')
          setPriceStorage(data.pricing.storageGiBMonth || '')
          setPriceLB(data.pricing.loadBalancerMonth || '')
          setPriceGPU(data.pricing.gpuHour || '')
          setLLMModels(data.pricing.llmModels || [])
        }
        setChannels(channelsForm(data.integrations?.notifications))
        setCleanup(cleanupForm(data.namespaceTTL))
        setAllocation(allocationForm(data.allocation))
        setClusters(clustersForm(data.clusters))
        setRightsizing(rightsizingForm(data.rightsizing))
        if (data.auth) {
          setDisableLocalLogin(data.auth.disableLocalLogin)
          setDashboardUrl(data.dashboardUrl || '')
          const o = data.auth.oidc
          if (o) {
            setOIDC({
              enabled: o.enabled, displayName: o.displayName || '', issuerUrl: o.issuerUrl || '', clientId: o.clientId || '', clientSecret: '',
              hasClientSecret: o.hasClientSecret, redirectUrl: o.redirectUrl || '', scopes: (o.scopes || []).join(' '), groupsClaim: o.groupsClaim || '',
              rolesClaim: o.rolesClaim || '', defaultRole: o.defaultRole || 'viewer', autoProvision: o.autoProvision, skipSslVerify: o.skipSslVerify,
              mapping: Object.entries(o.groupRoleMapping || {}).map(([group, role]) => ({ group, role })),
            })
          }
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
    const invalid = sec === 'calendars' ? calendarProblem(calendars) : sec === 'pricing' ? llmModelProblem(llmModels) : sec === 'sso' ? oidcProblem(oidc)
      : sec === 'notifications' ? channelsProblem(channels) : sec === 'cleanup' ? cleanupProblem(cleanup, settings?.namespaceTTL)
      : sec === 'allocation' ? allocationProblem(allocation) : sec === 'clusters' ? clustersProblem(clusters)
      : sec === 'rightsizing' ? rightsizingProblem(rightsizing) : null
    if (invalid) {
      setSaveMessage({ section: sec, text: `Not saved: ${invalid}`, ok: false })
      return
    }
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
            monthlyTokenBudget: Number(aiBudget) || 0,
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
          gpuHour: priceGPU,
          llmModels: llmModels.map(m => ({ model: m.model.trim(), inputPer1M: m.inputPer1M.trim(), outputPer1M: m.outputPer1M.trim() })),
          currency: priceCurrency,
        },
        auth: {
          disableLocalLogin,
          oidc: oidcRequest(oidc),
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
        notifications: { integrations: { messenger: full.integrations.messenger, notifications: channelsRequest(channels) } },
        metrics: { integrations: { victoriaMetrics: full.integrations.victoriaMetrics } },
        mcp: { integrations: { mcp: full.integrations.mcp } },
        pricing: { features: full.features, pricing: full.pricing },
        sso: { auth: full.auth, dashboardUrl: dashboardUrl.trim() },
        calendars: { calendars },
        cleanup: { namespaceTTL: cleanupRequest(cleanup) },
        allocation: { allocation: allocationRequest(allocation) },
        clusters: { clusters: clustersRequest(clusters) },
        rightsizing: { rightsizing: rightsizingRequest(rightsizing) },
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
        if (sec === 'calendars') setCalendars(data.calendars || [])
        if (sec === 'notifications') setChannels(channelsForm(data.integrations?.notifications))
        if (sec === 'cleanup') setCleanup(cleanupForm(data.namespaceTTL))
        if (sec === 'allocation') setAllocation(allocationForm(data.allocation))
        if (sec === 'clusters') setClusters(clustersForm(data.clusters))
        if (sec === 'rightsizing') setRightsizing(rightsizingForm(data.rightsizing))
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

  const webexStatus = settings?.integrations?.messenger?.webex?.status
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
        <div className="space-y-4 animate-rise">
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
        <div className="space-y-4 animate-rise">
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
              <div>
                <FieldLabel>Region</FieldLabel>
                <select value={awsRegion} onChange={e => setAwsRegion(e.target.value)} className={`${inputClass} sm:w-96`}>
                  {awsRegions.map(r => <option key={r} value={r}>{r}</option>)}
                </select>
              </div>
              <div>
                <FieldLabel done={settings?.providers.aws?.hasCredentials}>Access key</FieldLabel>
                <div className="grid gap-2 sm:grid-cols-2">
                  <SecretInput value={awsAccessKey} onChange={setAwsAccessKey} label="AWS access key ID"
                    placeholder={settings?.providers.aws?.hasCredentials ? '••••••••••••••••' : 'Access key ID'} />
                  <SecretInput value={awsSecretKey} onChange={setAwsSecretKey} label="AWS secret access key"
                    placeholder={settings?.providers.aws?.hasCredentials ? '••••••••••••••••' : 'Secret access key'} />
                </div>
                <p className={hintClass}>Leave empty to keep the stored key, or to use the pod identity (IRSA or EKS Pod Identity). Keys are stored in a Kubernetes Secret.</p>
              </div>
              <TestButton onTest={() => handleTestConnection('aws')} testing={testing === 'aws'} result={outcome(testResult, 'aws')} />
              <div>
                <FieldLabel>Discovery tags</FieldLabel>
                <p className="text-xs text-slate-400 mb-2">Only resources carrying all of these tags are offered for scheduling.</p>
                <TagEditor tags={awsTags} onChange={setAwsTags} />
              </div>
              <div>
                <FieldLabel>Resource types</FieldLabel>
                <ResourceTypePicker value={awsResourceTypes} onChange={setAwsResourceTypes} options={[
                  { id: 'aurora', label: 'Aurora clusters', desc: 'Start and stop RDS Aurora clusters' },
                  { id: 'ec2', label: 'EC2 instances', desc: 'Start and stop EC2 instances' },
                ]} />
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
                  <input value={azureSubscription} onChange={e => setAzureSubscription(e.target.value.trim())} placeholder="00000000-0000-0000-0000-000000000000" className={`${inputClass} font-mono`} />
                </div>
                <div>
                  <FieldLabel>Tenant ID</FieldLabel>
                  <input value={azureTenant} onChange={e => setAzureTenant(e.target.value.trim())} placeholder="Directory (tenant) ID" className={`${inputClass} font-mono`} />
                </div>
              </div>
              <div>
                <FieldLabel done={settings?.providers.azure?.hasCredentials}>Service principal</FieldLabel>
                <div className="grid gap-2 sm:grid-cols-2">
                  <input value={azureClientId} onChange={e => setAzureClientId(e.target.value.trim())} placeholder="Application (client) ID" className={`${inputClass} font-mono`} />
                  <SecretInput value={azureClientSecret} onChange={setAzureClientSecret}
                    placeholder={settings?.providers.azure?.hasCredentials ? '••••••••••••••••' : 'Client secret'} />
                </div>
                <p className="text-xs text-slate-400 mt-1.5">
                  Leave empty to keep the stored secret, or to use the pod identity (AKS workload identity or a managed identity).
                  The identity needs <code>Microsoft.Compute/virtualMachines/start|deallocate</code> and the flexible servers' <code>start|stop</code> actions; Virtual Machine Contributor covers VMs.
                </p>
              </div>
              <TestButton onTest={() => handleTestConnection('azure')} testing={testing === 'azure'} result={outcome(testResult, 'azure')} />
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
                <input value={gcpProject} onChange={e => setGcpProject(e.target.value.trim())} placeholder="Defaults to the service account's project" className={`${inputClass} font-mono sm:w-96`} />
              </div>
              <div>
                <FieldLabel done={settings?.providers.gcp?.hasCredentials}>Service account key</FieldLabel>
                <textarea value={gcpKey} onChange={e => setGcpKey(e.target.value)} rows={4} spellCheck={false}
                  placeholder={settings?.providers.gcp?.hasCredentials ? 'Stored. Paste a new key to replace it.' : 'Paste the JSON key of a service account'}
                  className={`${inputClass} font-mono text-xs`} />
                <p className="text-xs text-slate-400 mt-1.5">
                  Leave empty to keep the stored key, or to use GKE workload identity. The account needs <code>roles/compute.instanceAdmin.v1</code> and <code>roles/cloudsql.editor</code> (or narrower custom roles).
                </p>
              </div>
              <TestButton onTest={() => handleTestConnection('gcp')} testing={testing === 'gcp'} result={outcome(testResult, 'gcp')} />
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
        <div className="space-y-4 animate-rise">
          <SectionHeader
            icon={<Activity className="text-brand-600" size={20} />}
            title="Usage metrics"
            subtitle="Where namespace usage and right-sizing advice come from"
            action={saveAction('metrics')}
          />

          <SettingsCard
            logo={<CardIcon><Activity size={20} /></CardIcon>}
            title="VictoriaMetrics"
            subtitle="Usage history for namespace charts, right-sizing advice and schedule suggestions. Off: metrics-server, which keeps only the last hour."
            status={vmEnabled && settings?.integrations.victoriaMetrics?.status && (
              <span title={settings.integrations.victoriaMetrics.status.error || ''}>
                <StatusBadge connected={settings.integrations.victoriaMetrics.status.connected} error={settings.integrations.victoriaMetrics.status.error} />
              </span>
            )}
            enabled={vmEnabled}
            onToggle={setVmEnabled}
            footer={vmEndpoint && <>Changes apply at once, with no restart. While VictoriaMetrics cannot be reached, Cost Deck falls back to metrics-server and says why.</>}
          >
            <div>
              <label className={labelClass} htmlFor="vm-endpoint">Endpoint URL</label>
              <input id="vm-endpoint" value={vmEndpoint} onChange={e => setVmEndpoint(e.target.value)}
                placeholder="http://vmselect.monitoring.svc:8481/select/0/prometheus" className={`${inputClass} font-mono`} />
              <p className={hintClass}>Single node: <code>http://vmsingle.monitoring.svc:8428</code>; cluster: <code>http://vmselect.monitoring.svc:8481/select/0/prometheus</code>. Prometheus works too.</p>
            </div>

            <div>
              <label className={labelClass} htmlFor="vm-selector">Cluster label selector <Optional>(optional)</Optional></label>
              <input id="vm-selector" value={vmLabelSelector} onChange={e => setVmLabelSelector(e.target.value)} placeholder='cluster="prod-eu"' className={`${inputClass} font-mono`} />
              <p className={hintClass}>Needed when one VictoriaMetrics stores several clusters; otherwise namespaces with the same name are added up across them.</p>
            </div>

            <div>
              <label className={labelClass} htmlFor="vm-days">Lookback window</label>
              <div className="flex items-center gap-3">
                <input id="vm-days" type="number" min={1} max={90} value={vmRetentionDays} onChange={e => setVmRetentionDays(parseInt(e.target.value) || 7)} className={`${inputClass} !w-24 text-center`} />
                <span className="text-xs text-slate-400">days of history in the usage charts and behind right-sizing advice (advice uses at most 14)</span>
              </div>
            </div>

            <div>
              <label className={labelClass}>Authentication <Optional>(optional)</Optional></label>
              <div className="mb-3">
                <Segmented label="Authentication" value={vmAuthMode} onChange={setVmAuthMode} options={[['bearer', 'Bearer token'], ['basic', 'Basic auth']]} />
              </div>
              {vmAuthMode === 'bearer' ? (
                <SecretInput value={vmBearerToken} onChange={setVmBearerToken} label="Bearer token" placeholder="Bearer token" />
              ) : (
                <div className="grid grid-cols-2 gap-3">
                  <input value={vmUsername} onChange={e => setVmUsername(e.target.value)} aria-label="Username" placeholder="Username" className={inputClass} />
                  <SecretInput value={vmPassword} onChange={setVmPassword} label="Password" placeholder="Password" />
                </div>
              )}
              <p className={hintClass}>Leave empty when VictoriaMetrics needs no authentication.</p>
            </div>

            <div>
              <label className={labelClass} htmlFor="vm-ca">Custom CA <Optional>(optional, PEM)</Optional></label>
              <textarea id="vm-ca" value={vmCaCert} onChange={e => setVmCaCert(e.target.value)} rows={3}
                placeholder="-----BEGIN CERTIFICATE-----" className={`${inputClass} font-mono text-xs`} />
              <label className={`${checkClass} mt-2`}>
                <input type="checkbox" checked={vmSkipSsl} onChange={e => setVmSkipSsl(e.target.checked)} className="accent-brand-600" />
                Skip TLS verification (insecure; prefer a custom CA)
              </label>
            </div>

            <TestButton onTest={() => handleTestConnection('victoriametrics')} testing={testing === 'victoriametrics'} disabled={!vmEndpoint}
              result={outcome(testResult, 'victoriametrics', 'Connected: container metrics found')} />
          </SettingsCard>
        </div>
      )}

      {/* ─── AI Models Section ───────────────────────────────────────────── */}
      {section === 'ai' && (
        <div className="space-y-4 animate-rise">
          <SectionHeader
            icon={<Bot className="text-brand-500" size={20} />}
            title="AI assistant"
            subtitle="The model behind the assistant and the cost reports"
            action={saveAction('ai')}
          />

          <SettingsCard
            logo={<CardIcon><Sparkles size={20} /></CardIcon>}
            title="AI assistant"
            subtitle="The chat, AI reports and explanations of cost changes"
            enabled={aiEnabled}
            onToggle={setAiEnabled}
            footer={<>The assistant reads live cluster data through tools; changes it proposes run only after a user with the operator role confirms them.</>}
          >
              <div className="grid grid-cols-2 gap-4">
                <div>
                  <label className={labelClass}>Provider</label>
                  <select
                    value={aiProvider}
                    onChange={e => {
                      setAiProvider(e.target.value)
                      setAiModel('')
                      setAiModels([])
                    }}
                    className={inputClass}
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
                      className="normal-case tracking-normal text-xs font-semibold text-brand-600 hover:text-brand-800 disabled:opacity-50">
                      {aiModelsLoading ? 'Loading…' : 'Load available models'}
                    </button>
                  </label>
                  <input
                    list="ai-model-options"
                    value={aiModel}
                    onChange={e => setAiModel(e.target.value)}
                    placeholder={DEFAULT_MODEL[aiProvider] ? `${DEFAULT_MODEL[aiProvider]} (default)` : 'e.g. llama3.1'}
                    className={`${inputClass} font-mono`}
                  />
                  <datalist id="ai-model-options">
                    {(aiModels.length > 0 ? aiModels : MODEL_SUGGESTIONS[aiProvider] || []).map(m => <option key={m} value={m} />)}
                  </datalist>
                  {aiModelsError && <p className="mt-1.5 text-xs text-rose-600">{aiModelsError}</p>}
                  {aiModels.length > 0 && <p className={hintClass}>{aiModels.length} models available to this key</p>}
                </div>
              </div>

              <div>
                <label className={labelClass}>API key {settings?.integrations.ai?.hasCredentials && <Configured />}</label>
                <SecretInput value={aiApiKey} onChange={setAiApiKey} label="API key" placeholder={settings?.integrations.ai?.hasCredentials ? '•••••••• (leave empty to keep it)' : 'API key'} />
                <p className={hintClass}>{aiProvider === 'local' ? 'Leave empty if the endpoint needs no authentication.' : 'Stored in a Kubernetes Secret and never returned to the browser.'}</p>
              </div>

              <div className="grid grid-cols-2 gap-4">
                <div>
                  <label className={labelClass}>
                    API base URL <Optional>({aiProvider === 'local' ? 'required' : 'optional: gateways and proxies'})</Optional>
                  </label>
                  <input
                    value={aiBaseUrl}
                    onChange={e => setAiBaseUrl(e.target.value)}
                    placeholder={aiProvider === 'local' ? 'http://ollama.ai.svc:11434/v1' : aiProvider === 'anthropic' ? 'https://api.anthropic.com' : aiProvider === 'gemini' ? 'https://generativelanguage.googleapis.com/v1beta' : 'https://api.openai.com/v1'}
                    className={`${inputClass} font-mono`}
                  />
                </div>
                <label className={`${checkClass} mt-6`}>
                  <input type="checkbox" checked={aiSkipSslVerify} onChange={e => setAiSkipSslVerify(e.target.checked)} className="accent-brand-600" />
                  Skip TLS verification (insecure)
                </label>
              </div>

              <div data-testid="ai-budget">
                <label htmlFor="ai-budget" className={labelClass}>Monthly token budget</label>
                <div className="flex flex-wrap items-center gap-3">
                  <input id="ai-budget" type="number" min={0} step={10000} value={aiBudget} onChange={e => setAiBudget(e.target.value)} placeholder="no limit"
                    className={`${inputClass} font-mono !w-40`} />
                  {settings?.integrations.ai?.usage && (
                    <span className="text-xs text-slate-500">
                      {(() => {
                        const u = settings.integrations.ai.usage!
                        const used = u.inputTokens + u.outputTokens
                        const budget = settings.integrations.ai.monthlyTokenBudget || 0
                        return <>Used {tokens(used)}{budget ? ` of ${tokens(budget)} (${Math.round((used / budget) * 100)}%)` : ''} tokens in {u.month} over {u.requests} request{u.requests === 1 ? '' : 's'}
                          {settings.integrations.ai.usageCost !== undefined && <>, about {formatMoney(settings.integrations.ai.usageCost, settings.pricing?.effective.currency || 'USD')}</>}
                          {u.estimated && ' (partly estimated: the provider does not report usage)'}.</>
                      })()}
                    </span>
                  )}
                </div>
                <p className={hintClass}>The assistant and AI reports refuse new requests once the month's tokens are used up. Empty means no limit.</p>
              </div>

              <TestButton onTest={() => handleTestConnection('ai')} testing={testing === 'ai'} result={outcome(testResult, 'ai')} />
          </SettingsCard>
        </div>
      )}

      {/* ─── Messengers Section ──────────────────────────────────────────── */}
      {section === 'allocation' && (
        <div className="space-y-4 animate-rise">
          <SectionHeader
            icon={<PieChart className="text-brand-500" size={20} />}
            title="Cost allocation"
            subtitle="How cost that belongs to no team is charged in showback and chargeback (Reports → Cost allocation)"
            action={saveAction('allocation')}
          />
          <AllocationSettings value={allocation} onChange={setAllocation} />
        </div>
      )}

      {section === 'rightsizing' && (
        <div className="space-y-4 animate-rise">
          <SectionHeader
            icon={<Wand2 className="text-brand-500" size={20} />}
            title="Right-sizing"
            subtitle="What operators can do with right-sizing advice besides reading it: apply it, or open a pull request with it"
            action={saveAction('rightsizing')}
          />
          <RightsizingSettings value={rightsizing} onChange={r => { setRightsizing(r); setDirty(d => ({ ...d, rightsizing: true })) }} />
        </div>
      )}

      {section === 'cleanup' && (
        <div className="space-y-4 animate-rise">
          <SectionHeader
            icon={<Trash2 className="text-brand-500" size={20} />}
            title="Preview cleanup"
            subtitle="Delete the namespaces of preview environments some time after they were created"
            action={saveAction('cleanup')}
          />
          <PreviewCleanup value={cleanup} onChange={setCleanup} saved={settings?.namespaceTTL} />
        </div>
      )}

      {section === 'clusters' && (
        <div className="space-y-4 animate-rise">
          <SectionHeader
            icon={<Globe2 className="text-brand-500" size={20} />}
            title="Clusters"
            subtitle="Other Cost Deck installations the Clusters page shows next to this one"
            action={saveAction('clusters')}
          />
          <ClustersSettings value={clusters} onChange={c => { setClusters(c); setDirty(d => ({ ...d, clusters: true })) }} />
        </div>
      )}

      {section === 'notifications' && (
        <div className="space-y-4 animate-rise">
          <SectionHeader
            icon={<MessageSquare className="text-brand-500" size={20} />}
            title="Notifications"
            subtitle="Where alerts, digests and scaling changes are posted, and the chat bot that answers there"
            action={saveAction('notifications')}
          />

          <SettingsCard
            logo={<WebexLogo />}
            title="Cisco Webex"
            subtitle={webexEnabled && webexStatus ? (webexStatus.connected ? webexStatus.message : webexStatus.error) : 'A chat bot that answers questions and scales from a space'}
            status={webexEnabled && webexStatus && <StatusBadge connected={webexStatus.connected} error={webexStatus.error} />}
            enabled={webexEnabled}
            onToggle={setWebexEnabled}
            footer={<>Mention the bot with <code>help</code>, <code>list</code>, <code>scale group &lt;name&gt; up [for 4h]</code> or <code>resume group &lt;name&gt;</code>.</>}
          >
            <div>
              <label className={labelClass}>Bot token {settings?.integrations?.messenger?.webex?.hasCredentials && <Configured />}</label>
              <SecretInput value={webexBotToken} onChange={setWebexBotToken} label="Webex bot token"
                placeholder={settings?.integrations?.messenger?.webex?.hasCredentials ? '•••••••• (leave empty to keep it)' : 'The access token of a Webex bot'} />
            </div>

            <div>
              <label className={labelClass} htmlFor="webex-space">Space ID <Optional>(needed to scale from chat)</Optional></label>
              <input id="webex-space" value={webexRoomId} onChange={e => setWebexRoomId(e.target.value)} placeholder="The space whose members may scale" className={`${inputClass} font-mono`} />
              <p className={hintClass}>
                Scaling commands are accepted only in this space, so its members are who may scale. Without it the bot answers <code>list</code> and <code>status</code> but changes nothing: anyone on Webex can message a bot directly.
              </p>
            </div>

            <label className={checkClass}>
              <input type="checkbox" checked={webexNotify} onChange={e => setWebexNotify(e.target.checked)} className="accent-brand-600" />
              Announce scaling changes in this space, with who made them and the estimated savings
            </label>

            <div>
              <label className={labelClass}>Webhook secret <Optional>(optional: signed webhooks instead of polling)</Optional></label>
              <SecretInput value={webexWebhookSecret} onChange={setWebexWebhookSecret} label="Webex webhook secret" placeholder="The secret the webhook was registered with" />
              <p className={hintClass}>
                Register a webhook for <code>messages:created</code> pointing to <code>https://&lt;costdeck-host&gt;/api/webex/webhook</code> with this secret. Without it, Cost Deck polls Webex every 10 seconds.
              </p>
            </div>

            <TestButton onTest={() => handleTestConnection('webex')} testing={testing === 'webex'} result={outcome(testResult, 'webex')} />
          </SettingsCard>

          <NotificationChannels value={channels} onChange={setChannels} />
        </div>
      )}

      {/* ─── MCP Server Section ────────────────────────────────────────────── */}
      {section === 'mcp' && (
        <div className="space-y-4 animate-rise">
          <SectionHeader
            icon={<Plug className="text-brand-600" size={20} />}
            title="MCP server"
            subtitle="Expose Cost Deck's data and actions as tools to AI assistants such as Claude or Cursor"
            action={saveAction('mcp')}
          />

          <SettingsCard
            logo={<CardIcon><Plug size={20} /></CardIcon>}
            title="MCP server"
            subtitle="Lets AI clients such as Claude Code or Cursor read cost data, and with an operator token scale and right-size"
            enabled={mcpEnabled}
            onToggle={setMcpEnabled}
          >
              <div>
                <label className={labelClass}>Endpoint</label>
                <code className="block px-3 py-2 bg-slate-50 border border-slate-200 rounded-lg text-sm font-mono">{window.location.origin}/mcp</code>
                <p className={hintClass}>Streamable HTTP on the dashboard's own address, so no extra port or ingress. Clients sign in with an API token (<b>Settings → API tokens</b>): viewer tokens see read-only tools, operator tokens can also scale and right-size.</p>
              </div>

              <div className="bg-brand-50 border border-brand-100 rounded-xl p-4 space-y-3">
                <div>
                  <h4 className="text-sm font-bold text-brand-800 mb-1">Cursor / any client with remote MCP support</h4>
                  <pre className="theme-keep bg-slate-900 rounded-lg p-3 overflow-x-auto text-xs text-slate-200">{`{
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
                  <pre className="theme-keep bg-slate-900 rounded-lg p-3 overflow-x-auto text-xs text-slate-200">{`claude mcp add --transport http costdeck ${window.location.origin}/mcp \
  --header "Authorization: Bearer cdk_..."`}</pre>
                </div>
              </div>
          </SettingsCard>
        </div>
      )}

      {/* ─── Single sign-on ──────────────────────────────────────────────── */}
      {section === 'sso' && (
        <div className="space-y-4 animate-rise">
          <SectionHeader
            icon={<Shield className="text-brand-600" size={20} />}
            title="Single sign-on"
            subtitle="Let people sign in with Microsoft Entra ID, Keycloak or another OpenID Connect provider; their groups decide their role"
            action={saveAction('sso')}
          />

          <SettingsCard
            logo={<CardIcon><Shield size={20} /></CardIcon>}
            title="Microsoft Entra ID"
            subtitle="OpenID Connect, authorization code flow with PKCE"
            enabled={entraEnabled}
            onToggle={setEntraEnabled}
          >
            <div className="p-4 bg-slate-50 border border-slate-200 rounded-lg text-xs text-slate-600 space-y-1.5">
              <p className="font-semibold">Redirect URI to add to the app registration in the Azure portal, one of:</p>
              <p><code className="bg-white px-1.5 py-0.5 rounded">{window.location.origin}/api/auth/entra/callback</code> (web platform, recommended)</p>
              <p><code className="bg-white px-1.5 py-0.5 rounded">{window.location.origin}/auth/callback</code> (single-page application)</p>
              <p className="text-slate-500">Add the optional <b>groups</b> claim (Token configuration → Security groups) or define app roles named admin, operator and viewer.</p>
            </div>

            <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
              <div>
                <label className={labelClass} htmlFor="entra-tenant">Directory (tenant) ID</label>
                <input id="entra-tenant" value={entraTenant} onChange={e => setEntraTenant(e.target.value)} placeholder="00000000-0000-0000-0000-000000000000" className={`${inputClass} font-mono`} />
              </div>
              <div>
                <label className={labelClass} htmlFor="entra-client">Application (client) ID</label>
                <input id="entra-client" value={entraClient} onChange={e => setEntraClient(e.target.value)} placeholder="00000000-0000-0000-0000-000000000000" className={`${inputClass} font-mono`} />
              </div>
            </div>

            <div>
              <label className={labelClass}>Client secret {settings?.auth?.entra?.hasClientSecret && <Configured />}</label>
              <SecretInput value={entraSecret} onChange={setEntraSecret} label="Entra client secret"
                placeholder={settings?.auth?.entra?.hasClientSecret ? '•••••••• (leave empty to keep it)' : 'Client secret value'} />
              <p className={hintClass}>Stored in a Kubernetes Secret and never returned to the browser.</p>
            </div>

            <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
              <div>
                <label className={labelClass} htmlFor="entra-redirect">Redirect URI <Optional>(optional)</Optional></label>
                <input id="entra-redirect" value={entraRedirect} onChange={e => setEntraRedirect(e.target.value)} placeholder={`${window.location.origin}/api/auth/entra/callback`} className={`${inputClass} font-mono`} />
              </div>
              <div>
                <label className={labelClass} htmlFor="entra-authority">Authority <Optional>(sovereign clouds)</Optional></label>
                <input id="entra-authority" value={entraAuthority} onChange={e => setEntraAuthority(e.target.value)} placeholder="https://login.microsoftonline.com" className={`${inputClass} font-mono`} />
              </div>
            </div>

            <div className="grid grid-cols-1 sm:grid-cols-2 gap-4 items-end">
              <div>
                <label className={labelClass} htmlFor="entra-role">Role without a match</label>
                <select id="entra-role" value={entraDefaultRole} onChange={e => setEntraDefaultRole(e.target.value)} className={inputClass}>
                  <option value="viewer">Viewer: read only</option>
                  <option value="operator">Operator: scale and override schedules</option>
                  <option value="admin">Admin: full access</option>
                </select>
              </div>
              <label className={`${checkClass} pb-2`}>
                <input type="checkbox" checked={entraAutoProvision} onChange={e => setEntraAutoProvision(e.target.checked)} className="accent-brand-600" />
                Let anyone in the tenant use Cost Deck
              </label>
            </div>

            <div>
              <label className={labelClass}>Group → role mapping</label>
              <div className="space-y-2">
                {entraMapping.map((m, i) => (
                  <div key={i} className="flex items-center gap-2">
                    <input value={m.group} placeholder="Entra group object ID" aria-label="Group"
                      onChange={e => setEntraMapping(prev => prev.map((x, j) => j === i ? { ...x, group: e.target.value } : x))}
                      className={`${inputClass} flex-1 font-mono`} />
                    <select value={m.role} aria-label="Role"
                      onChange={e => setEntraMapping(prev => prev.map((x, j) => j === i ? { ...x, role: e.target.value } : x))}
                      className={`${inputClass} !w-36`}>
                      <option value="viewer">viewer</option>
                      <option value="operator">operator</option>
                      <option value="admin">admin</option>
                    </select>
                    <button type="button" title="Remove mapping" onClick={() => setEntraMapping(prev => prev.filter((_, j) => j !== i))}
                      className="p-1 text-slate-400 hover:text-rose-600"><X size={14} /></button>
                  </div>
                ))}
              </div>
              <Button size="sm" variant="secondary" icon={<Plus size={13} />} className="mt-2" onClick={() => setEntraMapping(prev => [...prev, { group: '', role: 'operator' }])}>Add a group</Button>
              <p className={hintClass}>The most privileged match wins. App roles named admin, operator or viewer count without a mapping.</p>
            </div>

            <label className={checkClass}>
              <input type="checkbox" checked={entraSkipSsl} onChange={e => setEntraSkipSsl(e.target.checked)} className="accent-brand-600" />
              Skip TLS verification (only behind TLS-inspecting proxies)
            </label>

            <TestButton onTest={() => handleTestConnection('entra')} testing={testing === 'entra'} disabled={!entraTenant || !entraClient} result={outcome(testResult, 'entra')} />
          </SettingsCard>

          <OIDCSettings value={oidc} onChange={setOIDC} />

          <div className="bg-white rounded-xl border border-slate-200 shadow-sm p-6 space-y-5">
            <div>
              <label htmlFor="dashboard-url" className={labelClass}>Dashboard address</label>
              <input id="dashboard-url" value={dashboardUrl} onChange={e => setDashboardUrl(e.target.value)} placeholder="https://costdeck.example.com"
                className={`${inputClass} font-mono md:w-96`} />
              <p className={hintClass}>The address people open Cost Deck at. The sleep page a schedule shows while an application is down links here to wake it.</p>
            </div>
            <label className="flex items-start gap-3 cursor-pointer">
              <input type="checkbox" checked={disableLocalLogin} onChange={e => setDisableLocalLogin(e.target.checked)} className="mt-1 accent-brand-600" />
              <span>
                <span className="block text-sm font-semibold text-slate-700">Single sign-on only</span>
                <span className="block text-xs text-slate-500">Hides the password form, and local users can no longer sign in. Only takes effect while Microsoft or OpenID Connect sign-in is enabled, so nobody is locked out; the built-in admin keeps working through the API as break-glass access.</span>
              </span>
            </label>
          </div>
        </div>
      )}

      {/* ─── Features Section ────────────────────────────────────────────── */}
      {section === 'calendars' && (
        <div className="space-y-4 animate-rise">
          <SectionHeader
            icon={<CalendarDays className="text-brand-600" size={20} />}
            title="Holiday calendars"
            subtitle="Days such as public holidays that schedules apply as exceptions"
            action={saveAction('calendars')}
          />
          <CalendarsEditor value={calendars} onChange={c => { setCalendars(c); setDirty(d => ({ ...d, calendars: true })) }} />
        </div>
      )}

      {section === 'pricing' && (
        <div className="space-y-4 animate-rise">
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

            <div className="flex items-center justify-between gap-4">
              <div>
                <h4 className="font-bold text-slate-800">Cloud list prices</h4>
                <p className="text-sm text-slate-500 mt-1">
                  Price each node at its list price (Linux, by instance type and region) and derive the per-core and per-GiB rates from the real hourly bill.
                  On AWS this uses the Price List API and needs the <code>pricing:GetProducts</code> permission; on Azure the public Retail Prices API, with no credentials.
                  Spot, reservations and savings plans are not applied. Google Cloud clusters keep the estimate.
                </p>
              </div>
              <Toggle checked={cloudPricingApi} onChange={setCloudPricingApi} label="Enable Cloud list prices" />
            </div>

            <div>
              <h4 className="font-bold text-slate-800">Custom rates</h4>
              <p className="text-sm text-slate-500 mt-1 mb-3">For on-premises clusters or negotiated prices. When both rates are set they override every other source.</p>
              <div className="grid grid-cols-3 gap-3">
                <div>
                  <label className={labelClass}>Per core-hour</label>
                  <input value={priceCpu} onChange={e => setPriceCpu(e.target.value)} placeholder="0.031" className={`${inputClass} font-mono`} />
                </div>
                <div>
                  <label className={labelClass}>Per GiB-hour</label>
                  <input value={priceMem} onChange={e => setPriceMem(e.target.value)} placeholder="0.0042" className={`${inputClass} font-mono`} />
                </div>
                <div>
                  <label className={labelClass}>Currency</label>
                  <input value={priceCurrency} onChange={e => setPriceCurrency(e.target.value)} placeholder="USD" maxLength={3} className={`${inputClass} font-mono uppercase`} />
                </div>
              </div>
              <p className="text-sm text-slate-500 mt-4 mb-3">Volumes and load balancers are priced at the cloud's list price. Without a cloud price list, network volumes are estimated at 0.08 USD a GiB-month and load balancers at nothing. Set your own here, and always when the currency is not USD.</p>
              <div className="grid grid-cols-3 gap-3">
                <div>
                  <label className={labelClass}>Per GiB-month of volume</label>
                  <input value={priceStorage} onChange={e => setPriceStorage(e.target.value)} placeholder="0.08" className={`${inputClass} font-mono`} />
                </div>
                <div>
                  <label className={labelClass}>Per load balancer-month</label>
                  <input value={priceLB} onChange={e => setPriceLB(e.target.value)} placeholder="18" className={`${inputClass} font-mono`} />
                </div>
              </div>
              <p className="text-sm text-slate-500 mt-4 mb-3">
                GPUs are priced apart from CPU and memory: a GPU costs its share of the node's list price, or an estimate for its model (an A10G about 0.81 USD an hour).
                A rate here applies to every GPU.
                {!!settings?.pricing?.effective.gpuHour && <> In effect now: {settings.pricing.effective.gpuHour.toFixed(2)} {settings.pricing.effective.currency} a GPU-hour on average.</>}
              </p>
              <div className="grid grid-cols-3 gap-3">
                <div>
                  <label htmlFor="price-gpu" className={labelClass}>Per GPU-hour</label>
                  <input id="price-gpu" value={priceGPU} onChange={e => setPriceGPU(e.target.value)} placeholder="0.81" className={`${inputClass} font-mono`} />
                </div>
              </div>
              <p className="text-sm text-slate-500 mt-4 mb-3">
                LLM tokens that applications report through OpenTelemetry (<code>gen_ai.request.model</code>) are priced per model here, per million tokens.
                LiteLLM reports what its tokens cost itself. See <b>AI Costs</b>.
              </p>
              <ModelPricesEditor models={llmModels} onChange={m => { setLLMModels(m); setDirty(d => ({ ...d, pricing: true })) }} />
            </div>
          </div>
        </div>
      )}

      {section === 'billing' && (
        <div className="space-y-4 animate-rise">
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
        <div className="space-y-4 animate-rise">
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
