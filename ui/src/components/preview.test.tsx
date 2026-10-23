import { describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { json, mockFetch, on } from '../test/fetch'
import SchedulePreview from './SchedulePreview'
import ScheduleWizard from './ScheduleWizard'
import { statusLine } from '../lib/schedule'
import type { SchedulePreview as Preview } from '../lib/types'

const now = Date.now()
const preview: Preview = {
  namespaces: [
    { name: 'shop-dev', source: 'listed', cronJobs: 1, workloads: [
      { kind: 'Deployment', name: 'api', replicas: 2, cpu: '500m', memory: '1Gi' },
      { kind: 'Deployment', name: 'redis', replicas: 1, excluded: true, cpu: '1', memory: '2Gi' },
    ] },
    { name: 'pr-7', source: 'selected', cronJobs: 0, workloads: [] },
    { name: 'pay-dev', source: 'listed', ownedBy: 'pay', cronJobs: 0, workloads: null },
  ],
  cloudTargets: [],
  intervals: [{ from: new Date(now - 3600e3).toISOString(), to: new Date(now + 14 * 86400e3).toISOString(), active: false, reason: 'Christmas' }],
  upShare: 0.36, savings: { hourly: 0.05, monthly: 23.4, currency: 'EUR', basis: 'custom rates' },
  problems: ['calendar "x" does not exist'], warnings: ['pay-dev is already scaled by pay and will be left to it.'],
}

describe('SchedulePreview', () => {
  it('shows the saving, problems, the two weeks and what it scales', async () => {
    mockFetch(on('POST', '/api/scaling/preview', () => json(preview)))
    render(<SchedulePreview kind="group" spec={{ namespaces: ['shop-dev'] }} />)
    const p = await screen.findByTestId('schedule-preview')
    expect(p.textContent).toMatch(/Saves about .*23.* a month: 1 workload stopped, up 36% of the next four weeks/)
    expect(within(p).getByText('calendar "x" does not exist')).toBeTruthy()
    expect(within(p).getByText(/already scaled by pay/)).toBeTruthy()
    expect(within(p).getByText(/api ×2 · kept running: redis · 1 CronJob paused/)).toBeTruthy()
    expect(within(p).getByText('by label')).toBeTruthy()
    expect(within(p).getByText('scaled by pay')).toBeTruthy()
    expect(within(p).getAllByText('Christmas').length).toBeGreaterThan(0)
  })
  it('says when it cannot work it out', async () => {
    mockFetch(on('POST', '/api/scaling/preview', () => json({ error: 'boom' }, 500)))
    render(<SchedulePreview kind="group" spec={{}} />)
    expect(await screen.findByText('Could not preview the schedule: boom')).toBeTruthy()
  })
})

describe('ScheduleWizard preview and dry run', () => {
  it('previews the draft as it would be saved, and saves a dry run', async () => {
    const fetch = mockFetch(
      on('GET', '/api/settings', () => json({ calendars: [] })),
      on('POST', '/api/scaling/preview', () => json(preview)),
      on('POST', '/api/scaling/groups', (_, init) => json(JSON.parse(init!.body as string), 201)),
    )
    const onSaved = vi.fn()
    render(<ScheduleWizard kind="group" namespaces={['shop-dev']} groups={[]} onClose={() => {}} onSaved={onSaved} />)
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'shop-dev' }))
    await user.click(screen.getByRole('checkbox', { name: /Dry run/ }))
    await user.click(screen.getByRole('button', { name: /Preview/ }))
    await screen.findByTestId('schedule-preview')
    const sent = JSON.parse(fetch.mock.calls.find(([url, init]) => url === '/api/scaling/preview' && init?.method === 'POST')![1]!.body as string)
    expect(sent.kind).toBe('group')
    expect(sent.spec.namespaces).toEqual(['shop-dev'])
    expect(sent.spec.schedules[0]).toMatchObject({ days: [1, 2, 3, 4, 5], startTime: '08:00', endTime: '20:00' })
    await user.click(screen.getByRole('button', { name: 'Create schedule' }))
    await waitFor(() => expect(onSaved).toHaveBeenCalled())
    const body = JSON.parse(fetch.mock.calls.find(([url, init]) => url === '/api/scaling/groups' && init?.method === 'POST')![1]!.body as string)
    expect(body.spec.dryRun).toBe(true)
  })
})

describe('statusLine for a dry run', () => {
  it('says what would happen, without the boilerplate', () => {
    const line = statusLine({ phase: 'DryRun', status: { desiredState: 'Down', conditions: [{ type: 'DryRun', status: 'True', message: 'Would scale down 2 workload(s) in 1 namespace(s) now. Nothing is scaled while the group\'s spec.dryRun is true.' }] } as never })
    expect(line).toEqual({ tone: 'manual', title: 'Dry run · would be down', detail: 'Would scale down 2 workload(s) in 1 namespace(s) now.' })
  })
})
