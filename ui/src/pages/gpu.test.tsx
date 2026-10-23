import { describe, expect, it } from 'vitest'
import { render, screen, within } from '@testing-library/react'
import { json, mockFetch, on } from '../test/fetch'
import ClusterDashboard from './ClusterDashboard'
import NamespaceInsights from './NamespaceInsights'
import Overview from './Overview'
import SettingsPage from './SettingsPage'
import type { NamespaceRow, NodeView, NodesReport, Overview as OverviewData } from '../lib/finops'

const res = { requested: 1, used: 0.5, limit: 2 }
const row = (name: string, gpu?: NamespaceRow['gpu']): NamespaceRow => ({
  name, environment: 'non-production', pods: 2, collected: true, cpu: res, memoryGiB: res, monthlyCost: 500, monthlyUsedCost: 200,
  monthlyIdle: 300, monthlyStorage: 0, monthlyNetwork: 0, monthlyTotal: 500, insights: [], gpu,
})

const overview = (): OverviewData => ({
  generatedAt: '2026-10-22T12:00:00Z', currency: 'USD',
  rates: { cpuCoreHour: 0.033, memoryGiBHour: 0.00375, gpuHour: 0.81, currency: 'USD', basis: 'Heuristic list-price estimate' },
  cluster: { nodes: 3, spotNodes: 0, cpuCores: 24, memoryGiB: 96, cpuRequested: 4, memoryRequestedGiB: 8, cpuUsed: 2, memoryUsedGiB: 4,
    provisionedHourly: 4, requestedHourly: 2, usedHourly: 1, gpus: 4, gpuRequested: 2, gpuHourly: 3.24 },
  monthly: { provisioned: 2920, requested: 1460, used: 730, unallocated: 1460, overprovisioned: 730, storage: 0, network: 0, total: 2920,
    unusedStorage: 0, idleNetwork: 0, gpu: 2365.2, idleGpu: 1123.47 },
  savings: { schedulesMonthly: 0, rightsizingMonthly: 0, rightsizingPending: 0, scheduleCandidatesMonthly: 0 },
  monthToDate: { month: '2026-10', cost: 0, saved: 0, hours: 0, forecast: 0, partial: true },
  history: { top: [], days: [] },
  namespaces: [row('ml', { requested: 2, models: ['NVIDIA-A10G'], monthlyCost: 1182.6, utilization: 0.05, memoryUsedGiB: 3 }), row('web')],
  opportunities: [{ kind: 'idle-gpu', namespace: 'ml', title: 'GPUs in ml are nearly idle', detail: '2 GPUs requested, busy 5% of the last day.', monthlySavings: 1123.47 }],
})

describe('GPUs in the dashboard', () => {
  it('shows what GPUs cost and what sits idle on the overview', async () => {
    mockFetch(on('GET', '/api/finops/overview', () => json(overview())), on('GET', '/api/budgets', () => json({ budgets: [] })))
    render(<Overview onSelectNamespace={() => {}} onNavigate={() => {}} />)
    expect((await screen.findByTestId('gpu-note')).textContent).toBe(' GPUs are $2,365 of the node bill; $1,123 of the requested ones sat idle over the last day.')
    expect(screen.getByText(/4 GPUs, 2 GPUs requested/)).toBeTruthy()
    expect(screen.getByText('GPUs in ml are nearly idle')).toBeTruthy()
  })

  it('marks the namespaces that hold GPUs, with how busy they are', async () => {
    mockFetch(on('GET', '/api/finops/overview', () => json(overview())), on('GET', '/api/namespaces/*', () => json({})))
    render(<NamespaceInsights onSelectNamespace={() => {}} />)
    const user = (await import('@testing-library/user-event')).default.setup()
    const table = screen.queryByRole('button', { name: /Table/i })
    if (table) await user.click(table)
    const badge = await screen.findByText(/GPU ×2/)
    expect(badge.textContent).toBe('GPU ×2 · 5%')
    expect(badge.closest('[title]')?.getAttribute('title')).toBe('NVIDIA-A10G: $1,183/mo, busy 5% of the last day')
    expect(screen.queryAllByText(/GPU ×/)).toHaveLength(1)
  })

  it('saves a GPU rate with the prices and shows the one in effect', async () => {
    window.history.replaceState(null, '', '/settings/pricing')
    const settings = { providers: {}, integrations: {}, auth: { disableLocalLogin: false }, calendars: [],
      pricing: { gpuHour: '1.2', effective: { cpuCoreHour: 0.033, memoryGiBHour: 0.00375, gpuHour: 1.2, currency: 'USD', basis: 'Custom rates' } } }
    const fetch = mockFetch(on('GET', '/api/settings', () => json(settings)), on('PUT', '/api/settings', () => json(settings)))
    render(<SettingsPage section="pricing" />)
    const user = (await import('@testing-library/user-event')).default.setup()
    const input = await screen.findByLabelText('Per GPU-hour') as HTMLInputElement
    expect(input.value).toBe('1.2')
    expect(screen.getByText(/In effect now: 1.20 USD a GPU-hour on average/)).toBeTruthy()
    await user.clear(input)
    await user.type(input, '0.9')
    await user.click(screen.getByRole('button', { name: 'Save' }))
    await screen.findByText('Saved')
    const put = fetch.mock.calls.find(([, init]) => init?.method === 'PUT')!
    expect(JSON.parse(put[1]!.body as string).pricing.gpuHour).toBe('0.9')
  })

  it("shows a GPU node's GPUs and what pods request of them", async () => {
    const r = { capacity: 8, allocatable: 8, requested: 2, used: 1 }
    const node = (name: string, gpu: Partial<NodeView>): NodeView => ({
      name, status: 'Ready', unschedulable: false, capacityType: 'on-demand', createdAt: '2026-10-01T00:00:00Z', pods: 3, maxPods: 110,
      metricsAvailable: true, cpu: r, memoryGiB: r, monthlyCost: 100, monthlyRequested: 50, monthlyUsed: 20, namespaces: [], daemonSetCpu: 0, daemonSetMemoryGiB: 0, ...gpu,
    })
    const report: NodesReport = {
      generatedAt: '2026-10-22T12:00:00Z', currency: 'USD', rates: overview().rates,
      summary: { nodes: 2, ready: 2, spot: 0, unschedulable: 0, pendingPods: 0, pods: 6, maxPods: 220, cpu: r, memoryGiB: r, monthlyCost: 200, monthlyUnrequested: 100, gpus: 4, gpuRequested: 1 },
      pools: [], nodes: [node('cpu-1', {}), node('gpu-1', { gpus: 4, gpuRequested: 1, gpuModel: 'NVIDIA-A10G', gpuHourly: 0.81 })], findings: [], recommendations: [],
    }
    mockFetch(on('GET', '/api/finops/nodes', () => json({ k8sVersion: 'v1.35.0', report })))
    render(<ClusterDashboard />)
    const gpuTile = (await screen.findByText('gpu-1')).closest('button')!
    expect(within(gpuTile).getByText('GPU · NVIDIA-A10G')).toBeTruthy()
    expect(within(gpuTile).getByText('25% requested · 1.00/4.00 GPUs')).toBeTruthy()
    expect(within(screen.getByText('cpu-1').closest('button')!).queryByText(/GPU/)).toBeNull()
  })
})
