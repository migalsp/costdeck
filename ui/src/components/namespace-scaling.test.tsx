import { describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { json, mockFetch, on } from '../test/fetch'
import NamespaceScalingPanel from './NamespaceScalingPanel'
import NamespaceCard from './NamespaceCard'
import Toaster from './Toaster'
import { AuthContext, type Role, type User } from '../lib/auth'
import { toast } from '../lib/toast'
import type { ScalingConfig, ScalingGroup } from '../lib/types'

vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} })

const as = (role: Role): User => ({ sub: `user:${role}`, name: role, role, provider: 'local' })
const weekdays = [{ days: [1, 2, 3, 4, 5], startTime: '08:00', endTime: '20:00', timezone: 'UTC' }]

const teamA = (status: Partial<NonNullable<ScalingGroup['status']>> = {}, spec: Partial<ScalingGroup['spec']> = {}): ScalingGroup => ({
  metadata: { name: 'team-a' },
  spec: { category: 'dev', namespaces: ['shop', 'cart'], schedules: weekdays, ...spec },
  status: { phase: 'ScaledUp', lastAction: '', managedCount: 2, desiredState: 'Up', mode: 'Schedule', ...status },
})
const shopConfig = (spec: Partial<ScalingConfig['spec']> = {}, status: Partial<NonNullable<ScalingConfig['status']>> = {}): ScalingConfig => ({
  metadata: { name: 'config-shop' },
  spec: { targetNamespace: 'shop', ...spec },
  status: { phase: 'ScaledDown', lastAction: '', desiredState: 'Down', mode: 'Schedule', ...status },
})
const namespaces = [{ metadata: { name: 'shop', creationTimestamp: '' }, spec: { targetNamespace: 'shop' } }]

const schedules = (groups: ScalingGroup[], configs: ScalingConfig[] = []) => [
  on('GET', '/api/scaling/groups', () => json(groups)),
  on('GET', '/api/scaling/configs', () => json(configs)),
  on('GET', '/api/namespaces', () => json(namespaces)),
]

const panel = (role: Role) => render(<AuthContext.Provider value={as(role)}><NamespaceScalingPanel namespace="shop" /><Toaster /></AuthContext.Provider>)
const dialog = (title: string) => screen.getByRole('heading', { name: title }).closest('.rounded-2xl') as HTMLElement
const posts = (fetch: ReturnType<typeof mockFetch>, url: string) =>
  fetch.mock.calls.filter(([u, init]) => u === url && init?.method === 'POST').map(([, init]) => JSON.parse(init!.body as string))
const gets = (fetch: ReturnType<typeof mockFetch>, url: string) => fetch.mock.calls.filter(([u, init]) => u === url && !init?.method).length

