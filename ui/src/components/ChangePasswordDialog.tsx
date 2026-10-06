import { useState } from 'react'
import { Button, Modal } from './ui'
import { apiError, errorMessage } from '../lib/api'

const MIN_LENGTH = 12
const field = 'w-full px-3 py-2 bg-white border border-slate-200 rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-brand-500/30 focus:border-brand-400'

// ChangePasswordDialog lets a local user choose a new password. `required` is the first
// sign-in after an administrator set the password: the dialog cannot be dismissed, only
// left by signing out.
export default function ChangePasswordDialog({ required, onDone, onClose, onSignOut }: {
  required?: boolean
  onDone: () => void
  onClose: () => void
  onSignOut: () => void
}) {
  const [current, setCurrent] = useState('')
  const [next, setNext] = useState('')
  const [confirm, setConfirm] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  const problem = next.length > 0 && next.length < MIN_LENGTH ? `At least ${MIN_LENGTH} characters`
    : confirm.length > 0 && confirm !== next ? 'The two passwords differ'
    : next.length > 0 && next === current ? 'Choose a password different from the current one'
    : null
  const ready = current.length > 0 && next.length >= MIN_LENGTH && next === confirm && next !== current

  const submit = async () => {
    setBusy(true)
    setError(null)
    try {
      const res = await fetch('/api/auth/password', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ currentPassword: current, newPassword: next }),
      })
      if (!res.ok) throw new Error(await apiError(res))
      onDone()
    } catch (e) {
      setError(errorMessage(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal
      title={required ? 'Choose your own password' : 'Change password'}
      subtitle={required ? 'An administrator set your current password. Pick a new one to continue.' : 'Your other sessions are signed out.'}
      size="sm"
      onClose={required ? onSignOut : onClose}
      dismissOnBackdrop={false}
      footer={<>
        {required ? <Button onClick={onSignOut}>Sign out</Button> : <Button onClick={onClose}>Cancel</Button>}
        <Button variant="primary" onClick={submit} disabled={!ready || busy}>Change password</Button>
      </>}>
      <form className="space-y-3" onSubmit={e => { e.preventDefault(); if (ready) submit() }}>
        <div>
          <label className="text-xs font-semibold text-slate-600 mb-1 block">{required ? 'Password you were given' : 'Current password'}</label>
          <input className={field} type="password" autoComplete="current-password" value={current} onChange={e => setCurrent(e.target.value)} autoFocus />
        </div>
        <div>
          <label className="text-xs font-semibold text-slate-600 mb-1 block">New password</label>
          <input className={field} type="password" autoComplete="new-password" value={next} onChange={e => setNext(e.target.value)} placeholder={`At least ${MIN_LENGTH} characters`} />
        </div>
        <div>
          <label className="text-xs font-semibold text-slate-600 mb-1 block">New password again</label>
          <input className={field} type="password" autoComplete="new-password" value={confirm} onChange={e => setConfirm(e.target.value)} />
        </div>
        {(problem || error) && <p className={`text-sm ${error ? 'text-rose-600' : 'text-slate-500'}`}>{error || problem}</p>}
        <button type="submit" hidden />
      </form>
    </Modal>
  )
}
