// Notification channels besides Webex: Slack and Microsoft Teams through incoming
// webhooks, and email through SMTP. Mirrors NotificationsSettingsResponse and
// NotificationsUpdateRequest in internal/api/notification_settings.go.

export type NotificationEvent = 'schedules' | 'alerts' | 'digests'

export const notificationEvents: { id: NotificationEvent; label: string; hint: string }[] = [
  { id: 'schedules', label: 'Scaling changes', hint: 'a schedule finished scaling up or down' },
  { id: 'alerts', label: 'Alerts', hint: 'budgets, cost anomalies, namespaces about to be deleted' },
  { id: 'digests', label: 'Digests', hint: 'the scheduled cost summary' },
]

export interface WebhookChannelSettings { enabled: boolean; hasWebhook: boolean; events: NotificationEvent[] }
export interface EmailChannelSettings {
  enabled: boolean; host?: string; port?: number; security: 'starttls' | 'tls' | 'none'
  from?: string; to: string[]; username?: string; hasPassword: boolean; events: NotificationEvent[]
}
export interface NotificationsSettings { slack: WebhookChannelSettings; teams: WebhookChannelSettings; email: EmailChannelSettings }

// The form keeps every event ticked when the server stores none (none means all), and the
// secrets the person typed, which are never read back.
export interface WebhookForm { enabled: boolean; hasWebhook: boolean; webhookUrl: string; events: NotificationEvent[] }
export interface EmailForm {
  enabled: boolean; host: string; port: string; security: 'starttls' | 'tls' | 'none'
  from: string; to: string; username: string; password: string; hasPassword: boolean; events: NotificationEvent[]
}
export interface ChannelsForm { slack: WebhookForm; teams: WebhookForm; email: EmailForm }

const all = (): NotificationEvent[] => notificationEvents.map(e => e.id)
const events = (e?: NotificationEvent[]) => (e && e.length ? e : all())

export function channelsForm(s?: NotificationsSettings): ChannelsForm {
  const hook = (c?: WebhookChannelSettings): WebhookForm => ({ enabled: !!c?.enabled, hasWebhook: !!c?.hasWebhook, webhookUrl: '', events: events(c?.events) })
  const e = s?.email
  return {
    slack: hook(s?.slack),
    teams: hook(s?.teams),
    email: {
      enabled: !!e?.enabled, host: e?.host || '', port: e?.port ? String(e.port) : '', security: e?.security || 'starttls',
      from: e?.from || '', to: (e?.to || []).join(', '), username: e?.username || '', password: '', hasPassword: !!e?.hasPassword, events: events(e?.events),
    },
  }
}

const recipients = (to: string) => to.split(/[\s,;]+/).map(s => s.trim()).filter(Boolean)

// channelsRequest is what Save sends: unchanged secrets are left out, so they stay stored.
export function channelsRequest(f: ChannelsForm) {
  const hook = (c: WebhookForm) => ({ enabled: c.enabled, events: c.events, ...(c.webhookUrl.trim() ? { webhookUrl: c.webhookUrl.trim() } : {}) })
  const e = f.email
  return {
    slack: hook(f.slack),
    teams: hook(f.teams),
    email: {
      enabled: e.enabled, host: e.host.trim(), port: e.port.trim() ? Number(e.port) : 0, security: e.security,
      from: e.from.trim(), to: recipients(e.to), username: e.username.trim(), events: e.events,
      ...(e.password ? { password: e.password } : {}),
    },
  }
}

// channelsProblem names what keeps the form from saving, in the words of the form.
export function channelsProblem(f: ChannelsForm): string | null {
  for (const [name, c] of [['Slack', f.slack], ['Teams', f.teams]] as const) {
    if (!c.enabled) continue
    if (!c.hasWebhook && !c.webhookUrl.trim()) return `${name}: paste the webhook URL`
    if (c.webhookUrl.trim() && !/^https?:\/\/[^\s/]+/.test(c.webhookUrl.trim())) return `${name}: the webhook URL starts with https://`
    if (c.events.length === 0) return `${name}: pick at least one kind of message`
  }
  const e = f.email
  if (e.enabled) {
    if (!e.host.trim()) return 'Email: enter the SMTP server'
    if (!e.from.trim()) return 'Email: enter the sender address'
    if (recipients(e.to).length === 0) return 'Email: enter at least one recipient'
    if (e.port.trim() && !(Number(e.port) >= 1 && Number(e.port) <= 65535)) return 'Email: the port is a number from 1 to 65535'
    if (e.username.trim() && e.security === 'none') return 'Email: a password is only sent over TLS; choose STARTTLS or TLS'
    if (e.events.length === 0) return 'Email: pick at least one kind of message'
  }
  return null
}

// channelsSummary says where a kind of message goes, for "Send now" buttons and hints.
export function channelList(channels: string[] | undefined): string {
  const c = channels || []
  if (c.length <= 1) return c[0] || ''
  return `${c.slice(0, -1).join(', ')} and ${c[c.length - 1]}`
}
