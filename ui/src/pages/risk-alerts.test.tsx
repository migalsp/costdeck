import { describe, expect, it } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { json, mockFetch, on } from '../test/fetch'
import BudgetsPage from './BudgetsPage'
import { AuthContext, type User } from '../lib/auth'

const admin: User = { sub: 'local:admin', name: 'admin', role: 'admin', provider: 'local' }
const budgets = (enabled: boolean) => ({
  currency: 'USD', budgets: [], anomalies: { enabled: false }, performanceRisks: { enabled }, events: [], webexReady: false, channels: ['Slack'],
  scopes: { namespaces: [], teams: [] },
})

describe('Performance risk alerts', () => {
  it('turns the alerts on without touching the anomaly alerts', async () => {
    let enabled = false
    const fetch = mockFetch(
      on('GET', '/api/budgets', () => json(budgets(enabled))),
      on('PUT', '/api/settings', (_, init) => {
        enabled = JSON.parse(init!.body as string).alerts.performanceRisks.enabled
        return json({})
      }),
    )
    render(<AuthContext.Provider value={admin}><BudgetsPage /></AuthContext.Provider>)
    const heading = await screen.findByRole('heading', { name: 'Performance risk alerts' })
    const card = heading.closest('div')!.parentElement!
    expect(within(card).getByText('Off')).toBeTruthy()
    await userEvent.setup().click(within(card).getByRole('checkbox', { name: 'Send performance risk alerts' }))
    const put = fetch.mock.calls.find(([u, init]) => u === '/api/settings' && init?.method === 'PUT')!
    expect(JSON.parse(put[1]!.body as string)).toEqual({ alerts: { performanceRisks: { enabled: true } } })
    await waitFor(() => expect(within(card).getByText('On')).toBeTruthy())
  })
})
