import { useState } from 'react'
import { CheckCircle2, ExternalLink, Plus, RefreshCw, X, XCircle } from 'lucide-react'
import { apiError, errorMessage } from '../../lib/api'
import { keycloakDefaults, oidcProblem, oidcRequest, type OIDCForm } from '../../lib/oidc'
import { Button } from '../ui'

const input = 'w-full px-3 py-2 bg-white border border-slate-200 rounded-lg text-sm font-mono'
const label = 'text-[10px] font-bold uppercase tracking-wider text-slate-500 mb-1 block'

export default function OIDCSettings({ value, onChange }: { value: OIDCForm; onChange: (f: OIDCForm) => void }) {
  const [test, setTest] = useState<{ ok: boolean; text: string } | null>(null)
  const [testing, setTesting] = useState(false)
  const set = (patch: Partial<OIDCForm>) => onChange({ ...value, ...patch })
  const problem = oidcProblem(value)
  const runTest = async () => {
    setTesting(true)
    setTest(null)
    try {
      const res = await fetch('/api/settings/providers/oidc/test', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(oidcRequest(value)) })
      if (!res.ok) throw new Error(await apiError(res))
      const d = await res.json()
      setTest(d.connected ? { ok: true, text: d.message } : { ok: false, text: d.error || 'Connection failed' })
    } catch (e) {
      setTest({ ok: false, text: errorMessage(e) })
    } finally {
      setTesting(false)
    }
  }
  const field = (key: keyof OIDCForm, text: string, placeholder: string, extra?: { type?: string; hint?: string }) => (
    <div>
      <label htmlFor={`oidc-${key}`} className={label}>{text}</label>
      <input id={`oidc-${key}`} type={extra?.type || 'text'} value={value[key] as string} placeholder={placeholder}
        onChange={e => set({ [key]: e.target.value } as Partial<OIDCForm>)} className={input} />
      {extra?.hint && <p className="text-[11px] text-slate-400 mt-1">{extra.hint}</p>}
    </div>
  )

  return (
    <div className="bg-white rounded-xl border border-slate-200 shadow-sm p-6 space-y-5" data-testid="oidc-settings">
      <div className="flex items-center justify-between gap-3">
        <div>
          <span className="font-bold text-slate-800">OpenID Connect</span>
          <p className="text-[10px] text-slate-400">Keycloak, Okta, Auth0, Dex… · authorization code flow with PKCE</p>
        </div>
        <div className="flex items-center gap-3">
          <Button size="sm" variant="secondary" onClick={() => onChange(keycloakDefaults(value))}>Keycloak defaults</Button>
          <label className="relative inline-flex items-center cursor-pointer">
            <input type="checkbox" aria-label="Enable OpenID Connect sign-in" checked={value.enabled} onChange={e => set({ enabled: e.target.checked })} className="sr-only peer" />
            <div className={`w-11 h-6 rounded-full ${value.enabled ? 'bg-brand-600' : 'bg-slate-300'}`}>
              <div className={`absolute top-0.5 left-0.5 w-5 h-5 bg-white rounded-full shadow-md transition-transform ${value.enabled ? 'translate-x-5' : 'translate-x-0'}`} />
            </div>
          </label>
        </div>
      </div>

      <div className="p-4 bg-slate-50 border border-slate-200 rounded-xl text-xs text-slate-600 space-y-1.5">
        <p className="font-bold">Redirect URI to register with the provider:</p>
        <p><code className="bg-white px-1.5 py-0.5 rounded">{window.location.origin}/api/auth/oidc/callback</code></p>
        <p>In Keycloak: a confidential OpenID Connect client with the standard flow, and a <i>Group Membership</i> mapper that adds <code>groups</code> to the ID token.</p>
      </div>

      <div className="grid gap-4 md:grid-cols-2">
        {field('displayName', 'Button label', 'Keycloak', { hint: 'Shown as "Sign in with …".' })}
        {field('issuerUrl', 'Issuer URL', 'https://keycloak.example.com/realms/costdeck')}
        {field('clientId', 'Client ID', 'costdeck')}
        {field('clientSecret', 'Client secret', value.hasClientSecret ? '•••••••• (stored)' : 'empty for a public client', { type: 'password' })}
        {field('groupsClaim', 'Groups claim', 'groups', { hint: 'Dotted paths reach nested claims.' })}
        {field('rolesClaim', 'Roles claim', 'realm_access.roles', { hint: 'Roles named viewer, operator or admin count as they are.' })}
        {field('scopes', 'Extra scopes', 'profile email')}
        {field('redirectUrl', 'Redirect URL', 'derived from the address in use')}
        <div>
          <label htmlFor="oidc-defaultRole" className={label}>Role without a match</label>
          <select id="oidc-defaultRole" value={value.defaultRole} onChange={e => set({ defaultRole: e.target.value })} className={input}>
            {['viewer', 'operator', 'admin'].map(r => <option key={r} value={r}>{r}</option>)}
          </select>
        </div>
        <div className="flex flex-col gap-2 justify-center">
          <label className="flex items-center gap-2 text-sm text-slate-600">
            <input type="checkbox" checked={value.autoProvision} onChange={e => set({ autoProvision: e.target.checked })} className="accent-brand-600" />
            Let anyone the provider signs in use Cost Deck
          </label>
          <label className="flex items-center gap-2 text-sm text-slate-600">
            <input type="checkbox" checked={value.skipSslVerify} onChange={e => set({ skipSslVerify: e.target.checked })} className="accent-brand-600" />
            Skip TLS verification (insecure)
          </label>
        </div>
      </div>

      <div>
        <span className={label}>Group → role mapping</span>
        <div className="space-y-2">
          {value.mapping.map((m, i) => (
            <div key={i} className="flex items-center gap-2">
              <input value={m.group} aria-label="Group" placeholder="/costdeck-admins"
                onChange={e => set({ mapping: value.mapping.map((x, j) => (j === i ? { ...x, group: e.target.value } : x)) })} className={input} />
              <select value={m.role} aria-label="Role" onChange={e => set({ mapping: value.mapping.map((x, j) => (j === i ? { ...x, role: e.target.value } : x)) })} className={`${input} w-36`}>
                {['viewer', 'operator', 'admin'].map(r => <option key={r} value={r}>{r}</option>)}
              </select>
              <button type="button" title="Remove mapping" onClick={() => set({ mapping: value.mapping.filter((_, j) => j !== i) })} className="p-1 text-slate-400 hover:text-rose-600"><X size={14} /></button>
            </div>
          ))}
        </div>
        <Button size="sm" variant="secondary" icon={<Plus size={13} />} className="mt-2" onClick={() => set({ mapping: [...value.mapping, { group: '', role: 'viewer' }] })}>Add a group</Button>
      </div>

      {problem && <p className="text-xs font-medium text-rose-600">{problem}</p>}
      <div className="flex items-center gap-3">
        <button onClick={runTest} disabled={testing} className="flex items-center gap-2 px-4 py-2 bg-slate-100 hover:bg-slate-200 text-slate-700 rounded-lg text-xs font-bold disabled:opacity-50">
          {testing ? <RefreshCw size={14} className="animate-spin" /> : <ExternalLink size={14} />} Test Connection
        </button>
        {test && (
          <span className={`text-xs font-bold flex items-center gap-1 ${test.ok ? 'text-emerald-600' : 'text-rose-500'}`}>
            {test.ok ? <CheckCircle2 size={14} /> : <XCircle size={14} />}{test.text}
          </span>
        )}
      </div>
    </div>
  )
}
