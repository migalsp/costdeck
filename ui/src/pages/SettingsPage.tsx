import { useState, useEffect, useCallback } from 'react'
import {
  Settings, Cloud, Bot, MessageSquare, Plus, Trash2, RefreshCw,
  CheckCircle2, XCircle, AlertTriangle, Eye, EyeOff, ChevronDown,
  ChevronUp, Sparkles, ExternalLink, Activity, Plug
} from 'lucide-react'
import { AWSLogo, AzureLogo, GCPLogo, WebexLogo } from '../components/ProviderLogos'

// ─── Types ──────────────────────────────────────────────────────────────────

interface ProviderStatus {
  connected: boolean
  lastChecked?: string
  error?: string
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
  hasCredentials: boolean
  status?: ProviderStatus
}

interface GCPSettings {
  enabled: boolean
  projectId?: string
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
  roomId?: string
  hasCredentials: boolean
}

interface VictoriaMetricsSettings {
  enabled: boolean
  endpoint?: string
  retentionDays: number
  hasCredentials: boolean
}

interface MCPSettings {
  enabled: boolean
  port: number
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
}

// ─── Sub-Components ─────────────────────────────────────────────────────────

const StatusBadge = ({ connected, error }: { connected: boolean; error?: string }) => (
  <div className={`inline-flex items-center gap-1.5 px-2.5 py-1 rounded-full text-[10px] font-bold uppercase tracking-wider ${connected
      ? 'bg-emerald-50 text-emerald-700 ring-1 ring-emerald-200'
      : error
        ? 'bg-red-50 text-red-600 ring-1 ring-red-200'
        : 'bg-slate-100 text-slate-500 ring-1 ring-slate-200'
    }`}>
    {connected ? <CheckCircle2 size={12} /> : error ? <XCircle size={12} /> : <AlertTriangle size={12} />}
    {connected ? 'Connected' : error ? 'Error' : 'Not configured'}
  </div>
)

const ComingSoonBadge = () => (
  <span className="inline-flex items-center gap-1 px-2.5 py-1 rounded-full text-[10px] font-bold uppercase tracking-wider bg-violet-50 text-violet-600 ring-1 ring-violet-200">
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
        className="w-full px-4 py-2.5 bg-white border border-slate-200 rounded-xl text-sm focus:outline-none focus:ring-2 focus:ring-emerald-500/30 focus:border-emerald-400 transition-all pr-10 font-mono"
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
            <button onClick={() => removeTag(k)} className="ml-1 text-emerald-400 hover:text-red-500 transition-colors">
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
          className="flex-1 px-3 py-2 bg-white border border-slate-200 rounded-lg text-xs font-mono focus:outline-none focus:ring-2 focus:ring-emerald-500/30 focus:border-emerald-400 transition-all"
          onKeyDown={e => e.key === 'Enter' && addTag()}
        />
        <input
          value={newValue}
          onChange={e => setNewValue(e.target.value)}
          placeholder="Tag value"
          className="flex-1 px-3 py-2 bg-white border border-slate-200 rounded-lg text-xs font-mono focus:outline-none focus:ring-2 focus:ring-emerald-500/30 focus:border-emerald-400 transition-all"
          onKeyDown={e => e.key === 'Enter' && addTag()}
        />
        <button
          onClick={addTag}
          disabled={!newKey.trim()}
          className="px-3 py-2 bg-emerald-500 text-white rounded-lg text-xs font-bold hover:bg-emerald-600 transition-colors disabled:opacity-50 disabled:cursor-not-allowed flex items-center gap-1"
        >
          <Plus size={14} /> Add
        </button>
      </div>
    </div>
  )
}

// ─── Section Components ─────────────────────────────────────────────────────

const SectionHeader = ({ icon, title, subtitle }: { icon: React.ReactNode; title: React.ReactNode; subtitle: string }) => (
  <div className="flex items-center gap-3 mb-6">
    <div className="w-10 h-10 rounded-xl bg-emerald-500/10 flex items-center justify-center">
      {icon}
    </div>
    <div>
      <h3 className="text-lg font-bold text-slate-800">{title}</h3>
      <p className="text-xs text-slate-500">{subtitle}</p>
    </div>
  </div>
)

