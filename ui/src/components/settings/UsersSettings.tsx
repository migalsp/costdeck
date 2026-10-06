import { useState } from 'react'
import { Check, Copy, KeyRound, Loader2, Pencil, Plus, ShieldCheck, Trash2, UserX, UserCheck } from 'lucide-react'
import { Badge, Button, Modal } from '../ui'
import { apiError, errorMessage } from '../../lib/api'
import { roleHelp, useAuth, type Role } from '../../lib/auth'
import { ago } from '../../lib/time'
import { usePolling } from '../../lib/usePolling'

interface LocalUser {
  username: string
  name?: string
  email?: string
  role: Role
  disabled?: boolean
  mustChangePassword?: boolean
  createdAt: string
  createdBy?: string
  passwordChangedAt: string
}

interface SignIn {
  subject: string
  name: string
  email?: string
  provider: 'local' | 'entra'
  role: Role
  at: string
  count: number
}

interface UsersResponse {
  builtin: { username: string; secret: string } | null
  users: LocalUser[]
  signIns: SignIn[]
  sso: boolean
  minPasswordLength: number
}

const roles: Role[] = ['viewer', 'operator', 'admin']
const field = 'w-full px-3 py-2 bg-white border border-slate-200 rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-brand-500/30 focus:border-brand-400'
const label = 'text-xs font-semibold text-slate-600 mb-1 block'

