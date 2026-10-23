import { describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { json, mockFetch, on } from '../test/fetch'
import ScheduleWizard from './ScheduleWizard'
import SchedulePreview from './SchedulePreview'
import ScheduleDetails from './ScheduleDetails'
import type { ScalingGroup } from '../lib/types'

describe('GitOps in the dashboard', () => {
  it('saves a schedule that pauses GitOps', async () => {
    const fetch = mockFetch(
      on('GET', '/api/settings', () => json({ calendars: [] })),
      on('POST', '/api/scaling/groups', (_, init) => json(JSON.parse(init!.body as string), 201)),
    )
    const onSaved = vi.fn()
    render(<ScheduleWizard kind="group" namespaces={['shop-dev']} groups={[]} onClose={() => {}} onSaved={onSaved} />)
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'shop-dev' }))
    await user.click(screen.getByRole('checkbox', { name: /Pause GitOps while down/ }))
    await user.click(screen.getByRole('button', { name: 'Create schedule' }))
    await waitFor(() => expect(onSaved).toHaveBeenCalled())
    const body = JSON.parse(fetch.mock.calls.find(([url, init]) => url === '/api/scaling/groups' && init?.method === 'POST')![1]!.body as string)
    expect(body.spec.pauseGitOps).toBe(true)
  })

  it('names the GitOps owner of a workload in the preview', async () => {
    mockFetch(on('POST', '/api/scaling/preview', () => json({
      namespaces: [{ name: 'shop-dev', source: 'listed', cronJobs: 0, workloads: [{ kind: 'Deployment', name: 'api', replicas: 2, cpu: '1', memory: '1Gi', gitOps: { kind: 'Application', name: 'shop' } }] }],
      cloudTargets: [], intervals: [], upShare: 0.3, savings: { hourly: 1, monthly: 500, currency: 'USD', basis: 'x' }, problems: [],
      warnings: ['Workloads in shop-dev are managed by Argo CD application shop: unless it ignores the replicas, its sync brings them back.'],
    })))
    render(<SchedulePreview kind="group" spec={{ namespaces: ['shop-dev'] }} />)
    expect(await screen.findByText(/api ×2 \(Argo CD shop\)/)).toBeTruthy()
    expect(screen.getByText(/managed by Argo CD application shop/)).toBeTruthy()
  })

  it('shows what the GitOps condition says on the schedule', () => {
    const group = {
      metadata: { name: 'shop' }, spec: { category: 'x', namespaces: ['shop-dev'] },
      status: { phase: 'ScaledUp', conditions: [{ type: 'GitOps', status: 'True', reason: 'NotPaused', message: 'The workloads are managed by Flux Kustomization flux-system/apps.' }] },
    } as unknown as ScalingGroup
    render(<ScheduleDetails target={{ kind: 'group', group }} groups={[group]} tab="overview" onTab={() => {}} onClose={() => {}} onSelectNamespace={() => {}} canOperate={false} canAdmin={false}
      onStart={() => {}} onStop={() => {}} onResume={() => {}} onEdit={() => {}} onDelete={() => {}} />)
    expect(screen.getByTestId('gitops-note').textContent).toBe('GitOps: The workloads are managed by Flux Kustomization flux-system/apps.')
  })
})