describe('Namespace scaling panel', () => {
  it('scales the whole group down for a chosen time and says so', async () => {
    const fetch = mockFetch(...schedules([teamA()]), on('POST', '/api/scaling/groups/team-a/manual', () => json(teamA())))
    const success = vi.spyOn(toast, 'success')
    panel('operator')
    expect(await screen.findByText('team-a')).toBeTruthy()
    expect(screen.getByText(/with 1 other namespace$/)).toBeTruthy()
    expect(screen.getByText('applies to all of team-a')).toBeTruthy()
    expect(screen.getByText('Weekdays 08:00–20:00 · UTC')).toBeTruthy()
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'Scale down now' }))
    const ask = dialog('Scale team-a down now')
    await user.click(within(ask).getByRole('radio', { name: /For 4 hours/ }))
    await user.click(within(ask).getByRole('button', { name: 'Scale down now' }))
    await waitFor(() => expect(success).toHaveBeenCalledWith('Stopping group team-a for 4 hours'))
    expect(posts(fetch, '/api/scaling/groups/team-a/manual')).toEqual([{ active: false, until: '4h' }])
    expect(screen.queryByRole('heading', { name: 'Scale team-a down now' })).toBeNull()
    await waitFor(() => expect(gets(fetch, '/api/scaling/groups')).toBe(2))
  })

  it("starts a namespace's own schedule until it is handed back, sending no end", async () => {
    const fetch = mockFetch(...schedules([], [shopConfig({ schedules: weekdays })]), on('POST', '/api/scaling/configs/config-shop/manual', () => json(shopConfig())))
    const success = vi.spyOn(toast, 'success')
    panel('operator')
    expect(await screen.findByText('Own schedule')).toBeTruthy()
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'Start now' }))
    const ask = dialog('Start config-shop now')
    await user.click(within(ask).getByRole('radio', { name: /Until I hand it back/ }))
    await user.click(within(ask).getByRole('button', { name: 'Start now' }))
    await waitFor(() => expect(success).toHaveBeenCalledWith('Starting shop until you hand it back to the schedule'))
    expect(posts(fetch, '/api/scaling/configs/config-shop/manual')).toEqual([{ active: true }])
  })

  it('hands a manually held group back to its schedule', async () => {
    const fetch = mockFetch(
      ...schedules([teamA({ mode: 'ManualDown', desiredState: 'Down', phase: 'ScaledDown' }, { active: false })]),
      on('POST', '/api/scaling/groups/team-a/manual', () => json(teamA())),
    )
    const success = vi.spyOn(toast, 'success')
    panel('operator')
    expect(await screen.findByText('Kept down manually')).toBeTruthy()
    await userEvent.setup().click(screen.getByRole('button', { name: 'Follow schedule' }))
    await waitFor(() => expect(success).toHaveBeenCalledWith('group team-a follows its schedule again'))
    expect(posts(fetch, '/api/scaling/groups/team-a/manual')).toEqual([{ active: null }])
  })

  it('cancelling the question sends nothing', async () => {
    const fetch = mockFetch(...schedules([teamA()]))
    panel('operator')
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: 'Scale down now' }))
    await user.click(within(dialog('Scale team-a down now')).getByRole('button', { name: 'Cancel' }))
    expect(screen.queryByRole('heading', { name: 'Scale team-a down now' })).toBeNull()
    expect(fetch.mock.calls.some(([, init]) => init?.method === 'POST')).toBe(false)
  })

  it.each([
    ['viewer', [], ['Scale down now', 'Edit schedule', 'Workload rules']],
    ['operator', ['Scale down now'], ['Edit schedule', 'Workload rules']],
    ['admin', ['Scale down now', 'Edit schedule', 'Workload rules'], []],
  ] as const)('offers a %s only what the role allows', async (role, shown, hidden) => {
    mockFetch(...schedules([teamA()]))
    panel(role)
    expect(await screen.findByText('team-a')).toBeTruthy()
    for (const name of shown) expect(screen.getByRole('button', { name: new RegExp(`^${name}`) })).toBeTruthy()
    for (const name of hidden) expect(screen.queryByRole('button', { name: new RegExp(`^${name}`) })).toBeNull()
    expect(screen.queryByText('applies to all of team-a') !== null).toBe(role !== 'viewer')
  })

  it('calls a namespace without a schedule always on and lets only an admin schedule it', async () => {
    mockFetch(...schedules([], [shopConfig({ exclusions: ['redis'] })]))
    const { unmount } = panel('operator')
    expect(await screen.findByText('Always on')).toBeTruthy()
    expect(screen.getByText('Not scheduled')).toBeTruthy()
    expect(screen.queryByRole('button', { name: /Start now|Scale down now|Schedule this namespace|Workload rules/ })).toBeNull()
    unmount()
    panel('admin')
    expect(await screen.findByRole('button', { name: 'Schedule this namespace' })).toBeTruthy()
    expect(screen.queryByRole('button', { name: /Workload rules/ })).toBeNull()
  })

  it('creates the namespace config the first time workload rules are opened', async () => {
    const fetch = mockFetch(
      ...schedules([teamA()]),
      on('POST', '/api/scaling/configs', () => json(shopConfig())),
    )
    panel('admin')
    await userEvent.setup().click(await screen.findByRole('button', { name: /Workload rules/ }))
    expect(await screen.findByRole('heading', { name: 'Workload rules' })).toBeTruthy()
    expect(posts(fetch, '/api/scaling/configs')).toEqual([{ metadata: { name: 'config-shop' }, spec: { targetNamespace: 'shop' } }])
  })

  it('shows why a manual change was refused', async () => {
    mockFetch(...schedules([teamA({ mode: 'ManualUp' }, { active: true })]), on('POST', '/api/scaling/groups/team-a/manual', () => json({ error: 'scalinggroups.finops.costdeck.io "team-a" is forbidden' }, 403)))
    const success = vi.spyOn(toast, 'success')
    panel('operator')
    await userEvent.setup().click(await screen.findByRole('button', { name: 'Follow schedule' }))
    expect(await screen.findByText('scalinggroups.finops.costdeck.io "team-a" is forbidden')).toBeTruthy()
    expect(success).not.toHaveBeenCalled()
  })

  it.skip('bug: a refused manual change is wiped by the next poll instead of staying until closed', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    try {
      let held = true
      mockFetch(
        on('GET', '/api/scaling/groups', () => json([held ? teamA({ mode: 'ManualUp' }, { active: true }) : teamA()])),
        on('GET', '/api/scaling/configs', () => json([])),
        on('GET', '/api/namespaces', () => json(namespaces)),
        on('POST', '/api/scaling/groups/team-a/manual', () => json({ error: 'quota exceeded' }, 403)),
      )
      panel('operator')
      await userEvent.setup({ advanceTimers: vi.advanceTimersByTime }).click(await screen.findByRole('button', { name: 'Follow schedule' }))
      expect(await screen.findByText(/quota exceeded/)).toBeTruthy()
      held = false
      await vi.advanceTimersByTimeAsync(10_000)
      expect(await screen.findByRole('button', { name: 'Scale down now' })).toBeTruthy()
      expect(screen.queryByText(/quota exceeded/)).not.toBeNull()
    } finally {
      vi.useRealTimers()
    }
  })
})

