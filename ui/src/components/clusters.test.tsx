import { describe, expect, it } from 'vitest'
import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { json, mockFetch, on } from '../test/fetch'
import ClustersPage from '../pages/ClustersPage'
import SettingsPage from '../pages/SettingsPage'
import { AuthContext } from '../lib/auth'
import { clustersProblem, clustersRequest, type ClusterSummary } from '../lib/clusters'

const summary = (over: Partial<ClusterSummary>): ClusterSummary => ({
  name: 'x', generatedAt: '2026-10-23T10:00:00Z', currency: 'EUR', nodes: 3, namespaces: 10, monthlyCost: 1000, monthlyRequested: 600, monthlyUsed: 300,
  monthlyUnallocated: 400, schedulesSaving: 100, savingsAvailable: 50, monthToDate: 700, forecast: 1000, schedules: 4, schedulesDown: 1, ...over,
})
const admin = { sub: 'local:admin', name: 'admin', role: 'admin' as const, provider: 'local' as const }

describe('Clusters', () => {
  it('sends tokens only when typed and says what keeps the list from saving', () => {
    expect(clustersRequest([{ name: ' eu ', url: 'https://costdeck.eu.example.com//', token: '', skipTlsVerify: false, hasToken: true }]))
      .toEqual([{ name: 'eu', url: 'https://costdeck.eu.example.com', skipTlsVerify: false }])
    const c = { name: 'eu', url: 'https://eu', token: 'cdk_x', skipTlsVerify: false, hasToken: false }
    expect(clustersProblem([c])).toBeNull()
    expect(clustersProblem([{ ...c, name: 'EU' }])).toMatch(/lowercase/)
    expect(clustersProblem([c, c])).toBe('Clusters: eu is listed twice')
    expect(clustersProblem([{ ...c, url: 'eu.example.com' }])).toMatch(/starts with https/)
    expect(clustersProblem([{ ...c, token: ' ' }])).toBe('Clusters: eu needs a viewer API token of that installation')
  })

  it('shows every cluster, this one marked, and says why one could not be read', async () => {
    mockFetch(on('GET', '/api/clusters', () => json({
      clusters: [
        { name: 'prod-eu', local: true, summary: summary({ name: 'prod-eu', nodes: 12, monthlyCost: 4000 }) },
        { name: 'prod-us', url: 'https://costdeck.us.example.com', local: false, summary: summary({ name: 'prod-us' }) },
        { name: 'lab', url: 'https://costdeck.lab.example.com', local: false, error: 'costdeck.lab.example.com refused the API token (401 Unauthorized)' },
      ],
      totals: [{ currency: 'EUR', clusters: 2, nodes: 15, monthlyCost: 5000, schedulesSaving: 200, savingsAvailable: 100, monthToDate: 1400, forecast: 2000 }],
    })))
    render(<AuthContext.Provider value={admin}><ClustersPage /></AuthContext.Provider>)
    const local = (await screen.findByText('prod-eu')).closest('tr')!
    expect(within(local).getByText('this one')).toBeTruthy()
    expect(local.textContent).toMatch(/€4,000/)
    expect(screen.getByText(/refused the API token/)).toBeTruthy()
    expect(within(screen.getByText('prod-us').closest('tr')!).getByRole('link', { name: /Open/ }).getAttribute('target')).toBe('_blank')
    expect(screen.getByText('2 clusters, 15 nodes').parentElement!.textContent).toMatch(/€5,000/)
  })

  it('points to the settings while this is the only cluster', async () => {
    mockFetch(on('GET', '/api/clusters', () => json({ clusters: [{ name: 'this cluster', local: true, summary: summary({}) }], totals: [] })))
    render(<AuthContext.Provider value={admin}><ClustersPage /></AuthContext.Provider>)
    expect((await screen.findByRole('link', { name: 'Settings → Clusters' })).getAttribute('href')).toBe('/settings/clusters')
  })

  it('adds a cluster, tests it before saving, and saves the list', async () => {
    window.history.replaceState(null, '', '/settings/clusters')
    const settings = { providers: {}, integrations: {}, auth: { disableLocalLogin: false }, calendars: [], clusters: [{ name: 'old', url: 'https://old', skipTlsVerify: false, hasToken: true }] }
    const fetch = mockFetch(
      on('GET', '/api/settings', () => json(settings)),
      on('PUT', '/api/settings', () => json(settings)),
      on('POST', '/api/settings/providers/cluster/test', () => json({ connected: true, message: 'Reached prod-us: 8 nodes, 3000 EUR a month' })),
    )
    render(<SettingsPage section="clusters" />)
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: 'Add a cluster' }))
    await user.type(screen.getByLabelText('Name', { selector: '#cluster-name-1' }), 'prod-us')
    await user.type(screen.getByLabelText('Dashboard URL', { selector: '#cluster-url-1' }), 'https://costdeck.us.example.com')
    await user.type(screen.getByLabelText('API token of prod-us'), 'cdk_us')
    await user.click(screen.getAllByRole('button', { name: 'Test connection' })[1])
    expect(await screen.findByText('Reached prod-us: 8 nodes, 3000 EUR a month')).toBeTruthy()
    expect(JSON.parse(fetch.mock.calls.find(([url]) => url === '/api/settings/providers/cluster/test')![1]!.body as string))
      .toEqual({ name: 'prod-us', url: 'https://costdeck.us.example.com', skipTlsVerify: false, token: 'cdk_us' })

    await user.click(screen.getAllByRole('button', { name: 'Remove' })[0])
    await user.click(screen.getByRole('button', { name: 'Save' }))
    await screen.findByText('Saved')
    const put = JSON.parse(fetch.mock.calls.find(([, init]) => init?.method === 'PUT')![1]!.body as string)
    expect(put).toEqual({ clusters: [{ name: 'prod-us', url: 'https://costdeck.us.example.com', skipTlsVerify: false, token: 'cdk_us' }] })
  })
})
