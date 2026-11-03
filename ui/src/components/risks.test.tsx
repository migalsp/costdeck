import { describe, expect, it } from 'vitest'
import { render, screen, within } from '@testing-library/react'
import { json, mockFetch, on } from '../test/fetch'
import RisksPanel from './RisksPanel'
import RecommendationsPanel from './RecommendationsPanel'
import { AuthContext, type User } from '../lib/auth'

const viewer: User = { sub: 'user:v', name: 'v', role: 'viewer', provider: 'local' }

describe('Performance risks', () => {
  it('lists risks most severe first, with the fix and what it costs', async () => {
    mockFetch(on('GET', '/api/namespaces/shop/risks', () => json({
      namespace: 'shop', historical: true, window: '14d', basis: 'CPU throttling and OOM kills over the last 14d.', currency: 'USD',
      risks: [
        { kind: 'Deployment', workload: 'api', container: 'main', severity: 'critical', code: 'oom-killed', message: 'OOM-killed 2 times at its 512Mi limit.', fix: 'Raise the memory limit to 768Mi and the request with it.', fixMonthlyCost: 12.5 },
        { kind: 'Deployment', workload: 'web', container: 'nginx', severity: 'warning', code: 'cpu-throttled', message: 'Throttled in 12% of CPU periods.', fix: 'Raise the CPU limit to 2.', fixMonthlyCost: 0 },
        { kind: 'Deployment', workload: 'worker', container: 'main', severity: 'warning', code: 'restarts', message: 'Restarted 5 times.', fix: 'Read the log of the previous run.' },
      ],
    })))
    render(<RisksPanel namespace="shop" />)
    const panel = await screen.findByRole('region', { name: 'Performance risks' })
    expect(within(panel).getByText('1 critical')).toBeTruthy()
    expect(within(panel).getByText('2 warnings')).toBeTruthy()
    const items = within(panel).getAllByRole('listitem')
    expect(items[0].textContent).toMatch(/api \/ main.*OOM-killed 2 times.*Fix: Raise the memory limit to 768Mi.*\+\$13\/mo/s)
    expect(within(items[0]).getByLabelText('Critical')).toBeTruthy()
    expect(items[1].textContent).toMatch(/no extra cost/)
    expect(items[2].textContent).not.toMatch(/\/mo|no extra cost/)
  })

  it('says when nothing was seen', async () => {
    mockFetch(on('GET', '/api/namespaces/shop/risks', () => json({ namespace: 'shop', historical: false, basis: 'What the pods report now.', currency: 'USD', risks: [] })))
    render(<RisksPanel namespace="shop" />)
    expect(await screen.findByText(/No performance risks seen right now/)).toBeTruthy()
  })

  it('shows a reduction right-sizing holds back', async () => {
    mockFetch(
      on('GET', '/api/namespaces/shop/recommendations', () => json({
        namespace: 'shop', source: 'victoriametrics', historical: true, basis: 'p95.', currency: 'USD', monthlySavings: 0,
        workloads: [{ kind: 'Deployment', name: 'api', replicas: 1, monthlySavings: 0, containers: [{ name: 'main',
          cpu: { request: '2', observed: '100m', action: 'keep', held: 'throttled in 30% of CPU periods at busy times' }, memory: { request: '1Gi', observed: '900Mi', action: 'keep' } }] }],
      })),
      on('GET', '/api/rightsizing/changes', () => json({ applyEnabled: true, guardMinutes: 30, changes: [] })),
    )
    render(<AuthContext.Provider value={viewer}><RecommendationsPanel namespace="shop" /></AuthContext.Provider>)
    expect(await screen.findByText('2 · held: throttled in 30% of CPU periods at busy times')).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Apply' })).toBeNull()
  })
})

describe('Right-sizing policies', () => {
  it('names the policy and offers no apply when it forbids one', async () => {
    const operator: User = { sub: 'user:ops', name: 'ops', role: 'operator', provider: 'local' }
    const reduce = { request: '2', observed: '50m', recommended: '400m', action: 'reduce' }
    mockFetch(
      on('GET', '/api/namespaces/shop/recommendations', () => json({
        namespace: 'shop', source: 'metrics-server', historical: false, basis: 'A reading.', currency: 'USD', monthlySavings: 80,
        workloads: [
          { kind: 'Deployment', name: 'api', replicas: 1, monthlySavings: 40, policy: 'payments', applyForbidden: true, containers: [{ name: 'main', cpu: reduce, memory: { request: '1Gi', action: 'keep' } }] },
          { kind: 'Deployment', name: 'web', replicas: 1, monthlySavings: 40, containers: [{ name: 'main', cpu: reduce, memory: { request: '1Gi', action: 'keep' } }] },
        ],
      })),
      on('GET', '/api/rightsizing/changes', () => json({ applyEnabled: true, guardMinutes: 30, changes: [] })),
    )
    render(<AuthContext.Provider value={operator}><RecommendationsPanel namespace="shop" /></AuthContext.Provider>)
    expect(await screen.findByText(/policy payments · apply forbidden/)).toBeTruthy()
    expect(screen.getAllByRole('button', { name: 'Apply' })).toHaveLength(1)
  })
})
