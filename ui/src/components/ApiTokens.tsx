import { useCallback, useEffect, useState } from 'react'
import { KeyRound, Trash2, Copy, Check, Plus } from 'lucide-react'
import { apiError } from '../lib/api'

interface TokenInfo {
  name: string
  role: 'viewer' | 'operator' | 'admin'
  createdAt: string
  createdBy?: string
  expiresAt?: string
}

// ApiTokens manages bearer tokens for MCP clients, CI pipelines and scripts.
export default function ApiTokens() {
  const [tokens, setTokens] = useState<TokenInfo[]>([])
  const [name, setName] = useState('')
  const [role, setRole] = useState('viewer')
  const [days, setDays] = useState(90)
  const [created, setCreated] = useState<string | null>(null)
  const [copied, setCopied] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const load = useCallback(async () => {
    const res = await fetch('/api/tokens')
    if (res.ok) setTokens(await res.json())
  }, [])

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect -- loading data on mount
    load()
  }, [load])

  const create = async () => {
    setError(null)
    const res = await fetch('/api/tokens', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ name: name.trim(), role, expiresInDays: days }),
    })
    if (!res.ok) {
      setError(await apiError(res))
      return
    }
    const data = await res.json()
    setCreated(data.token)
    setCopied(false)
    setName('')
    load()
  }

  const revoke = async (tokenName: string) => {
    const res = await fetch(`/api/tokens/${encodeURIComponent(tokenName)}`, { method: 'DELETE' })
    if (!res.ok) setError(await apiError(res))
    load()
  }

  return (
    <div className="bg-white rounded-2xl border border-slate-200 shadow-sm p-6 space-y-4">
      <div className="flex items-center gap-3">
        <div className="w-10 h-10 rounded-xl bg-slate-100 flex items-center justify-center"><KeyRound size={18} className="text-slate-600" /></div>
        <div>
          <span className="font-bold text-slate-800">API tokens</span>
          <p className="text-[10px] text-slate-400">For MCP clients, CI and scripts: <code>Authorization: Bearer cdk_…</code>. Only a SHA-256 of each token is stored.</p>
        </div>
      </div>

      {created && (
        <div className="p-4 rounded-xl border border-emerald-200 bg-emerald-50">
          <p className="text-xs font-bold text-emerald-800 mb-2">Copy this token now — it will not be shown again.</p>
          <div className="flex gap-2">
            <code className="flex-1 px-3 py-2 bg-white rounded-lg text-xs font-mono break-all border border-emerald-100">{created}</code>
            <button onClick={() => { navigator.clipboard.writeText(created); setCopied(true) }}
              className="px-3 py-2 bg-emerald-500 text-white rounded-lg text-xs font-bold flex items-center gap-1">
              {copied ? <Check size={14} /> : <Copy size={14} />} {copied ? 'Copied' : 'Copy'}
            </button>
          </div>
        </div>
      )}

      <div className="grid grid-cols-[1fr_140px_120px_auto] gap-2 items-end">
        <div>
          <label className="text-[10px] font-bold uppercase tracking-wider text-slate-500 mb-1 block">Name</label>
          <input value={name} onChange={e => setName(e.target.value)} placeholder="claude-desktop"
            className="w-full px-3 py-2 bg-white border border-slate-200 rounded-lg text-xs font-mono" />
        </div>
        <div>
          <label className="text-[10px] font-bold uppercase tracking-wider text-slate-500 mb-1 block">Role</label>
          <select value={role} onChange={e => setRole(e.target.value)} className="w-full px-3 py-2 bg-white border border-slate-200 rounded-lg text-xs">
            <option value="viewer">viewer</option>
            <option value="operator">operator</option>
            <option value="admin">admin</option>
          </select>
        </div>
        <div>
          <label className="text-[10px] font-bold uppercase tracking-wider text-slate-500 mb-1 block">Expires (days)</label>
          <input type="number" min={0} max={3650} value={days} onChange={e => setDays(parseInt(e.target.value) || 0)}
            className="w-full px-3 py-2 bg-white border border-slate-200 rounded-lg text-xs" title="0 = never" />
        </div>
        <button onClick={create} disabled={!name.trim()}
          className="px-3 py-2 bg-slate-800 hover:bg-slate-900 text-white rounded-lg text-xs font-bold flex items-center gap-1 disabled:opacity-50">
          <Plus size={14} /> Create
        </button>
      </div>
      {error && <p className="text-xs text-red-500">{error}</p>}

      {tokens.length > 0 && (
        <table className="w-full text-xs">
          <thead>
            <tr className="text-left text-[10px] uppercase tracking-wider text-slate-400">
              <th className="py-1">Name</th><th>Role</th><th>Created</th><th>Expires</th><th />
            </tr>
          </thead>
          <tbody>
            {tokens.map(t => (
              <tr key={t.name} className="border-t border-slate-100">
                <td className="py-2 font-mono">{t.name}</td>
                <td>{t.role}</td>
                <td title={t.createdBy}>{new Date(t.createdAt).toLocaleDateString()}</td>
                <td>{t.expiresAt ? new Date(t.expiresAt).toLocaleDateString() : 'never'}</td>
                <td className="text-right">
                  <button onClick={() => revoke(t.name)} className="p-1 text-slate-300 hover:text-red-500" title="Revoke"><Trash2 size={14} /></button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  )
}