const ProviderCard = ({
  name, logo, children, enabled, onToggle, comingSoon, expanded, onExpand, status
}: {
  name: string; logo: React.ReactNode; children: React.ReactNode; enabled: boolean;
  onToggle: (v: boolean) => void; comingSoon?: boolean; expanded: boolean;
  onExpand: () => void; status?: ProviderStatus
}) => (
  <div className={`bg-white rounded-2xl border shadow-sm transition-all duration-300 ${enabled ? 'border-emerald-200 ring-1 ring-emerald-100' : 'border-slate-200'
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
          <div className={`w-11 h-6 rounded-full peer-focus:outline-none peer-focus:ring-4 peer-focus:ring-emerald-500/20 transition-colors ${enabled ? 'bg-emerald-500' : 'bg-slate-300'
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

const AI_MODELS = {
  openai: [
    { id: 'gpt-5', name: 'GPT-5' },
    { id: 'gpt-5-turbo', name: 'GPT-5 Turbo' },
    { id: 'gpt-5-mini', name: 'GPT-5 Mini' },
    { id: 'o4', name: 'o4' },
    { id: 'o3', name: 'o3' },
    { id: 'o3-mini', name: 'o3 Mini' },
    { id: 'gpt-4o', name: 'GPT-4o' }
  ],
  anthropic: [
    { id: 'claude-4-opus-20260228', name: 'Claude 4 Opus' },
    { id: 'claude-4-sonnet-20260415', name: 'Claude 4 Sonnet' },
    { id: 'claude-4-haiku-20260501', name: 'Claude 4 Haiku' },
    { id: 'claude-3-7-sonnet-20250219', name: 'Claude 3.7 Sonnet' },
    { id: 'claude-3-5-sonnet-20241022', name: 'Claude 3.5 Sonnet' }
  ],
  gemini: [
    { id: 'gemini-3.5-flash', name: 'Gemini 3.5 Flash' },
    { id: 'gemini-3.1-pro', name: 'Gemini 3.1 Pro' },
    { id: 'gemini-3.1-flash', name: 'Gemini 3.1 Flash' },
    { id: 'gemini-3.0-pro', name: 'Gemini 3.0 Pro' },
    { id: 'gemini-3.0-flash', name: 'Gemini 3.0 Flash' },
    { id: 'gemini-2.5-pro', name: 'Gemini 2.5 Pro' },
    { id: 'gemini-2.5-flash', name: 'Gemini 2.5 Flash' }
  ]
};

export default function SettingsPage() {
  const [settings, setSettings] = useState<SettingsData | null>(null)
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [testing, setTesting] = useState<string | null>(null)
  const [testResult, setTestResult] = useState<{ provider: string; connected: boolean; error?: string } | null>(null)
  const [saveMessage, setSaveMessage] = useState<string | null>(null)
  const [expandedProvider, setExpandedProvider] = useState<string | null>('aws')
  const [expandedSection, setExpandedSection] = useState<string>('providers')

  // AWS form state
  const [awsAccessKey, setAwsAccessKey] = useState('')
  const [awsSecretKey, setAwsSecretKey] = useState('')
  const [awsRegion, setAwsRegion] = useState('us-east-1')
  const [awsEnabled, setAwsEnabled] = useState(true)
  const [awsTags, setAwsTags] = useState<Record<string, string>>({})
  const [awsResourceTypes, setAwsResourceTypes] = useState<string[]>(['aurora'])

  // Azure form state (stub)
  const [azureEnabled, setAzureEnabled] = useState(false)

  // GCP form state (stub)
  const [gcpEnabled, setGcpEnabled] = useState(false)

  // AI form state
  const [aiEnabled, setAiEnabled] = useState(false)
  const [aiProvider, setAiProvider] = useState('openai')
  const [aiModel, setAiModel] = useState('')
  const [aiBaseUrl, setAiBaseUrl] = useState('')

  // Features state
  const [cloudPricingApi, setCloudPricingApi] = useState(false)
  const [aiApiKey, setAiApiKey] = useState('')
  const [aiSkipSslVerify, setAiSkipSslVerify] = useState(false)

  // Webex form state
  const [webexEnabled, setWebexEnabled] = useState(false)
  const [webexRoomId, setWebexRoomId] = useState('')
  const [webexBotToken, setWebexBotToken] = useState('')

  // VictoriaMetrics form state
  const [vmEnabled, setVmEnabled] = useState(false)
  const [vmEndpoint, setVmEndpoint] = useState('')
  const [vmRetentionDays, setVmRetentionDays] = useState(7)
  const [vmBearerToken, setVmBearerToken] = useState('')
  const [vmUsername, setVmUsername] = useState('')
  const [vmPassword, setVmPassword] = useState('')
  const [vmAuthMode, setVmAuthMode] = useState<'bearer' | 'basic'>('bearer')

  // MCP form state
  const [mcpEnabled, setMcpEnabled] = useState(false)
  const [mcpPort, setMcpPort] = useState(8083)

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
        if (data.providers.azure) setAzureEnabled(data.providers.azure.enabled)
        if (data.providers.gcp) setGcpEnabled(data.providers.gcp.enabled)
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
        }
        if (data.integrations.victoriaMetrics) {
          setVmEnabled(data.integrations.victoriaMetrics.enabled)
          setVmEndpoint(data.integrations.victoriaMetrics.endpoint || '')
          setVmRetentionDays(data.integrations.victoriaMetrics.retentionDays || 7)
        }
        if (data.integrations.mcp) {
          setMcpEnabled(data.integrations.mcp.enabled)
          setMcpPort(data.integrations.mcp.port || 8083)
        }
        if (data.features) {
          setCloudPricingApi(data.features.cloudPricingApi || false)
        }
      }
    } catch (err) {
      console.error('Failed to fetch settings:', err)
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => { fetchSettings() }, [fetchSettings])

  const handleSave = async () => {
    setSaving(true)
    setSaveMessage(null)
    try {
      const body: any = {
        providers: {
          aws: {
            enabled: awsEnabled,
            region: awsRegion,
            discoveryTags: awsTags,
            resourceTypes: awsResourceTypes,
            ...(awsAccessKey && awsSecretKey ? { accessKeyId: awsAccessKey, secretAccessKey: awsSecretKey } : {}),
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
              ...(webexBotToken ? { botToken: webexBotToken } : {})
            }
          },
          victoriaMetrics: {
            enabled: vmEnabled,
            endpoint: vmEndpoint,
            retentionDays: vmRetentionDays,
            ...(vmAuthMode === 'bearer' && vmBearerToken ? { bearerToken: vmBearerToken } : {}),
            ...(vmAuthMode === 'basic' && vmUsername ? { username: vmUsername, password: vmPassword } : {}),
          },
          mcp: {
            enabled: mcpEnabled,
            port: mcpPort
          }
        },
        features: {
          cloudPricingApi: cloudPricingApi,
        }
      }

      const res = await fetch('/api/settings', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
      })

      if (res.ok) {
        const data = await res.json()
        setSettings(data)
        setAwsAccessKey('')
        setAwsSecretKey('')
        setAiApiKey('')
        setWebexBotToken('')
        setVmBearerToken('')
        setVmUsername('')
        setVmPassword('')
        setSaveMessage('Settings saved successfully')
        setTimeout(() => setSaveMessage(null), 3000)
      } else {
        const err = await res.text()
        setSaveMessage(`Failed to save: ${err}`)
      }
    } catch (err) {
      setSaveMessage(`Error: ${err}`)
    } finally {
      setSaving(false)
    }
  }

  const handleTestConnection = async (provider: string) => {
    setTesting(provider)
    setTestResult(null)
    try {
      const body: any = {}
      if (provider === 'aws') {
        body.accessKeyId = awsAccessKey
        body.secretAccessKey = awsSecretKey
        body.region = awsRegion
      } else if (provider === 'ai') {
        body.provider = aiProvider
        body.model = aiModel
        body.baseUrl = aiBaseUrl
        body.apiKey = aiApiKey
        body.skipSslVerify = aiSkipSslVerify
      }

      const res = await fetch(`/api/settings/providers/${provider}/test`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
      })
      const data = await res.json()
      setTestResult({ provider, connected: data.connected, error: data.error })
    } catch (err) {
      setTestResult({ provider, connected: false, error: String(err) })
    } finally {
      setTesting(null)
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
        <div className="animate-spin rounded-full h-12 w-12 border-b-2 border-emerald-500" />
      </div>
    )
  }

  const awsRegions = [
    'us-east-1', 'us-east-2', 'us-west-1', 'us-west-2',
    'eu-west-1', 'eu-west-2', 'eu-west-3', 'eu-central-1', 'eu-north-1',
    'ap-southeast-1', 'ap-southeast-2', 'ap-northeast-1', 'ap-northeast-2', 'ap-south-1',
    'sa-east-1', 'ca-central-1',
  ]

  return (
    <div className="p-8 max-w-[960px] mx-auto animate-in fade-in duration-500">
      {/* Header */}
      <div className="flex justify-between items-center mb-8">
        <div>
          <h2 className="text-3xl font-bold tracking-tight text-slate-800 flex items-center gap-3">
            <Settings className="text-emerald-500" size={32} />
            Settings
          </h2>
          <p className="text-slate-500 mt-1">Configure providers, integrations, and credentials</p>
        </div>
        <div className="flex items-center gap-3">
          {saveMessage && (
            <span className={`text-xs font-bold px-3 py-1.5 rounded-lg animate-in fade-in duration-300 ${saveMessage.includes('success') ? 'bg-emerald-50 text-emerald-600' : 'bg-red-50 text-red-600'
              }`}>
              {saveMessage}
            </span>
          )}
          <button
            onClick={handleSave}
            disabled={saving}
            className="flex items-center gap-2 px-5 py-2.5 bg-emerald-500 text-white rounded-xl shadow-lg shadow-emerald-500/20 hover:bg-emerald-600 transition-all font-bold text-sm disabled:opacity-50"
          >
            {saving ? <RefreshCw size={16} className="animate-spin" /> : <CheckCircle2 size={16} />}
            Save Changes
          </button>
        </div>
      </div>

      {/* Section Tabs */}
      <div className="flex gap-1 mb-6 bg-slate-100 p-1 rounded-xl w-fit">
        {[
          { id: 'providers', icon: <Cloud size={16} />, label: 'Cloud Providers' },
          { id: 'monitoring', icon: <Activity size={16} />, label: 'Monitoring' },
          { id: 'ai', icon: <Bot size={16} />, label: 'AI Models' },
          { id: 'messengers', icon: <MessageSquare size={16} />, label: 'Messengers' },
          { id: 'mcp', icon: <Plug size={16} />, label: 'MCP Server' },
          { id: 'features', icon: <Sparkles size={16} />, label: 'Features' },
        ].map(tab => (
          <button
            key={tab.id}
            onClick={() => setExpandedSection(tab.id)}
            className={`flex items-center gap-2 px-4 py-2 rounded-lg text-xs font-bold transition-all ${expandedSection === tab.id
                ? 'bg-white text-slate-800 shadow-sm'
                : 'text-slate-500 hover:text-slate-700'
              }`}
          >
            {tab.icon} {tab.label}
          </button>
        ))}
      </div>

      {/* ─── Cloud Providers Section ─────────────────────────────────────── */}
      {expandedSection === 'providers' && (
        <div className="space-y-4 animate-in fade-in slide-in-from-bottom-2 duration-300">
          <SectionHeader
            icon={<Cloud className="text-emerald-500" size={20} />}
            title="Cloud Providers"
            subtitle="Connect your cloud accounts for resource discovery and scaling"
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
                  className="w-full px-4 py-2.5 bg-white border border-slate-200 rounded-xl text-sm focus:outline-none focus:ring-2 focus:ring-emerald-500/30 focus:border-emerald-400 transition-all appearance-none cursor-pointer"
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
                  <span className={`text-xs font-bold flex items-center gap-1 ${testResult.connected ? 'text-emerald-600' : 'text-red-500'}`}>
                    {testResult.connected ? <CheckCircle2 size={14} /> : <XCircle size={14} />}
                    {testResult.connected ? 'Connection successful' : testResult.error || 'Connection failed'}
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
                        className="mt-0.5 accent-emerald-500"
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
            comingSoon
            expanded={expandedProvider === 'azure'}
            onExpand={() => setExpandedProvider(expandedProvider === 'azure' ? null : 'azure')}
            status={settings?.providers.azure?.status}
          >
            <div />
          </ProviderCard>

          {/* GCP */}
          <ProviderCard
            name="Google Cloud Platform"
            logo={<GCPLogo />}
            enabled={gcpEnabled}
            onToggle={setGcpEnabled}
            comingSoon
            expanded={expandedProvider === 'gcp'}
            onExpand={() => setExpandedProvider(expandedProvider === 'gcp' ? null : 'gcp')}
            status={settings?.providers.gcp?.status}
          >
            <div />
          </ProviderCard>
        </div>
      )}

      {/* ─── Monitoring Section (VictoriaMetrics) ──────────────────────────── */}
      {expandedSection === 'monitoring' && (
        <div className="space-y-4 animate-in fade-in slide-in-from-bottom-2 duration-300">
          <SectionHeader
            icon={<Activity className="text-orange-500" size={20} />}
            title={
              <div className="flex items-center gap-2">
                Monitoring <span className="text-[10px] font-bold text-emerald-600 bg-emerald-500/10 px-2 py-0.5 rounded-full">(Experimental)</span>
              </div>
            }
            subtitle="Configure metrics data source for namespace insights and optimization"
          />

          <div className="bg-white rounded-2xl border border-slate-200 shadow-sm p-6">
            <div className="flex items-center justify-between mb-6">
              <div className="flex items-center gap-3">
                <div className="w-10 h-10 rounded-xl bg-orange-500/10 flex items-center justify-center">
                  <Activity className="text-orange-500" size={20} />
                </div>
                <div>
                  <span className="font-bold text-slate-800">VictoriaMetrics</span>
                  <p className="text-[10px] text-slate-400">PromQL-compatible metrics backend</p>
                </div>
              </div>
              <div className="flex items-center gap-3">
                {vmEnabled && settings?.integrations.victoriaMetrics?.hasCredentials && (
                  <span className="inline-flex items-center gap-1 px-2 py-0.5 rounded-full text-[10px] font-bold bg-emerald-50 text-emerald-600 ring-1 ring-emerald-200">
                    <CheckCircle2 size={10} /> Configured
                  </span>
                )}
                <label className="relative inline-flex items-center cursor-pointer">
                  <input type="checkbox" checked={vmEnabled} onChange={e => setVmEnabled(e.target.checked)} className="sr-only peer" />
                  <div className={`w-11 h-6 rounded-full ${vmEnabled ? 'bg-orange-500' : 'bg-slate-300'}`}>
                    <div className={`absolute top-0.5 left-0.5 w-5 h-5 bg-white rounded-full shadow-md transition-transform ${vmEnabled ? 'translate-x-5' : 'translate-x-0'}`} />
                  </div>
                </label>
              </div>
            </div>

            <div className={`space-y-5 ${!vmEnabled ? 'opacity-50 pointer-events-none' : ''}`}>
              {/* Info banner */}
              <div className="flex items-start gap-3 p-4 bg-orange-50 rounded-xl border border-orange-100">
                <AlertTriangle size={16} className="text-orange-500 mt-0.5 flex-shrink-0" />
                <div className="text-xs text-orange-700">
                  <p className="font-bold mb-1">Metrics Source Override</p>
                  <p>When enabled, namespace insights will be collected from VictoriaMetrics instead of the Kubernetes Metrics Server. This provides historical data retention and more accurate resource recommendations.</p>
                </div>
              </div>

              {/* Endpoint */}
              <div>
                <label className="text-xs font-bold uppercase tracking-wider text-slate-500 mb-1.5 block">Endpoint URL</label>
                <input
                  value={vmEndpoint}
                  onChange={e => setVmEndpoint(e.target.value)}
                  placeholder="http://vmselect.monitoring.svc:8481/select/0/prometheus"
                  className="w-full px-4 py-2.5 bg-white border border-slate-200 rounded-xl text-sm font-mono focus:outline-none focus:ring-2 focus:ring-orange-500/30 focus:border-orange-400 transition-all"
                />
                <p className="text-[10px] text-slate-400 mt-1">In-cluster: http://service.namespace.svc:port/path • External: https://vm.example.com</p>
              </div>

              {/* Retention Days */}
              <div>
                <label className="text-xs font-bold uppercase tracking-wider text-slate-500 mb-1.5 block">Retention Period (days)</label>
                <div className="flex items-center gap-3">
                  <input
                    type="number"
                    min={1}
                    max={90}
                    value={vmRetentionDays}
                    onChange={e => setVmRetentionDays(parseInt(e.target.value) || 7)}
                    className="w-24 px-4 py-2.5 bg-white border border-slate-200 rounded-xl text-sm text-center focus:outline-none focus:ring-2 focus:ring-orange-500/30 focus:border-orange-400 transition-all"
                  />
                  <span className="text-xs text-slate-400">days of metrics lookback for Optimize recommendations</span>
                </div>
              </div>

              {/* Auth Mode */}
              <div>
                <label className="text-xs font-bold uppercase tracking-wider text-slate-500 mb-2 block">Authentication</label>
                <div className="flex gap-2 mb-3">
                  <button
                    onClick={() => setVmAuthMode('bearer')}
                    className={`px-4 py-2 rounded-lg text-xs font-bold transition-all ${vmAuthMode === 'bearer'
                        ? 'bg-orange-500 text-white shadow-sm'
                        : 'bg-slate-100 text-slate-600 hover:bg-slate-200'
                      }`}
                  >
                    Bearer Token
                  </button>
                  <button
                    onClick={() => setVmAuthMode('basic')}
                    className={`px-4 py-2 rounded-lg text-xs font-bold transition-all ${vmAuthMode === 'basic'
                        ? 'bg-orange-500 text-white shadow-sm'
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
                        className="w-full px-4 py-2.5 bg-white border border-slate-200 rounded-xl text-sm focus:outline-none focus:ring-2 focus:ring-orange-500/30 focus:border-orange-400 transition-all"
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
            </div>

            {vmEnabled && vmEndpoint && (
              <div className="flex items-center gap-3 mt-6 pt-4 border-t border-slate-100 justify-center text-orange-500">
                <Activity size={16} />
                <span className="text-xs font-medium">Namespace insights will use VictoriaMetrics at {vmEndpoint}</span>
              </div>
            )}
          </div>
        </div>
      )}

      {/* ─── AI Models Section ───────────────────────────────────────────── */}
      {expandedSection === 'ai' && (
        <div className="space-y-4 animate-in fade-in slide-in-from-bottom-2 duration-300">
          <SectionHeader
            icon={<Bot className="text-violet-500" size={20} />}
            title="AI Models"
            subtitle="Connect AI models for intelligent cost optimization insights"
          />

          <div className="bg-white rounded-2xl border border-slate-200 shadow-sm p-6">
            <div className="flex items-center justify-between mb-6">
              <div className="flex items-center gap-3">
                <div className="w-10 h-10 rounded-xl bg-violet-500/10 flex items-center justify-center">
                  <Sparkles className="text-violet-500" size={20} />
                </div>
                <div>
                  <span className="font-bold text-slate-800">AI-Powered Insights</span>
                </div>
              </div>
              <label className="relative inline-flex items-center cursor-pointer">
                <input type="checkbox" checked={aiEnabled} onChange={e => setAiEnabled(e.target.checked)} className="sr-only peer" />
                <div className={`w-11 h-6 rounded-full ${aiEnabled ? 'bg-violet-500' : 'bg-slate-300'}`}>
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
                    }}
                    className="w-full px-4 py-2.5 bg-white border border-slate-200 rounded-xl text-sm appearance-none focus:outline-none focus:ring-2 focus:ring-violet-500/30 focus:border-violet-400 transition-all"
                  >
                    <option value="openai">OpenAI</option>
                    <option value="anthropic">Anthropic</option>
                    <option value="gemini">Google Gemini</option>
                    <option value="local">Local / Custom Endpoint</option>
                  </select>
                </div>
                <div>
                  <label className="text-xs font-bold uppercase tracking-wider text-slate-500 mb-1.5 block">Model</label>
                  {aiProvider === 'local' ? (
                    <input
                      value={aiModel}
                      onChange={e => setAiModel(e.target.value)}
                      placeholder="e.g. llama3, mixtral"
                      className="w-full px-4 py-2.5 bg-white border border-slate-200 rounded-xl text-sm focus:outline-none focus:ring-2 focus:ring-violet-500/30 focus:border-violet-400 transition-all"
                    />
                  ) : (
                    <select
                      value={aiModel}
                      onChange={e => setAiModel(e.target.value)}
                      className="w-full px-4 py-2.5 bg-white border border-slate-200 rounded-xl text-sm appearance-none focus:outline-none focus:ring-2 focus:ring-violet-500/30 focus:border-violet-400 transition-all cursor-pointer"
                    >
                      <option value="" disabled>Select a model...</option>
                      {AI_MODELS[aiProvider as keyof typeof AI_MODELS]?.map(m => (
                        <option key={m.id} value={m.id}>{m.name}</option>
                      ))}
                    </select>
                  )}
                </div>
              </div>

              {aiProvider === 'local' ? (
                <>
                  <div className="grid grid-cols-2 gap-4">
                    <div>
                      <label className="text-xs font-bold uppercase tracking-wider text-slate-500 mb-1 block">API Base URL <span className="text-[10px] lowercase text-slate-400 font-normal">(Optional)</span></label>
                      <p className="text-[10px] text-slate-400 mb-1.5">Enter URL to override the default provider API endpoint (e.g., http://localhost:11434).</p>
                      <input
                        value={aiBaseUrl}
                        onChange={e => setAiBaseUrl(e.target.value)}
                        placeholder="https://api.openai.com/v1"
                        className="w-full px-4 py-2.5 bg-white border border-slate-200 rounded-xl text-sm focus:outline-none focus:ring-2 focus:ring-violet-500/30 focus:border-violet-400 transition-all"
                      />
                    </div>
                    <div>
                      <label className="text-xs font-bold uppercase tracking-wider text-slate-500 mb-1 block">API Key</label>
                      <p className="text-[10px] text-slate-400 mb-1.5">Leave blank if using a local model that doesn't require auth.</p>
                      <SecretInput value={aiApiKey} onChange={setAiApiKey} placeholder={settings?.integrations.ai?.hasCredentials ? '••••••••••••••••••••' : 'Enter API key'} />
                    </div>
                  </div>
                  <div className="flex items-center gap-2 mt-2 mb-4">
                    <input
                      type="checkbox"
                      id="skipSslVerify"
                      checked={aiSkipSslVerify}
                      onChange={(e) => setAiSkipSslVerify(e.target.checked)}
                      className="rounded border-slate-300 text-violet-500 focus:ring-violet-500"
                    />
                    <label htmlFor="skipSslVerify" className="text-xs font-bold text-slate-500 cursor-pointer">
                      Skip SSL Verification (Insecure)
                    </label>
                  </div>
                </>
              ) : (
                <div className="grid grid-cols-1 gap-4">
                  <div>
                    <label className="text-xs font-bold uppercase tracking-wider text-slate-500 mb-1 block">API Key</label>
                    <p className="text-[10px] text-slate-400 mb-1.5">Required API Key for the selected provider.</p>
                    <SecretInput value={aiApiKey} onChange={setAiApiKey} placeholder={settings?.integrations.ai?.hasCredentials ? '••••••••••••••••••••' : 'Enter API key'} />
                  </div>
                </div>
              )}

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
                  <span className={`text-xs font-bold flex items-center gap-1 ${testResult.connected ? 'text-emerald-600' : 'text-red-500'}`}>
                    {testResult.connected ? <CheckCircle2 size={14} /> : <XCircle size={14} />}
                    {testResult.connected ? 'Connection successful' : testResult.error || 'Connection failed'}
                  </span>
                )}
              </div>
            </div>

            {aiEnabled && (
              <div className="flex items-center gap-3 mt-6 pt-4 border-t border-slate-100 justify-center text-violet-500">
                <Sparkles size={16} />
                <span className="text-xs font-medium">AI-powered cost optimization and chatbot are active.</span>
              </div>
            )}
          </div>
        </div>
      )}

      {/* ─── Messengers Section ──────────────────────────────────────────── */}
      {expandedSection === 'messengers' && (
        <div className="space-y-4 animate-in fade-in slide-in-from-bottom-2 duration-300">
          <SectionHeader
            icon={<MessageSquare className="text-blue-500" size={20} />}
            title="Messengers"
            subtitle="Connect messaging platforms to control Cost Deck remotely"
          />

          <div className="bg-white rounded-2xl border border-slate-200 shadow-sm p-6">
            <div className="flex items-center justify-between mb-6">
              <div className="flex items-center gap-3">
                <WebexLogo />
                <div>
                  <span className="font-bold text-slate-800">Cisco Webex</span>
                </div>
              </div>
              <label className="relative inline-flex items-center cursor-pointer">
                <input type="checkbox" checked={webexEnabled} onChange={e => setWebexEnabled(e.target.checked)} className="sr-only peer" />
                <div className={`w-11 h-6 rounded-full ${webexEnabled ? 'bg-blue-500' : 'bg-slate-300'}`}>
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
                <label className="text-xs font-bold uppercase tracking-wider text-slate-500 mb-1.5 block">Room ID</label>
                <input
                  value={webexRoomId}
                  onChange={e => setWebexRoomId(e.target.value)}
                  placeholder="Webex Room/Space ID"
                  className="w-full px-4 py-2.5 bg-white border border-slate-200 rounded-xl text-sm"
                />
              </div>
            </div>

            {webexEnabled && (
              <div className="flex items-center gap-3 mt-6 pt-4 border-t border-slate-100 justify-center text-emerald-500">
                <MessageSquare size={16} />
                <span className="text-xs font-medium">Webex integration is active. Use /scale commands in the configured room.</span>
              </div>
            )}
          </div>
        </div>
      )}

      {/* ─── MCP Server Section ────────────────────────────────────────────── */}
      {expandedSection === 'mcp' && (
        <div className="space-y-4 animate-in fade-in slide-in-from-bottom-2 duration-300">
          <SectionHeader
            icon={<Plug className="text-pink-500" size={20} />}
            title="MCP (Experimental)"
            subtitle="Expose CostDeck's capabilities as tools to external AI assistants like Cursor or Claude Desktop."
          />

          <div className="bg-white rounded-2xl border border-slate-200 shadow-sm p-6">
            <div className="flex items-center justify-between mb-6">
              <div className="flex items-center gap-3">
                <div className="w-10 h-10 rounded-xl bg-pink-500/10 flex items-center justify-center">
                  <Plug className="text-pink-500" size={20} />
                </div>
                <div>
                  <span className="font-bold text-slate-800">MCP Server</span>
                </div>
              </div>
              <label className="relative inline-flex items-center cursor-pointer">
                <input type="checkbox" checked={mcpEnabled} onChange={e => setMcpEnabled(e.target.checked)} className="sr-only peer" />
                <div className={`w-11 h-6 rounded-full ${mcpEnabled ? 'bg-pink-500' : 'bg-slate-300'}`}>
                  <div className={`absolute top-0.5 left-0.5 w-5 h-5 bg-white rounded-full shadow-md transition-transform ${mcpEnabled ? 'translate-x-5' : 'translate-x-0'}`} />
                </div>
              </label>
            </div>

            <div className={`space-y-4 ${!mcpEnabled ? 'opacity-50 pointer-events-none' : ''}`}>
              <div>
                <label className="text-xs font-bold uppercase tracking-wider text-slate-500 mb-1.5 block">Port</label>
                <input
                  type="number"
                  value={mcpPort}
                  onChange={e => setMcpPort(Number(e.target.value))}
                  placeholder="8083"
                  className="w-full max-w-[200px] px-4 py-2.5 bg-white border border-slate-200 rounded-xl text-sm"
                />
              </div>

              <div className="bg-blue-50 border border-blue-100 rounded-xl p-4 mt-6">
                <h4 className="text-sm font-bold text-blue-800 mb-2">How to connect Cursor/Claude Desktop:</h4>
                <p className="text-xs text-blue-600 mb-3">Add CostDeck as an MCP SSE Server. Use your cluster's LoadBalancer or port-forwarded IP.</p>
                <div className="bg-slate-900 rounded-lg p-3 overflow-x-auto">
                  <code className="text-xs text-emerald-400">
                    URL: http://&lt;costdeck-address&gt;:{mcpPort}/sse
                  </code>
                </div>
              </div>
            </div>
          </div>
        </div>
      )}

      {/* ─── Features Section ────────────────────────────────────────────── */}
      {expandedSection === 'features' && (
        <div className="space-y-4 animate-in fade-in slide-in-from-bottom-2 duration-300">
          <SectionHeader
            icon={<Sparkles className="text-amber-500" size={20} />}
            title="Features"
            subtitle="Enable or disable core CostDeck capabilities"
          />

          <div className="bg-white rounded-2xl border border-slate-200 shadow-sm p-6">
            <div className="flex flex-col gap-6">
              <div className="flex items-center justify-between">
                <div>
                  <h4 className="font-bold text-slate-800">Public Cloud API Pricing <span className="text-emerald-600 ml-1">(Experimental)</span></h4>
                  <p className="text-sm text-slate-500 mt-1">
                    Use real-time API queries to AWS/Azure/GCP to get 100% accurate pricing instead of mathematical heuristics.
                  </p>
                </div>
                <label className="relative inline-flex items-center cursor-pointer ml-4">
                  <input type="checkbox" checked={cloudPricingApi} onChange={e => setCloudPricingApi(e.target.checked)} className="sr-only peer" />
                  <div className={`w-11 h-6 rounded-full peer-focus:outline-none peer-focus:ring-4 peer-focus:ring-emerald-500/20 transition-colors ${cloudPricingApi ? 'bg-emerald-500' : 'bg-slate-300'}`}>
                    <div className={`absolute top-0.5 left-0.5 w-5 h-5 bg-white rounded-full shadow-md transition-transform ${cloudPricingApi ? 'translate-x-5' : 'translate-x-0'}`} />
                  </div>
                </label>
              </div>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}
