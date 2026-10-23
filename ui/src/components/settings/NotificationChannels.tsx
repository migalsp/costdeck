import { useState } from 'react'
import { Mail } from 'lucide-react'
import { apiError, errorMessage } from '../../lib/api'
import { channelsRequest, notificationEvents, type ChannelsForm, type EmailForm, type NotificationEvent, type WebhookForm } from '../../lib/notifications'
import { SlackLogo, TeamsLogo } from '../ProviderLogos'
import { SecretInput, SettingsCard, TestButton, inputClass, labelClass } from './fields'

type Channel = 'slack' | 'teams' | 'email'

// NotificationChannels edits Slack, Teams and email next to the Webex card; the section's
// Save stores them.
export default function NotificationChannels({ value, onChange }: { value: ChannelsForm; onChange: (f: ChannelsForm) => void }) {
  const [testing, setTesting] = useState<Channel | null>(null)
  const [result, setResult] = useState<{ channel: Channel; ok: boolean; text: string } | null>(null)
  const setHook = (ch: 'slack' | 'teams', patch: Partial<WebhookForm>) => onChange({ ...value, [ch]: { ...value[ch], ...patch } })
  const setEmail = (patch: Partial<EmailForm>) => onChange({ ...value, email: { ...value.email, ...patch } })

  const test = async (ch: Channel) => {
    setTesting(ch)
    setResult(null)
    try {
      const body = { [ch]: channelsRequest(value)[ch] }
      const res = await fetch(`/api/settings/providers/${ch}/test`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) })
      if (!res.ok) throw new Error(await apiError(res))
      const d = await res.json()
      setResult({ channel: ch, ok: !!d.connected, text: d.connected ? d.message : d.error || 'Could not send' })
    } catch (e) {
      setResult({ channel: ch, ok: false, text: errorMessage(e) })
    } finally {
      setTesting(null)
    }
  }
  const outcome = (ch: Channel) => (result?.channel === ch ? { ok: result.ok, text: result.text } : null)

  const events = (ch: Channel, current: NotificationEvent[], set: (e: NotificationEvent[]) => void) => (
    <fieldset>
      <legend className={labelClass}>Send</legend>
      <div className="flex flex-wrap gap-x-5 gap-y-2">
        {notificationEvents.map(e => (
          <label key={e.id} className="flex items-start gap-2 text-sm text-slate-700" title={e.hint}>
            <input type="checkbox" className="mt-0.5 accent-brand-600" checked={current.includes(e.id)} aria-label={`${ch} ${e.label}`}
              onChange={ev => set(ev.target.checked ? [...current, e.id] : current.filter(x => x !== e.id))} />
            <span>{e.label} <span className="text-slate-400 text-xs">— {e.hint}</span></span>
          </label>
        ))}
      </div>
    </fieldset>
  )

  const webhookCard = (ch: 'slack' | 'teams', title: string, logo: React.ReactNode, help: React.ReactNode) => {
    const c = value[ch]
    return (
      <SettingsCard logo={logo} title={title} enabled={c.enabled} onToggle={v => setHook(ch, { enabled: v })}>
        <div>
          <label className={labelClass}>
            Webhook URL {c.hasWebhook && <span className="ml-2 text-emerald-500 normal-case font-medium text-xs">✓ Configured</span>}
          </label>
          <SecretInput value={c.webhookUrl} onChange={v => setHook(ch, { webhookUrl: v })} label={`${title} webhook URL`}
            placeholder={c.hasWebhook ? '•••••••••••••••• (leave empty to keep it)' : 'https://…'} />
          <p className="mt-1 text-[11px] text-slate-400">{help}</p>
        </div>
        {events(ch, c.events, e => setHook(ch, { events: e }))}
        <TestButton onTest={() => test(ch)} testing={testing === ch} result={outcome(ch)} label="Send test message" />
      </SettingsCard>
    )
  }

  const e = value.email
  return (
    <>
      {webhookCard('slack', 'Slack', <SlackLogo />, <>An incoming webhook of a Slack app (Features → Incoming Webhooks), which posts to one channel.</>)}
      {webhookCard('teams', 'Microsoft Teams', <TeamsLogo />, <>A Teams workflow "Post to a channel when a webhook request is received"; messages arrive as cards.</>)}
      <SettingsCard logo={<div className="w-10 h-10 rounded-xl bg-slate-100 flex items-center justify-center"><Mail size={20} className="text-slate-500" /></div>}
        title="Email" enabled={e.enabled} onToggle={v => setEmail({ enabled: v })}>
        <div className="grid grid-cols-1 sm:grid-cols-[1fr_7rem_10rem] gap-3">
          <div>
            <label className={labelClass} htmlFor="smtp-host">SMTP server</label>
            <input id="smtp-host" className={inputClass} value={e.host} onChange={ev => setEmail({ host: ev.target.value })} placeholder="smtp.example.com" />
          </div>
          <div>
            <label className={labelClass} htmlFor="smtp-port">Port</label>
            <input id="smtp-port" className={inputClass} inputMode="numeric" value={e.port} onChange={ev => setEmail({ port: ev.target.value })}
              placeholder={e.security === 'tls' ? '465' : '587'} />
          </div>
          <div>
            <label className={labelClass} htmlFor="smtp-security">Security</label>
            <select id="smtp-security" className={inputClass} value={e.security} onChange={ev => setEmail({ security: ev.target.value as EmailForm['security'] })}>
              <option value="starttls">STARTTLS</option>
              <option value="tls">TLS</option>
              <option value="none">None (relay in the cluster)</option>
            </select>
          </div>
        </div>
        <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
          <div>
            <label className={labelClass} htmlFor="smtp-from">From</label>
            <input id="smtp-from" className={inputClass} value={e.from} onChange={ev => setEmail({ from: ev.target.value })} placeholder="Cost Deck <costdeck@example.com>" />
          </div>
          <div>
            <label className={labelClass} htmlFor="smtp-to">To</label>
            <input id="smtp-to" className={inputClass} value={e.to} onChange={ev => setEmail({ to: ev.target.value })} placeholder="finops@example.com, platform@example.com" />
          </div>
        </div>
        <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
          <div>
            <label className={labelClass} htmlFor="smtp-user">Username <span className="normal-case font-normal text-slate-400">(empty: no sign-in)</span></label>
            <input id="smtp-user" className={inputClass} value={e.username} onChange={ev => setEmail({ username: ev.target.value })} autoComplete="off" />
          </div>
          <div>
            <label className={labelClass}>
              Password {e.hasPassword && <span className="ml-2 text-emerald-500 normal-case font-medium text-xs">✓ Configured</span>}
            </label>
            <SecretInput value={e.password} onChange={v => setEmail({ password: v })} label="SMTP password"
              placeholder={e.hasPassword ? '•••••••• (leave empty to keep it)' : ''} />
          </div>
        </div>
        {events('email', e.events, ev => setEmail({ events: ev }))}
        <TestButton onTest={() => test('email')} testing={testing === 'email'} result={outcome('email')} label="Send test email" />
      </SettingsCard>
    </>
  )
}
