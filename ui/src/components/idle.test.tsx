import { describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { json, mockFetch, on } from '../test/fetch'
import ScheduleWizard from './ScheduleWizard'
import ScheduleDetails from './ScheduleDetails'
import { shortDuration, statusLine } from '../lib/schedule'
import type { ScalingConfig, ScalingGroup } from '../lib/types'

const posted = (fetch: ReturnType<typeof mockFetch>, url: string, method: string) =>
  JSON.parse(fetch.mock.calls.find(([u, init]) => u === url && init?.method === method)![1]!.body as string)

describe('Sleep when unused', () => {
  it('saves a schedule that sleeps after an hour without requests', async () => {
    const fetch = mockFetch(
      on('GET', '/api/settings', () => json({ calendars: [] })),
      on('POST', '/api/scaling/groups', (_, init) => json(JSON.parse(init!.body as string), 201)),
    )
    const onSaved = vi.fn()
    render(<ScheduleWizard kind="group" namespaces={['shop-dev']} groups={[]} onClose={() => {}} onSaved={onSaved} />)
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'shop-dev' }))
    await user.selectOptions(screen.getByRole('combobox', { name: /Sleep when unused for/ }), '1h')
    await user.click(screen.getByRole('button', { name: 'Create schedule' }))
    await waitFor(() => expect(onSaved).toHaveBeenCalled())
    expect(posted(fetch, '/api/scaling/groups', 'POST').spec.idleAfter).toBe('1h')
  })

  it('shows a saved period, keeps one it does not offer, and switches it off', async () => {
    const fetch = mockFetch(
      on('GET', '/api/settings', () => json({ calendars: [] })),
      on('PUT', '/api/scaling/configs/lab', (_, init) => json(JSON.parse(init!.body as string))),
    )
    const config = { metadata: { name: 'lab' }, spec: { targetNamespace: 'lab', idleAfter: '2h0m0s' } } as unknown as ScalingConfig
    const onSaved = vi.fn()
    const { unmount } = render(<ScheduleWizard kind="config" existing={config} namespaces={['lab']} groups={[]} onClose={() => {}} onSaved={onSaved} />)
    const select = screen.getByRole('combobox', { name: /Sleep when unused for/ }) as HTMLSelectElement
    expect(select.value).toBe('2h')
    const user = userEvent.setup()
    await user.selectOptions(select, 'never')
    await user.click(screen.getByRole('button', { name: /Save/ }))
    await waitFor(() => expect(onSaved).toHaveBeenCalled())
    expect(posted(fetch, '/api/scaling/configs/lab', 'PUT').spec.idleAfter).toBeUndefined()
    unmount()

    const odd = { ...config, spec: { ...config.spec, idleAfter: '1m0s' } }
    render(<ScheduleWizard kind="config" existing={odd} namespaces={['lab']} groups={[]} onClose={() => {}} onSaved={() => {}} />)
    expect((screen.getByRole('combobox', { name: /Sleep when unused for/ }) as HTMLSelectElement).value).toBe('1m')
  })

  it('reads Go durations the way the editor offers them', () => {
    expect(shortDuration('1h0m0s')).toBe('1h')
    expect(shortDuration('30m0s')).toBe('30m')
    expect(shortDuration('1h30m0s')).toBe('90m')
    expect(shortDuration('45s')).toBe('45s')
    expect(shortDuration(undefined)).toBe('')
    expect(shortDuration('1.5h')).toBe('1.5h')
  })

  it('says a schedule sleeps for being unused, and until when', () => {
    const line = statusLine({ phase: 'ScaledDown', status: { mode: 'Idle', desiredState: 'Down', nextTransition: { time: '2026-10-23T08:00:00Z', desiredState: 'Up' } } as never })
    expect(line.title).toBe('Asleep, unused')
    expect(line.detail).toMatch(/^starts .*, or when someone starts it$/)
    expect(statusLine({ phase: 'ScaledDown', status: { mode: 'Idle' } as never }).detail).toBe('until someone starts it')
  })

  it('shows what the Idle condition says on the schedule', () => {
    const group = {
      metadata: { name: 'shop' }, spec: { category: 'x', namespaces: ['shop-dev'], idleAfter: '1h0m0s' },
      status: { phase: 'ScaledUp', conditions: [{ type: 'Idle', status: 'False', reason: 'NoTrafficData', message: 'No Envoy Gateway, Istio or ingress-nginx request metrics for these namespaces in VictoriaMetrics, so they are never treated as unused.' }] },
    } as unknown as ScalingGroup
    render(<ScheduleDetails target={{ kind: 'group', group }} groups={[group]} tab="overview" onTab={() => {}} onClose={() => {}} onSelectNamespace={() => {}} canOperate={false} canAdmin={false}
      onStart={() => {}} onStop={() => {}} onResume={() => {}} onEdit={() => {}} onDelete={() => {}} />)
    const note = screen.getByTestId('idle-note')
    expect(note.textContent).toMatch(/^Sleep when unused: No Envoy Gateway/)
    expect(note.className).toContain('amber')
  })
})
