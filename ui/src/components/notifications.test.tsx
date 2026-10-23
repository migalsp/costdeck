import { describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { json, mockFetch, on } from '../test/fetch'
import SettingsPage from '../pages/SettingsPage'
import ReportsPage from '../pages/ReportsPage'
import { AuthContext } from '../lib/auth'
import { toast } from '../lib/toast'
import { channelList, channelsForm, channelsProblem, channelsRequest } from '../lib/notifications'

const saved = {
  slack: { enabled: true, hasWebhook: true, events: ['alerts'] },
  teams: { enabled: false, hasWebhook: false, events: [] },
  email: { enabled: false, security: 'starttls', to: [], hasPassword: false, events: [] },
}

describe('Notification channels', () => {
  it('reads the saved channels and sends back only what changed', () => {
    const f = channelsForm(saved as never)
    expect(f.slack.events).toEqual(['alerts'])
    expect(f.teams.events).toEqual(['schedules', 'alerts', 'digests'])
    const req = channelsRequest({ ...f, email: { ...f.email, enabled: true, host: 'smtp.example.com', from: 'a@example.com', to: 'b@example.com; c@example.com,  d@example.com', password: 'pw' } })
    expect(req.slack).toEqual({ enabled: true, events: ['alerts'] })
    expect(req.email.to).toEqual(['b@example.com', 'c@example.com', 'd@example.com'])
    expect(req.email.password).toBe('pw')
    expect(channelsRequest(f).email).not.toHaveProperty('password')
  })

  it('says what keeps the form from saving', () => {
    const f = channelsForm()
    expect(channelsProblem(f)).toBeNull()
    expect(channelsProblem({ ...f, teams: { ...f.teams, enabled: true } })).toBe('Teams: paste the webhook URL')
    expect(channelsProblem({ ...f, slack: { ...f.slack, enabled: true, webhookUrl: 'hooks.slack.com/x' } })).toBe('Slack: the webhook URL starts with https://')
    expect(channelsProblem({ ...f, slack: { ...f.slack, enabled: true, hasWebhook: true, events: [] } })).toBe('Slack: pick at least one kind of message')
    const email = { ...f.email, enabled: true, host: 'smtp', from: 'a@example.com', to: 'b@example.com' }
    expect(channelsProblem({ ...f, email: { ...email, to: ' ' } })).toBe('Email: enter at least one recipient')
    expect(channelsProblem({ ...f, email: { ...email, username: 'u', security: 'none' } })).toMatch(/only sent over TLS/)
    expect(channelsProblem({ ...f, email })).toBeNull()
    expect(channelList(['Webex', 'Slack', 'email'])).toBe('Webex, Slack and email')
  })

  it('tests a webhook before saving and saves Slack with the Webex settings', async () => {
    window.history.replaceState(null, '', '/settings/notifications')
    const settings = { providers: {}, integrations: { notifications: saved }, auth: { disableLocalLogin: false }, calendars: [] }
    const fetch = mockFetch(
      on('GET', '/api/settings', () => json(settings)),
      on('PUT', '/api/settings', () => json(settings)),
      on('POST', '/api/settings/providers/teams/test', () => json({ connected: true, message: 'Sent a test message' })),
    )
    render(<SettingsPage section="notifications" />)
    const user = userEvent.setup()
    await user.click(await screen.findByRole('switch', { name: 'Enable Microsoft Teams' }))
    await user.type(screen.getByLabelText('Microsoft Teams webhook URL'), 'https://prod.westeurope.logic.azure.com/workflows/x')
    await user.click(screen.getByRole('checkbox', { name: 'teams Digests' }))
    const card = screen.getByLabelText('Microsoft Teams webhook URL').closest('.rounded-xl') as HTMLElement
    await user.click(within(card).getByRole('button', { name: 'Send test message' }))
    expect(await within(card).findByText('Sent a test message')).toBeTruthy()
    const test = fetch.mock.calls.find(([url]) => url === '/api/settings/providers/teams/test')!
    expect(JSON.parse(test[1]!.body as string)).toEqual({ teams: { enabled: true, events: ['schedules', 'alerts'], webhookUrl: 'https://prod.westeurope.logic.azure.com/workflows/x' } })

    await user.click(screen.getByRole('button', { name: 'Save' }))
    await screen.findByText('Saved')
    const put = JSON.parse(fetch.mock.calls.find(([, init]) => init?.method === 'PUT')![1]!.body as string)
    expect(put.integrations.notifications.teams.webhookUrl).toBe('https://prod.westeurope.logic.azure.com/workflows/x')
    expect(put.integrations.notifications.slack).toEqual({ enabled: true, events: ['alerts'] })
    expect(put.integrations).toHaveProperty('messenger')
  })

  it('does not save an email channel without recipients', async () => {
    window.history.replaceState(null, '', '/settings/notifications')
    const settings = { providers: {}, integrations: { notifications: saved }, auth: { disableLocalLogin: false }, calendars: [] }
    const fetch = mockFetch(on('GET', '/api/settings', () => json(settings)), on('PUT', '/api/settings', () => json(settings)))
    render(<SettingsPage section="notifications" />)
    const user = userEvent.setup()
    await user.click(await screen.findByRole('switch', { name: 'Enable Email' }))
    await user.type(screen.getByLabelText(/SMTP server/), 'smtp.example.com')
    await user.type(screen.getByLabelText('From'), 'costdeck@example.com')
    await user.click(screen.getByRole('button', { name: 'Save' }))
    expect(await screen.findByText('Not saved: Email: enter at least one recipient')).toBeTruthy()
    expect(fetch.mock.calls.some(([, init]) => init?.method === 'PUT')).toBe(false)
  })

  it('shows notifications as on in the settings menu when only Slack is set up', async () => {
    window.history.replaceState(null, '', '/settings/notifications')
    const settings = { providers: {}, integrations: { notifications: saved }, auth: { disableLocalLogin: false }, calendars: [] }
    mockFetch(on('GET', '/api/settings', () => json(settings)))
    render(<SettingsPage section="notifications" />)
    expect(await screen.findByRole('button', { name: /^Notifications\s*On$/ })).toBeTruthy()
  })

  it('says where a digest sent by hand went', async () => {
    const digest = { period: 'week', title: 'Cost digest · Oct 16 – Oct 22', hours: 0, currency: 'USD', namespaces: [], teams: [], opportunities: [] }
    mockFetch(
      on('GET', '/api/reports', () => json({ reports: [], schedule: {}, webexReady: false, channels: ['Slack', 'email'] })),
      on('GET', '/api/reports/digest', () => json({ digest, markdown: '**Cost digest**' })),
      on('POST', '/api/reports/digest/send', () => json({ id: 'r1', kind: 'digest', title: digest.title, delivered: 'Slack, email' })),
    )
    const success = vi.spyOn(toast, 'success')
    render(<AuthContext.Provider value={{ sub: 'user:ops', name: 'ops', role: 'operator', provider: 'local' }}><ReportsPage /></AuthContext.Provider>)
    await userEvent.setup().click(await screen.findByRole('button', { name: 'Send to Slack and email now' }))
    await waitFor(() => expect(success).toHaveBeenCalledWith('Sent the digest to Slack, email and saved it in the history'))
    // The digest goes wherever notifications are set up, not only to Webex.
    expect(screen.getByText(/^Not scheduled\. Turn it on to send the digest every week or month to the channels set up under Settings → Notifications\.$/)).toBeTruthy()
    expect(screen.queryByText(/Webex space/)).toBeNull()
  })
})
