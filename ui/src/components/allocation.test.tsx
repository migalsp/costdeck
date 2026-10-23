import { afterEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { json, mockFetch, on } from '../test/fetch'
import AllocationCard from './finops/AllocationCard'
import SettingsPage from '../pages/SettingsPage'
import NamespaceInsights from '../pages/NamespaceInsights'
import { allocationForm, allocationProblem, allocationQuery, allocationRequest, policySummary, type AllocationReport } from '../lib/allocation'
import type { NamespaceRow, Overview } from '../lib/finops'

const report = (by = 'team'): AllocationReport => ({
  currency: 'EUR', from: '2026-10-01', to: '2026-10-23', by, days: 23, total: 1150, unallocated: 300,
  policy: { idle: 'separate', sharedSplit: 'proportional', sharedNamespaces: ['kube-*'] }, sharedNamespaces: ['kube-system', 'monitoring'],
  rows: [
    { key: 'payments', namespaces: ['shop-dev', 'shop-prod'], compute: 500, storage: 100, network: 50, shared: 150, idle: 0, total: 800 },
    { key: '(no team)', namespaces: ['pr-41'], compute: 40, storage: 0, network: 0, shared: 10, idle: 0, total: 50 },
  ],
})

afterEach(() => { vi.restoreAllMocks() })

describe('Cost allocation', () => {
  it('builds the query, says how costs were charged and reads the form', () => {
    expect(allocationQuery('last-month', 'label', ' cost-center ')).toBe('by=label%3Acost-center&period=last-month')
    expect(allocationQuery('this-month', 'label', ' ')).toBeNull()
    expect(policySummary(report())).toBe('kube-system, monitoring shared out by what each namespace requests; unrequested capacity on a line of its own')
    expect(policySummary({ policy: { idle: 'proportional', sharedSplit: 'even' }, sharedNamespaces: [] })).toBe('no shared namespaces; unrequested capacity charged by requests')
    const f = { ...allocationForm(), shared: 'kube-*, monitoring  monitoring' }
    expect(allocationRequest(f)).toEqual({ idle: 'separate', sharedSplit: 'proportional', sharedNamespaces: ['kube-*', 'monitoring'] })
    expect(allocationProblem({ ...f, shared: 'kube-*, Monitoring' })).toMatch(/"Monitoring" is not a namespace name/)
    expect(allocationProblem({ ...f, shared: '[kube' })).toMatch(/is not a namespace name/)
  })

  it('shows what each group cost, switches period and group, and downloads the CSV', async () => {
    const fetch = mockFetch(
      on('GET', '/api/finops/allocation.csv', () => new Response('team,namespaces\npayments,shop-dev\n', { headers: { 'Content-Type': 'text/csv' } })),
      on('GET', '/api/finops/allocation', () => json(report())),
    )
    const created = vi.fn<(blob: Blob) => string>(() => 'blob:csv')
    Object.assign(URL, { createObjectURL: created, revokeObjectURL: vi.fn() })
    render(<AllocationCard canAdmin />)
    const user = userEvent.setup()
    expect(await screen.findByText('payments')).toBeTruthy()
    expect(screen.getByText('shop-dev, shop-prod')).toBeTruthy()
    // The Reports page titles the section; the card does not repeat it.
    expect(screen.queryByRole('heading', { name: 'Cost allocation' })).toBeNull()
    expect(screen.getByText(/Unallocated capacity/).parentElement!.textContent).toMatch(/€300/)
    expect(screen.getByText(/Total, 23 days from 2026-10-01 to 2026-10-23/).parentElement!.textContent).toMatch(/€1,150/)
    expect(screen.getByRole('link', { name: 'Change the policy' }).getAttribute('href')).toBe('/settings/allocation')

    await user.click(screen.getByRole('button', { name: 'Last month' }))
    await user.click(screen.getByRole('button', { name: 'Label' }))
    await waitFor(() => expect(fetch.mock.calls.some(([url]) => String(url) === '/api/finops/allocation?by=label%3Acost-center&period=last-month')).toBe(true))

    await user.click(screen.getByRole('button', { name: 'Download CSV' }))
    await waitFor(() => expect(created).toHaveBeenCalled())
    expect(await created.mock.calls[0][0].text()).toBe('team,namespaces\npayments,shop-dev\n')
  })

  it('saves the policy from Settings', async () => {
    window.history.replaceState(null, '', '/settings/allocation')
    const settings = { providers: {}, integrations: {}, auth: { disableLocalLogin: false }, calendars: [], allocation: { idle: 'separate', sharedNamespaces: [], sharedSplit: 'proportional' } }
    const fetch = mockFetch(on('GET', '/api/settings', () => json(settings)), on('PUT', '/api/settings', () => json(settings)))
    render(<SettingsPage section="allocation" />)
    const user = userEvent.setup()
    await user.type(await screen.findByLabelText('Shared namespaces'), 'kube-*, monitoring')
    await user.click(screen.getByRole('radio', { name: /Evenly/ }))
    await user.click(screen.getByRole('radio', { name: /Charged to the namespaces/ }))
    await user.click(screen.getByRole('button', { name: 'Save' }))
    await screen.findByText('Saved')
    const put = JSON.parse(fetch.mock.calls.find(([, init]) => init?.method === 'PUT')![1]!.body as string)
    expect(put).toEqual({ allocation: { idle: 'proportional', sharedSplit: 'even', sharedNamespaces: ['kube-*', 'monitoring'] } })
  })

  it('shares out idle capacity on Namespace Insights when the policy does', async () => {
    const res = { requested: 1, used: 0.5, limit: 2 }
    const row: NamespaceRow = { name: 'web', environment: 'production', pods: 2, collected: true, cpu: res, memoryGiB: res, monthlyCost: 500, monthlyUsedCost: 200,
      monthlyIdle: 300, monthlyStorage: 0, monthlyNetwork: 0, monthlyTotal: 500, insights: [] }
    const overview = {
      generatedAt: '2026-10-22T12:00:00Z', currency: 'USD', rates: { cpuCoreHour: 0.033, memoryGiBHour: 0.00375, currency: 'USD', basis: 'x' },
      cluster: { nodes: 1, spotNodes: 0, cpuCores: 4, memoryGiB: 16, cpuRequested: 1, memoryRequestedGiB: 1, cpuUsed: 1, memoryUsedGiB: 1, provisionedHourly: 1, requestedHourly: 1, usedHourly: 1 },
      monthly: { provisioned: 730, requested: 500, used: 200, unallocated: 230, overprovisioned: 300, storage: 0, network: 0, total: 730, unusedStorage: 0, idleNetwork: 0, gpu: 0, idleGpu: 0 },
      savings: { schedulesMonthly: 0, rightsizingMonthly: 0, rightsizingPending: 0, scheduleCandidatesMonthly: 0 },
      monthToDate: { month: '2026-10', cost: 0, saved: 0, hours: 0, forecast: 0, partial: true }, history: { top: [], days: [] }, namespaces: [row], opportunities: [],
    } as unknown as Overview
    mockFetch(
      on('GET', '/api/finops/overview', () => json(overview)),
      on('GET', '/api/settings', () => json({ integrations: {}, allocation: { idle: 'proportional', sharedNamespaces: [], sharedSplit: 'proportional' } })),
      on('GET', '/api/namespaces/*', () => json({})),
    )
    render(<NamespaceInsights onSelectNamespace={() => {}} />)
    expect(await screen.findByText('Monthly cost, fully allocated')).toBeTruthy()
  })
})