async function send(method: string, url: string, body?: unknown) {
  const res = await fetch(url, {
    method,
    headers: body === undefined ? undefined : { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  if (!res.ok) throw new Error(await apiError(res))
  return res.status === 204 ? null : res.json()
}

type Dialog =
  | { kind: 'add' }
  | { kind: 'edit'; user: LocalUser }
  | { kind: 'reset'; user: LocalUser }
  | { kind: 'delete'; user: LocalUser }
  | { kind: 'password'; username: string; password: string; created: boolean }

// UsersSettings manages the people who sign in with a password and shows who signed in,
// single sign-on users included.
export default function UsersSettings({ onOpenSSO }: { onOpenSSO: () => void }) {
  const { user: me } = useAuth()
  const [data, setData] = useState<UsersResponse | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [dialog, setDialog] = useState<Dialog | null>(null)
  const [busy, setBusy] = useState<string | null>(null)

  const load = () => fetch('/api/users')
    .then(async r => { if (!r.ok) throw new Error(await apiError(r)); return r.json() })
    .then((d: UsersResponse) => { setData(d); setError(null) })
    .catch(e => setError(errorMessage(e)))
  usePolling(load, 60000)

  const toggle = async (u: LocalUser) => {
    setBusy(u.username)
    try {
      await send('PATCH', `/api/users/${encodeURIComponent(u.username)}`, { disabled: !u.disabled })
      await load()
    } catch (e) {
      setError(errorMessage(e))
    } finally {
      setBusy(null)
    }
  }

  if (!data) {
    return error ? <p className="text-sm text-rose-600">Could not load users: {error}</p>
      : <div className="flex items-center gap-2 text-sm text-slate-400"><Loader2 size={16} className="animate-spin" /> Loading…</div>
  }

  const lastSignIn = (subject: string) => data.signIns.find(s => s.subject === subject)
  const isMe = (u: LocalUser) => me?.sub === `user:${u.username}`
  const sso = data.signIns.filter(s => s.provider === 'entra')

  return (
    <div className="space-y-6">
      {error && <p className="text-sm text-rose-600">{error}</p>}

      <div className="rounded-xl border border-slate-200 bg-white shadow-sm">
        <div className="flex flex-wrap items-center gap-3 px-5 py-4 border-b border-slate-100">
          <div className="min-w-0 flex-1">
            <h4 className="font-bold text-slate-800">Local users</h4>
            <p className="text-sm text-slate-500">For people without single sign-on. Each gets a role and a password of at least {data.minPasswordLength} characters.</p>
          </div>
          <Button variant="primary" icon={<Plus size={15} />} onClick={() => setDialog({ kind: 'add' })}>Add user</Button>
        </div>
        <div className="overflow-x-auto">
          <table className="w-full text-sm">
            <thead className="text-xs text-slate-500 bg-slate-50">
              <tr>
                <th className="text-left font-semibold px-5 py-2.5">User</th>
                <th className="text-left font-semibold px-3 py-2.5">Role</th>
                <th className="text-left font-semibold px-3 py-2.5">Status</th>
                <th className="text-left font-semibold px-3 py-2.5 whitespace-nowrap">Last sign-in</th>
                <th className="px-5 py-2.5" />
              </tr>
            </thead>
            <tbody>
              {data.builtin && (
                <tr className="border-t border-slate-100">
                  <td className="px-5 py-3">
                    <div className="font-medium text-slate-800 flex items-center gap-1.5"><ShieldCheck size={14} className="text-slate-400" />{data.builtin.username}{me?.sub === `local:${data.builtin.username}` && <span className="text-xs font-normal text-slate-400">(you)</span>}</div>
                    <div className="text-xs text-slate-500">Built-in admin for break-glass access · password in Secret <code className="font-mono">{data.builtin.secret}</code></div>
                  </td>
                  <td className="px-3 py-3"><RoleLabel role="admin" /></td>
                  <td className="px-3 py-3 text-xs text-slate-500 whitespace-nowrap" title="Set by the Helm chart; it cannot be edited or disabled here">Managed by Helm</td>
                  <td className="px-3 py-3 text-xs text-slate-500 whitespace-nowrap"><LastSeen s={lastSignIn(`local:${data.builtin.username}`)} /></td>
                  <td className="px-5 py-3" />
                </tr>
              )}
              {data.users.map(u => (
                <tr key={u.username} className={`border-t border-slate-100 ${u.disabled ? 'bg-slate-50/60' : ''}`}>
                  <td className="px-5 py-3">
                    <div className={`font-medium ${u.disabled ? 'text-slate-400' : 'text-slate-800'}`}>{u.name || u.username}{isMe(u) && <span className="ml-1.5 text-xs font-normal text-slate-400">(you)</span>}</div>
                    <div className="text-xs text-slate-500">{u.username}{u.email ? ` · ${u.email}` : ''}</div>
                  </td>
                  <td className="px-3 py-3"><RoleLabel role={u.role} /></td>
                  <td className="px-3 py-3">
                    {u.disabled ? <Badge>Disabled</Badge>
                      : u.mustChangePassword ? <span className="whitespace-nowrap"><Badge tone="info" title="Signs in with the password you gave them, then chooses a new one">Sets password at sign-in</Badge></span>
                      : <span className="text-xs text-slate-600">Active</span>}
                  </td>
                  <td className="px-3 py-3 text-xs text-slate-500 whitespace-nowrap"><LastSeen s={lastSignIn(`user:${u.username}`)} /></td>
                  <td className="px-5 py-3">
                    <div className="flex justify-end gap-1">
                      <IconButton title="Edit" onClick={() => setDialog({ kind: 'edit', user: u })}><Pencil size={15} /></IconButton>
                      <IconButton title="Reset password" onClick={() => setDialog({ kind: 'reset', user: u })}><KeyRound size={15} /></IconButton>
                      {!isMe(u) && (
                        <IconButton title={u.disabled ? 'Enable' : 'Disable: signs them out and blocks sign-in'} onClick={() => toggle(u)} disabled={busy === u.username}>
                          {u.disabled ? <UserCheck size={15} /> : <UserX size={15} />}
                        </IconButton>
                      )}
                      {!isMe(u) && <IconButton title="Delete" danger onClick={() => setDialog({ kind: 'delete', user: u })}><Trash2 size={15} /></IconButton>}
                    </div>
                  </td>
                </tr>
              ))}
              {data.users.length === 0 && (
                <tr className="border-t border-slate-100"><td colSpan={5} className="px-5 py-6 text-center text-sm text-slate-400">No local users yet. Add one for each person who should sign in with a password.</td></tr>
              )}
            </tbody>
          </table>
        </div>
      </div>

      <div className="rounded-xl border border-slate-200 bg-white shadow-sm">
        <div className="px-5 py-4 border-b border-slate-100">
          <h4 className="font-bold text-slate-800">Single sign-on users</h4>
          <p className="text-sm text-slate-500">
            {data.sso
              ? <>People who sign in with Microsoft need no account here: their role comes from their groups. <button className="font-semibold text-brand-700 hover:underline" onClick={onOpenSSO}>Edit group mappings</button></>
              : <>Single sign-on is off. <button className="font-semibold text-brand-700 hover:underline" onClick={onOpenSSO}>Connect Microsoft Entra ID</button> to let people sign in with their work account.</>}
          </p>
        </div>
        {sso.length > 0 ? (
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead className="text-xs text-slate-500 bg-slate-50">
                <tr>
                  <th className="text-left font-semibold px-5 py-2.5">User</th>
                  <th className="text-left font-semibold px-3 py-2.5">Role at last sign-in</th>
                  <th className="text-left font-semibold px-3 py-2.5">Last sign-in</th>
                  <th className="text-right font-semibold px-5 py-2.5">Sign-ins</th>
                </tr>
              </thead>
              <tbody>
                {sso.map(s => (
                  <tr key={s.subject} className="border-t border-slate-100">
                    <td className="px-5 py-3"><div className="font-medium text-slate-800">{s.name}</div>{s.email && <div className="text-xs text-slate-500">{s.email}</div>}</td>
                    <td className="px-3 py-3"><RoleLabel role={s.role} /></td>
                    <td className="px-3 py-3 text-xs text-slate-500"><LastSeen s={s} /></td>
                    <td className="px-5 py-3 text-right tabular-nums text-slate-600">{s.count}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : data.sso && <p className="px-5 py-6 text-center text-sm text-slate-400">Nobody has signed in with Microsoft yet.</p>}
      </div>

      {dialog?.kind === 'add' && (
        <UserForm min={data.minPasswordLength} onClose={() => setDialog(null)} onDone={(username, password) => {
          load()
          setDialog(password ? { kind: 'password', username, password, created: true } : null)
        }} />
      )}
      {dialog?.kind === 'edit' && (
        <UserForm min={data.minPasswordLength} user={dialog.user} self={isMe(dialog.user)} onClose={() => setDialog(null)} onDone={() => { load(); setDialog(null) }} />
      )}
      {dialog?.kind === 'reset' && (
        <ResetPassword user={dialog.user} min={data.minPasswordLength} onClose={() => setDialog(null)} onDone={password => {
          load()
          setDialog(password ? { kind: 'password', username: dialog.user.username, password, created: false } : null)
        }} />
      )}
      {dialog?.kind === 'delete' && (
        <ConfirmDelete user={dialog.user} onClose={() => setDialog(null)} onDone={() => { load(); setDialog(null) }} />
      )}
      {dialog?.kind === 'password' && <OneTimePassword {...dialog} onClose={() => setDialog(null)} />}
    </div>
  )
}

function RoleLabel({ role }: { role: Role }) {
  return <span className="text-sm text-slate-700 capitalize" title={roleHelp[role]}>{role}</span>
}

function LastSeen({ s }: { s?: SignIn }) {
  if (!s) return <span className="text-slate-400">Never</span>
  return <span title={new Date(s.at).toLocaleString()}>{ago(s.at)}</span>
}

function IconButton({ title, onClick, children, danger, disabled }: { title: string; onClick: () => void; children: React.ReactNode; danger?: boolean; disabled?: boolean }) {
  return (
    <button type="button" title={title} aria-label={title} onClick={onClick} disabled={disabled}
      className={`p-1.5 rounded-lg text-slate-400 disabled:opacity-40 ${danger ? 'hover:text-rose-600 hover:bg-rose-50' : 'hover:text-slate-700 hover:bg-slate-100'}`}>
      {children}
    </button>
  )
}

function RolePicker({ value, onChange, disabled }: { value: Role; onChange: (r: Role) => void; disabled?: boolean }) {
  return (
    <div className="grid gap-2">
      {roles.map(r => (
        <label key={r} className={`flex items-start gap-3 p-3 rounded-lg border cursor-pointer ${value === r ? 'border-brand-400 bg-brand-50/50' : 'border-slate-200 hover:border-slate-300'} ${disabled ? 'opacity-60 pointer-events-none' : ''}`}>
          <input type="radio" name="role" className="mt-1 accent-brand-600" checked={value === r} onChange={() => onChange(r)} disabled={disabled} />
          <span><span className="block text-sm font-semibold text-slate-800 capitalize">{r}</span><span className="block text-xs text-slate-500">{roleHelp[r]}</span></span>
        </label>
      ))}
    </div>
  )
}

// UserForm adds a user, or edits one when `user` is given.
function UserForm({ user, self, min, onClose, onDone }: {
  user?: LocalUser; self?: boolean; min: number; onClose: () => void; onDone: (username: string, password?: string) => void
}) {
  const [username, setUsername] = useState(user?.username || '')
  const [name, setName] = useState(user?.name || '')
  const [email, setEmail] = useState(user?.email || '')
  const [role, setRole] = useState<Role>(user?.role || 'viewer')
  const [mode, setMode] = useState<'generate' | 'set'>('generate')
  const [password, setPassword] = useState('')
  const [mustChange, setMustChange] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  const valid = user ? true : /^[a-z0-9]([a-z0-9._-]{0,61}[a-z0-9])?$/.test(username.trim().toLowerCase()) && (mode === 'generate' || password.length >= min)
  const submit = async () => {
    setBusy(true)
    setError(null)
    try {
      if (user) {
        await send('PATCH', `/api/users/${encodeURIComponent(user.username)}`, { name, email, ...(self ? {} : { role }) })
        onDone(user.username)
      } else {
        const res = await send('POST', '/api/users', { username: username.trim().toLowerCase(), name, email, role, mustChangePassword: mustChange, ...(mode === 'set' ? { password } : {}) })
        onDone(res.user.username, res.password)
      }
    } catch (e) {
      setError(errorMessage(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal title={user ? `Edit ${user.name || user.username}` : 'Add a user'} onClose={onClose} dismissOnBackdrop={false}
      footer={<>
        <Button onClick={onClose}>Cancel</Button>
        <Button variant="primary" onClick={submit} disabled={!valid || busy}>{user ? 'Save' : 'Add user'}</Button>
      </>}>
      <div className="space-y-4">
        <div className="grid gap-4 sm:grid-cols-2">
          <div>
            <label className={label}>Username</label>
            <input className={`${field} font-mono`} value={username} onChange={e => setUsername(e.target.value)} disabled={!!user} placeholder="anna" autoFocus={!user} />
            {!user && <p className="mt-1 text-xs text-slate-400">Lower-case letters, digits, dots and dashes. Used to sign in.</p>}
          </div>
          <div>
            <label className={label}>Display name <span className="font-normal text-slate-400">(optional)</span></label>
            <input className={field} value={name} onChange={e => setName(e.target.value)} placeholder="Anna Petrova" />
          </div>
        </div>
        <div>
          <label className={label}>Email <span className="font-normal text-slate-400">(optional)</span></label>
          <input className={field} type="email" value={email} onChange={e => setEmail(e.target.value)} placeholder="anna@example.com" />
        </div>
        <div>
          <label className={label}>Role</label>
          <RolePicker value={role} onChange={setRole} disabled={self} />
          {self && <p className="mt-1 text-xs text-slate-400">You cannot change your own role.</p>}
        </div>
        {!user && (
          <div>
            <label className={label}>Password</label>
            <div className="flex gap-1 mb-2">
              {(['generate', 'set'] as const).map(m => (
                <button key={m} type="button" onClick={() => setMode(m)}
                  className={`px-3 py-1.5 rounded-lg text-xs font-semibold border ${mode === m ? 'bg-slate-900 text-white border-slate-900' : 'bg-white text-slate-600 border-slate-200'}`}>
                  {m === 'generate' ? 'Generate one' : 'Type one'}
                </button>
              ))}
            </div>
            {mode === 'set' && <input className={field} type="password" autoComplete="new-password" value={password} onChange={e => setPassword(e.target.value)} placeholder={`At least ${min} characters`} />}
            {mode === 'generate' && <p className="text-xs text-slate-500">You will see it once, to hand over.</p>}
            <label className="mt-3 flex items-start gap-2 text-sm text-slate-700">
              <input type="checkbox" className="mt-0.5 accent-brand-600" checked={mustChange} onChange={e => setMustChange(e.target.checked)} />
              <span>Ask them to choose their own password at the first sign-in</span>
            </label>
          </div>
        )}
        {error && <p className="text-sm text-rose-600">{error}</p>}
      </div>
    </Modal>
  )
}

function ResetPassword({ user, min, onClose, onDone }: { user: LocalUser; min: number; onClose: () => void; onDone: (password?: string) => void }) {
  const [mode, setMode] = useState<'generate' | 'set'>('generate')
  const [password, setPassword] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const submit = async () => {
    setBusy(true)
    setError(null)
    try {
      const res = await send('POST', `/api/users/${encodeURIComponent(user.username)}/password`, mode === 'set' ? { password } : {})
      onDone(res?.password)
    } catch (e) {
      setError(errorMessage(e))
    } finally {
      setBusy(false)
    }
  }
  return (
    <Modal title={`Reset the password of ${user.name || user.username}`} size="sm" onClose={onClose} dismissOnBackdrop={false}
      footer={<>
        <Button onClick={onClose}>Cancel</Button>
        <Button variant="primary" onClick={submit} disabled={busy || (mode === 'set' && password.length < min)}>Reset password</Button>
      </>}>
      <div className="space-y-3 text-sm text-slate-600">
        <p>They are signed out everywhere and choose a new password at their next sign-in.</p>
        <div className="flex gap-1">
          {(['generate', 'set'] as const).map(m => (
            <button key={m} type="button" onClick={() => setMode(m)}
              className={`px-3 py-1.5 rounded-lg text-xs font-semibold border ${mode === m ? 'bg-slate-900 text-white border-slate-900' : 'bg-white text-slate-600 border-slate-200'}`}>
              {m === 'generate' ? 'Generate one' : 'Type one'}
            </button>
          ))}
        </div>
        {mode === 'set' && <input className={field} type="password" autoComplete="new-password" value={password} onChange={e => setPassword(e.target.value)} placeholder={`At least ${min} characters`} />}
        {error && <p className="text-rose-600">{error}</p>}
      </div>
    </Modal>
  )
}

function ConfirmDelete({ user, onClose, onDone }: { user: LocalUser; onClose: () => void; onDone: () => void }) {
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const submit = async () => {
    setBusy(true)
    try {
      await send('DELETE', `/api/users/${encodeURIComponent(user.username)}`)
      onDone()
    } catch (e) {
      setError(errorMessage(e))
      setBusy(false)
    }
  }
  return (
    <Modal title={`Delete ${user.name || user.username}?`} size="sm" onClose={onClose}
      footer={<>
        <Button onClick={onClose}>Cancel</Button>
        <Button variant="danger" icon={<Trash2 size={14} />} onClick={submit} disabled={busy}>Delete user</Button>
      </>}>
      <p className="text-sm text-slate-600">They are signed out at once and can no longer sign in. API tokens they created keep working until you revoke them under API tokens. To keep the account for later, disable it instead.</p>
      {error && <p className="mt-2 text-sm text-rose-600">{error}</p>}
    </Modal>
  )
}

function OneTimePassword({ username, password, created, onClose }: { username: string; password: string; created: boolean; onClose: () => void }) {
  const [copied, setCopied] = useState(false)
  return (
    <Modal title={created ? `${username} was added` : `New password for ${username}`} size="sm" onClose={onClose} dismissOnBackdrop={false}
      footer={<Button variant="primary" onClick={onClose}>Done</Button>}>
      <div className="space-y-3 text-sm text-slate-600">
        <p>Hand this password over securely. It is shown only now; they choose their own at the first sign-in.</p>
        <div className="flex gap-2">
          <code className="flex-1 px-3 py-2 rounded-lg border border-slate-200 bg-slate-50 font-mono text-sm text-slate-800 break-all">{password}</code>
          <Button icon={copied ? <Check size={14} /> : <Copy size={14} />} onClick={() => { navigator.clipboard.writeText(`${username} / ${password}`).then(() => setCopied(true)).catch(() => {}) }}>
            {copied ? 'Copied' : 'Copy'}
          </Button>
        </div>
        <p className="text-xs text-slate-400">Copy places “username / password” on the clipboard.</p>
      </div>
    </Modal>
  )
}
