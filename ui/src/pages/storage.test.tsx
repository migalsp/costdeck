import { describe, expect, it } from 'vitest'
import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { json, mockFetch, on } from '../test/fetch'
import StoragePage from './StoragePage'
import type { InfraResponse } from '../lib/finops'

const infra = (over: Partial<InfraResponse> = {}, lbs?: InfraResponse['infra']['loadBalancers']): InfraResponse => ({
  currency: 'USD',
  cloud: 'aws',
  infra: {
    volumes: [
      { namespace: 'shop', claim: 'data-db-0', volume: 'pvc-1', storageClass: 'gp3', diskType: 'gp3', phase: 'Bound', sizeGiB: 100, monthlyCost: 8, mountedBy: ['db-0'], state: 'in-use', basis: 'gp3 list price' },
      { namespace: 'shop', claim: 'old-cache', volume: 'pvc-2', storageClass: 'gp3', diskType: 'gp3', phase: 'Bound', sizeGiB: 20, monthlyCost: 1.6, mountedBy: [], state: 'unused', basis: 'gp3 list price' },
      { volume: 'pvc-3', storageClass: 'gp3', diskType: 'gp3', phase: 'Released', sizeGiB: 1000, monthlyCost: 80, mountedBy: [], state: 'orphaned', basis: 'gp3 list price' },
      { namespace: 'dev', claim: 'scratch', volume: 'pvc-4', storageClass: 'local-path', phase: 'Bound', sizeGiB: 5, monthlyCost: 0, local: true, mountedBy: ['a', 'b', 'c', 'd', 'e'], state: 'scaled-down', basis: 'part of the node' },
    ],
    loadBalancers: lbs ?? [
      { namespace: 'shop', name: 'ingress', address: '203.0.113.7', ports: ['80/TCP', '443/TCP'], readyEndpoints: 2, monthlyCost: 18, state: 'serving', basis: 'AWS NLB' },
      { namespace: 'dev', name: 'api-lb', ports: ['8080/TCP'], readyEndpoints: 0, monthlyCost: 18, state: 'scaled-down', basis: 'AWS NLB' },
    ],
    storageMonthly: 89.6,
    networkMonthly: 36,
    unusedStorageMonthly: 81.6,
    idleNetworkMonthly: 18,
    networkBasis: 'AWS Network Load Balancer list price',
  },
  ...over,
})

const tile = (label: string) => screen.getByText(label, { selector: 'span.font-semibold' }).parentElement!.parentElement as HTMLElement
const rows = () => screen.getAllByRole('row').slice(1)
const names = () => rows().map(r => r.querySelector('td div')?.textContent)

describe('Storage and network', () => {
  it('sums up volumes and load balancers and flags what to review', async () => {
    mockFetch(on('GET', '/api/finops/infra', () => json(infra())))
    render(<StoragePage />)
    expect(await screen.findByText('data-db-0')).toBeTruthy()
    expect(tile('Storage').textContent).toBe('Storage$90/mo4 volumes, 1.1 TiB')
    expect(within(tile('Unused volumes')).getByText('2 to review')).toBeTruthy()
    expect(tile('Load balancers').textContent).toContain('2 LoadBalancer Services')
    expect(within(tile('Idle load balancers')).getByText('1 to review')).toBeTruthy()
    expect(names()).toEqual(['data-db-0', 'old-cache', 'pvc-3', 'scratch'])
    expect(screen.getByText(/Load balancers: AWS Network Load Balancer list price\./)).toBeTruthy()
  })

  it('filters volumes by state', async () => {
    mockFetch(on('GET', '/api/finops/infra', () => json(infra())))
    render(<StoragePage />)
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: 'Unused 1' }))
    expect(names()).toEqual(['old-cache'])
    await user.click(screen.getByRole('button', { name: 'Orphaned 1' }))
    expect(names()).toEqual(['pvc-3'])
    expect(within(rows()[0]).getByText('no claim')).toBeTruthy()
    expect(within(rows()[0]).getByText('1000 GiB')).toBeTruthy()
    await user.click(screen.getByRole('button', { name: 'Kept while down 1' }))
    expect(within(rows()[0]).getByText('a, b, c +2')).toBeTruthy()
    expect(within(rows()[0]).getByText('local, part of the node')).toBeTruthy()
    await user.click(screen.getByRole('button', { name: 'All 4' }))
    expect(rows()).toHaveLength(4)
  })

  it('says when no volume is in the chosen state', async () => {
    const data = infra()
    data.infra.volumes = data.infra.volumes.filter(v => v.state !== 'orphaned')
    mockFetch(on('GET', '/api/finops/infra', () => json(data)))
    render(<StoragePage />)
    await userEvent.setup().click(await screen.findByRole('button', { name: 'Orphaned 0' }))
    expect(screen.getByText('No volume in this state.')).toBeTruthy()
  })

  it('lists load balancers on their own tab', async () => {
    mockFetch(on('GET', '/api/finops/infra', () => json(infra())))
    render(<StoragePage />)
    await userEvent.setup().click(await screen.findByRole('button', { name: /Load balancers/ }))
    expect(screen.queryByRole('button', { name: /^All 4/ })).toBeNull()
    expect(names()).toEqual(['ingress', 'api-lb'])
    expect(within(rows()[0]).getByText('80/TCP, 443/TCP')).toBeTruthy()
    expect(within(rows()[0]).getByText('Serving')).toBeTruthy()
    expect(within(rows()[1]).getByText('pending')).toBeTruthy()
    expect(within(rows()[1]).getByText('Billed while down')).toBeTruthy()
  })

  it('says when there is no load balancer', async () => {
    mockFetch(on('GET', '/api/finops/infra', () => json(infra({}, []))))
    render(<StoragePage />)
    await userEvent.setup().click(await screen.findByRole('button', { name: /Load balancers/ }))
    expect(screen.getByText('No Service of type LoadBalancer.')).toBeTruthy()
    expect(within(tile('Idle load balancers')).queryByText(/to review/)).toBeNull()
  })

  it('explains a load error and what the chart may not read', async () => {
    mockFetch(on('GET', '/api/finops/infra', () => json({ error: 'boom' }, 500)))
    const { unmount } = render(<StoragePage />)
    expect(await screen.findByText('Could not load storage and network: boom')).toBeTruthy()
    unmount()
    mockFetch(on('GET', '/api/finops/infra', () => json(infra({ error: 'persistentvolumes is forbidden' }))))
    render(<StoragePage />)
    expect(await screen.findByText('Volumes and load balancers could not be read: persistentvolumes is forbidden. Upgrade the chart so the operator may list them.')).toBeTruthy()
  })
})
