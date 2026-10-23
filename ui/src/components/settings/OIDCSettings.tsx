import { useState } from 'react'
import { KeyRound, Plus, X } from 'lucide-react'
import { apiError, errorMessage } from '../../lib/api'
import { keycloakDefaults, oidcProblem, oidcRequest, type OIDCForm } from '../../lib/oidc'
import { Button } from '../ui'
import { CardIcon, SettingsCard, TestButton, checkClass, hintClass, inputClass, labelClass } from './fields'

const input = `${inputClass} font-mono`
const label = labelClass

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
      {extra?.hint && <p className={hintClass}>{extra.hint}</p>}
    </div>
  )

  return (
    <div data-testid="oidc-settings">
    <SettingsCard
      logo={<CardIcon><KeyRound size={20} /></CardIcon>}
      title="OpenID Connect"
      subtitle="Keycloak, Okta, Auth0, Dex…: authorization code flow with PKCE"
      status={value.enabled && <Button size="sm" variant="secondary" onClick={() => onChange(keycloakDefaults(value))}>Keycloak defaults</Button>}
      enabled={value.enabled}
      onToggle={v => set({ enabled: v })}
    >
      <div className="p-4 bg-slate-50 border border-slate-200 rounded-lg text-xs text-slate-600 space-y-1.5">
        <p className="font-semibold">Redirect URI to register with the provider:</p>
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
          <label className={checkClass}>
            <input type="checkbox" checked={value.autoProvision} onChange={e => set({ autoProvision: e.target.checked })} className="accent-brand-600" />
            Let anyone the provider signs in use Cost Deck
          </label>
          <label className={checkClass}>
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
      <TestButton onTest={runTest} testing={testing} result={test} />
    </SettingsCard>
    </div>
  )
}
