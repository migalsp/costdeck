import { describe, expect, it } from 'vitest'
import { render, screen } from '@testing-library/react'
import { json, mockFetch, on } from '../test/fetch'
import RealizedSavings from './RealizedSavings'
import ScheduleDetails from './ScheduleDetails'
import type { RealizedSavings as Check, ScalingGroup } from '../lib/types'

const check = (c: Partial<Check>): Check => ({
  state: 'checked', settleMinutes: 15, estimatedHourly: 1.2, realizedHourly: 0.3, currency: 'USD', gone: 1, blocked: 1, nodes: [], ...c,
})

describe('Realized savings', () => {
  it('says which nodes went and what keeps the others', async () => {
    mockFetch(on('GET', '/api/scaling/groups/shop/realized', () => json(check({
      nodes: [
        { name: 'node-a', hourlyCost: 0.3, gone: true, blockers: [], tenants: [], requestedShare: 0 },
        { name: 'node-b', hourlyCost: 0.2, gone: false, requestedShare: 0.1, tenants: [],
          blockers: [{ reason: 'pdb', pod: 'ops/db-0', detail: 'The PodDisruptionBudget ops/db allows no disruption now.' }] },
      ],
    }))))
    render(<RealizedSavings kind="group" name="shop" />)
    const section = await screen.findByTestId('realized-savings')
    expect(section.textContent).toContain('1 of 2 nodes the workloads ran on was removed, saving $0.30/h of the estimated $1.20/h.')
    expect(screen.getByText('kept by one blocker on ops/db-0')).toBeTruthy()
    expect(screen.getByText('The PodDisruptionBudget ops/db allows no disruption now.')).toBeTruthy()
  })

  it('says the bill only drops once nodes go, and that autoscalers get time first', async () => {
    mockFetch(on('GET', '/api/scaling/configs/billing/realized', () => json(check({
      gone: 0, realizedHourly: 0, nodes: [{ name: 'node-c', hourlyCost: 0.2, gone: false, blockers: [], tenants: [], requestedShare: 0.05 }],
    }))))
    render(<RealizedSavings kind="config" name="billing" />)
    expect((await screen.findByTestId('realized-savings')).textContent).toContain('The freed requests do not lower the bill until nodes go.')
    expect(screen.getByText(/does Cluster Autoscaler or Karpenter manage its pool/)).toBeTruthy()

    mockFetch(on('GET', '/api/scaling/configs/fresh/realized', () => json(check({ state: 'settling', gone: 0, realizedHourly: 0,
      nodes: [{ name: 'node-c', hourlyCost: 0.2, gone: false, blockers: [], tenants: [], requestedShare: 0.05 }] }))))
    render(<RealizedSavings kind="config" name="fresh" />)
    expect(await screen.findByText(/Autoscalers get 15 minutes to act/)).toBeTruthy()
    expect(screen.getByText('waiting for the autoscaler')).toBeTruthy()
  })

  it('shows nothing when there is nothing to check, and the error when the check fails', async () => {
    mockFetch(on('GET', '/api/scaling/groups/up/realized', () => json(check({ state: 'none' }))))
    const { container } = render(<RealizedSavings kind="group" name="up" />)
    await new Promise(r => setTimeout(r, 0))
    expect(container.textContent).toBe('')

    mockFetch(on('GET', '/api/scaling/groups/broken/realized', () => json({ error: 'forbidden' }, 403)))
    render(<RealizedSavings kind="group" name="broken" />)
    expect(await screen.findByText(/Could not check the nodes: forbidden/)).toBeTruthy()
  })

  it('appears on the card of a schedule that is down, not one that is up', async () => {
    const fetch = mockFetch(on('GET', '/api/scaling/groups/*', () => json(check({ nodes: [{ name: 'node-a', hourlyCost: 0.3, gone: true, blockers: [], tenants: [], requestedShare: 0 }] }))))
    const group = (name: string, desiredState: 'Up' | 'Down') => ({
      metadata: { name }, spec: { category: 'x', namespaces: ['shop-dev'] }, status: { phase: desiredState === 'Up' ? 'ScaledUp' : 'ScaledDown', desiredState },
    }) as unknown as ScalingGroup
    const props = { groups: [], tab: 'overview' as const, onTab: () => {}, onClose: () => {}, onSelectNamespace: () => {}, canOperate: false, canAdmin: false,
      onStart: () => {}, onStop: () => {}, onResume: () => {}, onEdit: () => {}, onDelete: () => {} }
    const { unmount } = render(<ScheduleDetails target={{ kind: 'group', group: group('up', 'Up') }} {...props} />)
    expect(fetch.mock.calls.some(([url]) => String(url).includes('/realized'))).toBe(false)
    unmount()
    render(<ScheduleDetails target={{ kind: 'group', group: group('down', 'Down') }} {...props} />)
    expect(await screen.findByTestId('realized-savings')).toBeTruthy()
    expect(fetch.mock.calls.some(([url]) => url === '/api/scaling/groups/down/realized')).toBe(true)
  })
})
