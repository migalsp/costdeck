import { describe, expect, it } from 'vitest'
import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { json, mockFetch, on } from '../test/fetch'
import SettingsPage from './SettingsPage'

const settings = { providers: {}, integrations: {}, calendars: [], auth: { disableLocalLogin: false }, billing: {} }

// The on/off switches of every card, by section. Each must be a named switch, so all cards
// look and behave the same and a screen reader can tell which one it is.
const switches: Record<string, string[]> = {
  sso: ['Enable Microsoft Entra ID', 'Enable OpenID Connect'],
  pricing: ['Enable Cloud list prices'],
  billing: ['Enable Reconcile with the cloud bill'],
  metrics: ['Enable VictoriaMetrics'],
  clouds: ['Enable Amazon Web Services', 'Enable Microsoft Azure', 'Enable Google Cloud'],
  notifications: ['Enable Cisco Webex', 'Enable Slack', 'Enable Microsoft Teams', 'Enable Email'],
  ai: ['Enable AI assistant'],
  mcp: ['Enable MCP server'],
}

const open = (section: string) => {
  window.history.replaceState(null, '', `/settings/${section}`)
  return render(<SettingsPage section={section} />)
}

describe('Settings cards', () => {
  it.each(Object.entries(switches))('give every switch in %s a name', async (section, names) => {
    mockFetch(on('GET', '/api/settings', () => json(settings)))
    const { container } = open(section)
    for (const name of names) expect(await screen.findByRole('switch', { name })).toBeTruthy()
    // No hand-made switch is left: a hidden checkbox that is not a named switch.
    const bare = [...container.querySelectorAll('input[type="checkbox"].sr-only')].filter(el => el.getAttribute('role') !== 'switch' || !el.getAttribute('aria-label'))
    expect(bare).toEqual([])
  })

  it('tests and saves VictoriaMetrics with basic auth', async () => {
    const fetch = mockFetch(
      on('GET', '/api/settings', () => json(settings)),
      on('PUT', '/api/settings', () => json(settings)),
      on('POST', '/api/settings/providers/victoriametrics/test', () => json({ connected: true })),
    )
    open('metrics')
    const user = userEvent.setup()
    await user.click(await screen.findByRole('switch', { name: 'Enable VictoriaMetrics' }))
    expect(screen.getByRole('button', { name: 'Test connection' })).toHaveProperty('disabled', true)
    await user.type(screen.getByLabelText('Endpoint URL'), 'http://vmsingle.monitoring.svc:8428')
    await user.click(screen.getByRole('radio', { name: 'Basic auth' }))
    await user.type(screen.getByLabelText('Username'), 'reader')
    await user.type(screen.getByLabelText('Password'), 'pw')
    await user.click(screen.getByRole('button', { name: 'Test connection' }))
    expect((await screen.findByRole('status')).textContent).toBe('Connected: container metrics found')
    const test = JSON.parse(fetch.mock.calls.find(([url]) => url === '/api/settings/providers/victoriametrics/test')![1]!.body as string)
    expect(test).toMatchObject({ endpoint: 'http://vmsingle.monitoring.svc:8428', username: 'reader', password: 'pw' })

    await user.click(screen.getByRole('button', { name: 'Save' }))
    await screen.findByText('Saved')
    const put = JSON.parse(fetch.mock.calls.find(([, init]) => init?.method === 'PUT')![1]!.body as string)
    expect(put.integrations.victoriaMetrics).toMatchObject({ enabled: true, endpoint: 'http://vmsingle.monitoring.svc:8428', username: 'reader', password: 'pw' })
  })

  it('shows a failed connection test next to its button', async () => {
    mockFetch(
      on('GET', '/api/settings', () => json(settings)),
      on('POST', '/api/settings/providers/webex/test', () => json({ connected: false, error: 'the bot token was refused' })),
    )
    open('notifications')
    const user = userEvent.setup()
    await user.click(await screen.findByRole('switch', { name: 'Enable Cisco Webex' }))
    const card = screen.getByRole('switch', { name: 'Enable Cisco Webex' }).closest('.shadow-sm') as HTMLElement
    await user.click(within(card).getByRole('button', { name: 'Test connection' }))
    expect((await within(card).findByRole('status')).textContent).toBe('the bot token was refused')
  })
})