describe('Namespace card', () => {
  const point = (ts: string, cpu: string, mem: string) => ({
    timestamp: ts, cpu: { usage: cpu, requests: '500m', limits: '1' }, memory: { usage: mem, requests: '512Mi', limits: '1Gi' },
  })
  const card = (fetchRevert: () => Response) => mockFetch(
    on('GET', '/api/namespaces/shop/history', () => new Response(JSON.stringify([point('2026-10-31T10:00:00Z', '100m', '128Mi'), point('2026-10-31T10:01:00Z', '250m', '256Mi')]),
      { headers: { 'Content-Type': 'application/json', 'X-History-Range': '1h', 'X-History-Source': 'costdeck' } })),
    on('GET', '/api/namespaces/shop/optimization', () => json({ active: true })),
    on('POST', '/api/costing', () => json({ hourlyCost: 0.0123, monthlyCost: 8.98, currency: 'USD', determinedBy: 'AWS list price' })),
    on('GET', '/api/namespaces/shop/recommendations', () => json({ namespace: 'shop', source: 'metrics-server', historical: false, basis: 'A single reading.', currency: 'USD', monthlySavings: 40, workloads: [] })),
    on('POST', '/api/namespaces/shop/revert', fetchRevert),
  )
  const show = (role: Role, onClick = vi.fn(), range = '1h') => {
    render(<AuthContext.Provider value={as(role)}><NamespaceCard namespace="shop" insights={['Optimized', 'Uncapped']} range={range} onClick={onClick} /></AuthContext.Provider>)
    return onClick
  }

  it('shows the latest usage against requests, the price and what could be saved', async () => {
    card(() => json({}))
    const onClick = show('viewer', vi.fn(), '7d')
    expect(await screen.findByText('0.250 Cores (50.0% of Requests)')).toBeTruthy()
    expect(screen.getByText('256.0 MiB (50.0% of Requests)')).toBeTruthy()
    expect(await screen.findByText(/\$0\.0123\/h/)).toBeTruthy()
    expect(await screen.findByText('Could save ~$40/mo')).toBeTruthy()
    expect(screen.getByText('Healthy')).toBeTruthy()
    expect(screen.getByText('No limits')).toBeTruthy()
    expect(screen.getAllByText('Last 60 minutes · no longer history available')).toHaveLength(2)
    await userEvent.setup().click(screen.getByText('shop'))
    expect(onClick).toHaveBeenCalledOnce()
  })

  it('reverts an earlier optimization after an inline confirmation, without opening the namespace', async () => {
    const fetch = card(() => json({ status: 'ok' }))
    const success = vi.spyOn(toast, 'success')
    const onClick = show('operator')
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: /Revert optimization/ }))
    expect(screen.getByText('Restore the original requests and limits?')).toBeTruthy()
    await user.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(screen.queryByText('Restore the original requests and limits?')).toBeNull()
    expect(fetch.mock.calls.some(([url]) => url === '/api/namespaces/shop/revert')).toBe(false)

    await user.click(screen.getByRole('button', { name: /Revert optimization/ }))
    await user.click(screen.getByRole('button', { name: 'Revert' }))
    await waitFor(() => expect(success).toHaveBeenCalledWith('Restored the original requests and limits in shop'))
    expect(fetch.mock.calls.filter(([url, init]) => url === '/api/namespaces/shop/revert' && init?.method === 'POST')).toHaveLength(1)
    expect(onClick).not.toHaveBeenCalled()
  })

  it('reports a failed revert as an error', async () => {
    card(() => json({ error: 'boom' }, 500))
    const failure = vi.spyOn(toast, 'error')
    show('operator')
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: /Revert optimization/ }))
    await user.click(screen.getByRole('button', { name: 'Revert' }))
    await waitFor(() => expect(failure).toHaveBeenCalledWith('Could not revert shop: boom'))
  })

  it('offers a viewer no revert', async () => {
    card(() => json({}))
    show('viewer')
    expect(await screen.findByText('Could save ~$40/mo')).toBeTruthy()
    expect(screen.queryByRole('button', { name: /Revert/ })).toBeNull()
  })
})
