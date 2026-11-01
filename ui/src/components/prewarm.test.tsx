import { describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { json, mockFetch, on } from '../test/fetch'
import ScheduleWizard from './ScheduleWizard'
import { statusLine, upHours, weekGrid } from '../lib/schedule'
import type { ScalingConfig } from '../lib/types'

const posted = (fetch: ReturnType<typeof mockFetch>, url: string, method: string) =>
  JSON.parse(fetch.mock.calls.find(([u, init]) => u === url && init?.method === method)![1]!.body as string)

describe('Start ahead of the schedule', () => {
  it('saves a prewarm that follows the wake-ups', async () => {
    const fetch = mockFetch(
      on('GET', '/api/settings', () => json({ calendars: [] })),
      on('POST', '/api/scaling/groups', (_, init) => json(JSON.parse(init!.body as string), 201)),
    )
    const onSaved = vi.fn()
    render(<ScheduleWizard kind="group" namespaces={['shop-dev']} groups={[]} onClose={() => {}} onSaved={onSaved} />)
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'shop-dev' }))
    await user.selectOptions(screen.getByRole('combobox', { name: 'Start ahead of the schedule' }), 'auto')
    await user.click(screen.getByRole('button', { name: 'Create schedule' }))
    await waitFor(() => expect(onSaved).toHaveBeenCalled())
    expect(posted(fetch, '/api/scaling/groups', 'POST').spec.prewarm).toEqual({ auto: true })
  })

  it('shows a saved lead and switches it off', async () => {
    const fetch = mockFetch(
      on('GET', '/api/settings', () => json({ calendars: [] })),
      on('PUT', '/api/scaling/configs/lab', (_, init) => json(JSON.parse(init!.body as string))),
    )
    const config = { metadata: { name: 'lab' }, spec: { targetNamespace: 'lab', prewarm: { minutes: 15 } } } as unknown as ScalingConfig
    const onSaved = vi.fn()
    render(<ScheduleWizard kind="config" existing={config} namespaces={['lab']} groups={[]} onClose={() => {}} onSaved={onSaved} />)
    const select = screen.getByRole('combobox', { name: 'Start ahead of the schedule' }) as HTMLSelectElement
    expect(select.value).toBe('15')
    const user = userEvent.setup()
    await user.selectOptions(select, 'no')
    await user.click(screen.getByRole('button', { name: /Save/ }))
    await waitFor(() => expect(onSaved).toHaveBeenCalled())
    expect(posted(fetch, '/api/scaling/configs/lab', 'PUT').spec.prewarm).toBeUndefined()
  })

  it('says a namespace is up early, and the week shows the lead', () => {
    expect(statusLine({ phase: 'ScaledUp', status: { mode: 'Prewarm', desiredState: 'Up', prewarmMinutes: 9 } })).toMatchObject({ tone: 'up', title: 'Up early', detail: 'started 9 min ahead of its schedule to be ready on time' })
    const officeHours = [{ days: [1, 2, 3, 4, 5], startTime: '08:00', endTime: '18:00', timezone: 'UTC' }]
    expect(upHours(weekGrid(officeHours, 30))).toBe(upHours(weekGrid(officeHours)) + 5 * 0.5)
  })
})
