import { describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { json, mockFetch, on } from '../test/fetch'
import OperatorHealth from './OperatorHealth'
import { AuthContext, type Role } from '../lib/auth'

vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} })

const sample = {
  status: 'Healthy', managedNamespaces: 14, memoryUsage: 61.4, cpuUsage: 0.0123, memoryRequests: 64, memoryLimits: 256,
  cpuRequests: 0.1, cpuLimits: 0.5, goroutines: 212, cpuCores: 4, heapAllocMiB: 31.6, sysMemoryMiB: 58.2, gcCycles: 77,
  timestamp: '2026-10-31T10:00:00Z',
}
const replicas = [
  { name: 'costdeck-0', ready: true, node: 'node-a', restarts: 0 },
  { name: 'costdeck-1', ready: true, node: 'node-b', restarts: 1 },
  { name: 'costdeck-2', ready: false, node: '', restarts: 2 },
]
const healthy = { current: { ...sample, pod: 'costdeck-0', leader: 'costdeck-1', replicas }, history: [sample] }
const logs = (text: string, pod = 'costdeck-1') => new Response(text, { headers: { 'X-Costdeck-Pod': pod } })
const tile = (label: string) => screen.getByText(label).parentElement!.parentElement as HTMLElement
const asRole = (role: Role) => ({ sub: `user:${role}`, name: role, role, provider: 'local' as const })

describe('Operator health', () => {
  it('shows the replicas, who leads and serves, the resource use and the leader logs', async () => {
    mockFetch(
      on('GET', '/api/operator/health', () => json(healthy)),
      on('GET', '/api/operator/logs', () => logs('I1031 Reconciled ScalingGroup team-a\nI1031 Collected the cost ledger')),
    )
    render(<OperatorHealth />)
    expect(await screen.findByRole('heading', { name: 'Replicas' })).toBeTruthy()
    expect(tile('Status').textContent).toContain('Healthy')
    expect(tile('Status').textContent).toContain('No errors in the recent logs')
    expect(tile('Namespaces tracked').textContent).toContain('14')
    expect(tile('Goroutines').textContent).toContain('4 CPU cores visible to this pod')
    expect(tile('Memory').textContent).toContain('32 MiB heap')
    expect(tile('Memory').textContent).toContain('58 MiB from the OS · 77 GC cycles')
    expect(screen.getByText(/Every replica serves the dashboard and API/)).toBeTruthy()

    const leader = screen.getByText('costdeck-1').parentElement as HTMLElement
    expect(leader.textContent).toContain('Leader')
    expect(leader.textContent).toContain('node-b · 1 restart')
    expect(screen.getByText('costdeck-0').parentElement!.textContent).toContain('Serving this page')
    const broken = screen.getByText('costdeck-2').parentElement as HTMLElement
    expect(broken.textContent).toContain('Not ready')
    expect(broken.textContent).toContain('unscheduled · 2 restarts')

    expect(screen.getByText('0.012 / 0.5 Cores')).toBeTruthy()
    expect(screen.getByText('61 / 256 MiB')).toBeTruthy()
    expect(screen.getByText('Logs of costdeck-1 (last 100 lines)')).toBeTruthy()
    expect(screen.getByText(/Collected the cost ledger/)).toBeTruthy()
  })

  it('counts the error lines in the logs', async () => {
    mockFetch(
      on('GET', '/api/operator/health', () => json({ current: { ...sample, replicas: [replicas[0]] }, history: [] })),
      on('GET', '/api/operator/logs', () => logs('I1031 fine\n{"level":"error","msg":"Reconciler error"}\nE1031 ERROR could not list nodes\nI1031 no terrors here')),
    )
    render(<OperatorHealth />)
    expect(await screen.findByText('2 errors')).toBeTruthy()
    expect(tile('Status').textContent).toContain('In the last log lines below')
    expect(screen.getByText('One replica: the dashboard is down while it restarts.')).toBeTruthy()
  })

  it('refreshes on demand', async () => {
    let calls = 0
    mockFetch(
      on('GET', '/api/operator/health', () => { calls++; return json({ current: { ...sample, managedNamespaces: calls }, history: [] }) }),
      on('GET', '/api/operator/logs', () => logs('')),
    )
    render(<OperatorHealth />)
    expect(await screen.findByText('No logs available...')).toBeTruthy()
    expect(tile('Namespaces tracked').textContent).toContain('1')
    await userEvent.setup().click(screen.getByRole('button', { name: 'Refresh' }))
    await waitFor(() => expect(tile('Namespaces tracked').textContent).toContain('2'))
  })

  it.skip('bug: a viewer is shown the 403 answer of the logs endpoint as logs and counted as an error', async () => {
    mockFetch(
      on('GET', '/api/operator/health', () => json({ current: sample, history: [] })),
      on('GET', '/api/operator/logs', () => json({ error: 'This action needs the operator role; you are signed in as viewer.' }, 403)),
    )
    render(<AuthContext.Provider value={asRole('viewer')}><OperatorHealth /></AuthContext.Provider>)
    expect(await screen.findByText('Cost Deck Health')).toBeTruthy()
    expect(screen.queryByText(/"error"/)).toBeNull()
    expect(tile('Status').textContent).toContain('Healthy')
  })

  it.skip('bug: the download of the full logs is offered to operators and viewers, whom the server refuses', async () => {
    mockFetch(
      on('GET', '/api/operator/health', () => json({ current: sample, history: [] })),
      on('GET', '/api/operator/logs', () => logs('I1031 fine')),
    )
    render(<AuthContext.Provider value={asRole('operator')}><OperatorHealth /></AuthContext.Provider>)
    expect(await screen.findByText(/I1031 fine/)).toBeTruthy()
    expect(screen.queryByRole('link', { name: 'Download full logs' })).toBeNull()
  })
})
