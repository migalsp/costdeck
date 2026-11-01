import { useState } from 'react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { json, mockFetch, on } from '../test/fetch'
import StartOrderEditor, { type StartOrder } from './StartOrderEditor'
import ScheduleEditor from './ScheduleEditor'
import WeekTimeline from './WeekTimeline'
import ScheduleCard, { type ScheduleCardProps } from './ScheduleCard'
import ScheduleSuggestion from './ScheduleSuggestion'
import { defaultPlan, type SchedulePlan } from '../lib/schedule'
import type { ExternalTarget } from '../lib/types'

afterEach(() => { vi.useRealTimers() })

function StartOrderHarness({ initial, namespaces, discovered, onChange }: { initial: StartOrder; namespaces: string[]; discovered?: ExternalTarget[]; onChange: (v: StartOrder) => void }) {
  const [value, setValue] = useState(initial)
  return <StartOrderEditor namespaces={namespaces} discovered={discovered} value={value} onChange={v => { onChange(v); setValue(v) }} />
}

function ScheduleHarness({ onChange, allowOnDemand }: { onChange: (p: SchedulePlan) => void; allowOnDemand?: boolean }) {
  const [plan, setPlan] = useState(defaultPlan('UTC'))
  return <ScheduleEditor plan={plan} allowOnDemand={allowOnDemand} onChange={p => { onChange(p); setPlan(p) }} />
}

const stage = (n: number) => screen.getByText(`Stage ${n}`, { selector: 'span' }).closest('.rounded-xl') as HTMLElement
const orders: ExternalTarget = { provider: 'aws', type: 'rds', identifier: 'arn:aws:rds:eu-west-1:1:db:orders', name: 'orders-db', region: 'eu-west-1' }

describe('Start order editor', () => {
  it('arranges namespaces into stages and reports the sequence', async () => {
    const onChange = vi.fn()
    render(<StartOrderHarness initial={{}} namespaces={['api', 'db', 'web']} onChange={onChange} />)
    const user = userEvent.setup()
    expect(screen.queryByText('Stage 1')).toBeNull()
    await user.selectOptions(screen.getByRole('combobox', { name: 'Move db' }), 'New last stage')
    expect(onChange).toHaveBeenLastCalledWith({ sequence: ['db'], externalTargets: undefined })
    expect(within(stage(1)).getByText('starts first, stops last')).toBeTruthy()
    await user.selectOptions(screen.getByRole('combobox', { name: 'Move api' }), 'Stage 1')
    expect(onChange).toHaveBeenLastCalledWith({ sequence: ['db api'], externalTargets: undefined })
    await user.selectOptions(screen.getByRole('combobox', { name: 'Move web' }), 'New last stage')
    expect(onChange).toHaveBeenLastCalledWith({ sequence: ['db api', 'web'], externalTargets: undefined })
    expect(screen.getByText('Every namespace has a stage.')).toBeTruthy()
    expect(within(stage(1)).getByTitle('Move stage up')).toHaveProperty('disabled', true)
    expect(within(stage(2)).getByTitle('Move stage down')).toHaveProperty('disabled', true)

    await user.click(within(stage(1)).getByTitle('Move stage down'))
    expect(onChange).toHaveBeenLastCalledWith({ sequence: ['web', 'db api'], externalTargets: undefined })
    await user.selectOptions(screen.getByRole('combobox', { name: 'Move api' }), 'Not placed (starts last)')
    expect(onChange).toHaveBeenLastCalledWith({ sequence: ['web', 'db'], externalTargets: undefined })
    await user.selectOptions(screen.getByRole('combobox', { name: 'Move web' }), 'Stage 2')
    expect(onChange).toHaveBeenLastCalledWith({ sequence: ['db web'], externalTargets: undefined })
  })

  it('moves a namespace dropped on the new-stage area into a new last stage', () => {
    const onChange = vi.fn()
    render(<StartOrderHarness initial={{ sequence: ['db'] }} namespaces={['api', 'db']} onChange={onChange} />)
    fireEvent.drop(screen.getByText('Drop here to start a new last stage'), { dataTransfer: { getData: () => 'api' } })
    expect(onChange).toHaveBeenLastCalledWith({ sequence: ['db', 'api'], externalTargets: undefined })
  })

  it('leaves out namespaces that are no longer part of the schedule', async () => {
    const onChange = vi.fn()
    render(<StartOrderHarness initial={{ sequence: ['gone api', 'old'] }} namespaces={['api', 'web']} onChange={onChange} />)
    expect(screen.queryByText('gone')).toBeNull()
    expect(screen.queryByText('Stage 2')).toBeNull()
    await userEvent.setup().selectOptions(screen.getByRole('combobox', { name: 'Move web' }), 'Stage 1')
    expect(onChange).toHaveBeenLastCalledWith({ sequence: ['api web'], externalTargets: undefined })
  })

  it('adds a discovered cloud resource to the first stage and removes it again', async () => {
    const onChange = vi.fn()
    render(<StartOrderHarness initial={{ sequence: ['api'] }} namespaces={['api']} discovered={[orders]} onChange={onChange} />)
    const user = userEvent.setup()
    await user.selectOptions(screen.getByDisplayValue('Add a database or instance…'), 'orders-db · AWS rds · eu-west-1')
    expect(onChange).toHaveBeenLastCalledWith({ sequence: [`ext:${orders.identifier} api`], externalTargets: [orders] })
    expect(within(stage(1)).getByText('orders-db')).toBeTruthy()
    expect(screen.getByText('All discovered resources are added.')).toBeTruthy()
    const move = screen.getByRole('combobox', { name: 'Move orders-db' })
    expect(within(move).queryByRole('option', { name: 'Not placed (starts last)' })).toBeNull()
    await user.selectOptions(move, 'Remove from schedule')
    expect(onChange).toHaveBeenLastCalledWith({ sequence: ['api'], externalTargets: undefined })
    expect(screen.queryByText('orders-db')).toBeNull()
  })

  it('says how to get cloud resources, and hides the section where discovery is not offered', () => {
    const { unmount } = render(<StartOrderHarness initial={{}} namespaces={['api']} discovered={[]} onChange={() => {}} />)
    expect(screen.getByText(/Connect AWS, Azure or Google Cloud under Settings/)).toBeTruthy()
    unmount()
    render(<StartOrderHarness initial={{}} namespaces={['api']} onChange={() => {}} />)
    expect(screen.queryByText('Cloud resources')).toBeNull()
  })
})

describe('Schedule editor', () => {
  const times = () => [...document.querySelectorAll('input[type="time"]')] as HTMLInputElement[]

  it('previews working hours and explains a plan that cannot be saved instead of drawing it', async () => {
    const onChange = vi.fn()
    render(<ScheduleHarness onChange={onChange} />)
    expect(screen.getByText('Weekdays 08:00–20:00')).toBeTruthy()
    expect(screen.getByText(/a week, scaled down/).textContent).toBe('Up 60 h a week, scaled down 108 h (64%) · times in UTC')
    const user = userEvent.setup()
    for (const day of ['Mon', 'Tue', 'Wed', 'Thu', 'Fri']) await user.click(screen.getByRole('button', { name: day }))
    expect(screen.getByText('Pick at least one day.')).toBeTruthy()
    expect(screen.queryByText(/a week, scaled down/)).toBeNull()
    await user.click(screen.getByRole('button', { name: 'Sat' }))
    await user.click(screen.getByRole('button', { name: 'Sun' }))
    expect(screen.getByText('Weekends 08:00–20:00')).toBeTruthy()
    expect(onChange).toHaveBeenLastCalledWith(expect.objectContaining({ preset: 'workhours', days: [6, 0] }))

    fireEvent.change(times()[1], { target: { value: '08:00' } })
    expect(screen.getByText('Start and end time are the same.')).toBeTruthy()
    fireEvent.change(times()[1], { target: { value: '02:00' } })
    expect(screen.getByText('Weekends 08:00–02:00 (overnight)')).toBeTruthy()
  })

  it('switches between the presets', async () => {
    const onChange = vi.fn()
    render(<ScheduleHarness onChange={onChange} />)
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: /^Work week/ }))
    expect(screen.getByText('Mon 00:00 → Fri 23:59, non-stop')).toBeTruthy()
    await user.selectOptions(screen.getAllByRole('combobox')[1], 'Sat')
    expect(screen.getByText('Mon 00:00 → Sat 23:59, non-stop')).toBeTruthy()

    await user.click(screen.getByRole('button', { name: /^On demand/ }))
    expect(screen.getByText(/^Runs only while a schedule that depends on it is up/)).toBeTruthy()
    expect(screen.getByText('No fixed hours: up only while something that depends on it is up.')).toBeTruthy()
    expect(screen.queryByText('Time zone')).toBeNull()

    await user.click(screen.getByRole('button', { name: /^Working hours/ }))
    await user.selectOptions(screen.getByDisplayValue('UTC'), 'Asia/Tokyo')
    expect(onChange).toHaveBeenLastCalledWith(expect.objectContaining({ preset: 'workhours', timezone: 'Asia/Tokyo' }))
    expect(screen.getByText(/times in Asia\/Tokyo$/)).toBeTruthy()
  })

  it('offers on demand only where it makes sense', () => {
    render(<ScheduleHarness onChange={() => {}} allowOnDemand={false} />)
    expect(screen.queryByRole('button', { name: /^On demand/ })).toBeNull()
    expect(screen.getByRole('button', { name: /^Custom/ })).toBeTruthy()
  })

  it('edits several custom windows', async () => {
    render(<ScheduleHarness onChange={() => {}} />)
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: /^Custom/ }))
    expect(screen.getByText('1 custom window')).toBeTruthy()
    expect(screen.queryByTitle('Remove window')).toBeNull()
    await user.click(screen.getByRole('button', { name: 'Add window' }))
    expect(screen.getByText('2 custom windows')).toBeTruthy()
    expect(screen.getAllByTitle('Remove window')).toHaveLength(2)
    const second = screen.getAllByTitle('Remove window')[1].parentElement as HTMLElement
    for (const day of ['Mon', 'Tue', 'Wed', 'Thu', 'Fri']) await user.click(within(second).getByRole('button', { name: day }))
    expect(screen.getByText('Every daily window needs at least one day.')).toBeTruthy()
    await user.selectOptions(within(second).getByDisplayValue('Every selected day'), 'One continuous stretch')
    expect(screen.queryByText('Every daily window needs at least one day.')).toBeNull()
    await user.click(screen.getAllByTitle('Remove window')[0])
    expect(screen.getByText('1 custom window')).toBeTruthy()
    expect(screen.getByText(/a week, scaled down/).textContent).toMatch(/^Up 108 h a week/)
  })
})

describe('Week timeline', () => {
  const row = (day: string) => screen.getByText(day, { selector: 'span' }).nextElementSibling as HTMLElement

  it('names the up hours of a hovered day and marks the current time', async () => {
    vi.useFakeTimers({ toFake: ['Date'] })
    vi.setSystemTime(new Date('2026-11-02T10:10:00Z'))
    render(<WeekTimeline schedules={[{ days: [1, 2, 3, 4, 5], startTime: '08:00', endTime: '20:00', timezone: 'UTC' }]} />)
    const user = userEvent.setup()
    await user.hover(row('Mon'))
    expect(screen.getByRole('tooltip').textContent).toBe('Mon: up 08:00–20:00 · now 10:00')
    await user.hover(row('Sat'))
    expect(screen.getByRole('tooltip').textContent).toBe('Sat: scaled down all day')
    expect(screen.getAllByRole('tooltip')).toHaveLength(1)
  })

  it('runs an overnight window into the next day and starts early by the lead', async () => {
    render(<WeekTimeline schedules={[{ days: [5], startTime: '22:00', endTime: '06:00', timezone: 'UTC' }]} leadMinutes={60} />)
    const user = userEvent.setup()
    await user.hover(row('Fri'))
    expect(screen.getByRole('tooltip').textContent).toMatch(/^Fri: up 21:00–24:00/)
    await user.hover(row('Sat'))
    expect(screen.getByRole('tooltip').textContent).toMatch(/^Sat: up 00:00–06:00/)
    expect(screen.getByText(/a week, scaled down/).textContent).toBe('Up 9 h a week, scaled down 159 h (95%) · times in UTC')
  })

  it('treats no usable schedule as always up, like the operator', async () => {
    render(<WeekTimeline schedules={[]} timezone="Europe/Berlin" />)
    await userEvent.setup().hover(row('Wed'))
    expect(screen.getByRole('tooltip').textContent).toMatch(/^Wed: up all day/)
    expect(screen.getByText(/a week, scaled down/).textContent).toBe('Up 168 h a week, scaled down 0 h (0%) · times in Europe/Berlin')
  })

  it('shows an on-demand schedule as down, and the compact form says the weekly hours on hover', async () => {
    const { unmount } = render(<WeekTimeline schedules={[{ days: [1], startTime: '08:00', endTime: '20:00' }]} onDemand />)
    const user = userEvent.setup()
    await user.hover(row('Mon'))
    expect(screen.getByRole('tooltip').textContent).toMatch(/^Mon: up only on demand/)
    expect(screen.getByText('No fixed hours: up only while something that depends on it is up.')).toBeTruthy()
    unmount()
    render(<WeekTimeline schedules={[{ days: [1], startTime: '08:00', endTime: '20:00', timezone: 'Asia/Tokyo' }]} compact />)
    await user.hover(screen.getAllByText('M', { selector: 'span' })[0].nextElementSibling as HTMLElement)
    expect(screen.getByRole('tooltip').textContent).toMatch(/^Mon: up 08:00–20:00.*12 h up a week · Asia\/Tokyo$/)
  })
})

describe('Schedule card', () => {
  const props = (over: Partial<ScheduleCardProps> = {}): ScheduleCardProps => ({
    name: 'team-a',
    namespaces: ['shop', 'cart'],
    spec: { schedules: [{ days: [1, 2, 3, 4, 5], startTime: '08:00', endTime: '20:00', timezone: 'UTC' }] },
    status: { phase: 'ScaledUp', desiredState: 'Up', mode: 'Schedule' },
    canOperate: true,
    canAdmin: false,
    onOpen: vi.fn(), onStart: vi.fn(), onStop: vi.fn(), onResume: vi.fn(), onEdit: vi.fn(),
    ...over,
  })

  it('offers the one action that fits without opening the details', async () => {
    const p = props()
    const { rerender } = render(<ScheduleCard {...p} />)
    const user = userEvent.setup()
    expect(screen.getByText('Weekdays 08:00–20:00 · UTC')).toBeTruthy()
    expect(screen.queryByRole('button', { name: /Start now|Follow schedule|Edit/ })).toBeNull()
    await user.click(screen.getByRole('button', { name: 'Scale down now' }))
    expect(p.onStop).toHaveBeenCalledOnce()
    expect(p.onOpen).not.toHaveBeenCalled()

    rerender(<ScheduleCard {...p} status={{ phase: 'ScaledDown', desiredState: 'Down', mode: 'Schedule' }} />)
    await user.click(screen.getByRole('button', { name: 'Start now' }))
    expect(p.onStart).toHaveBeenCalledOnce()

    rerender(<ScheduleCard {...p} spec={{ ...p.spec, active: true }} status={{ phase: 'ScaledUp', desiredState: 'Up', mode: 'ManualUp' }} />)
    expect(screen.getByText('Kept up manually')).toBeTruthy()
    await user.click(screen.getByRole('button', { name: 'Follow schedule' }))
    expect(p.onResume).toHaveBeenCalledOnce()
    expect(p.onOpen).not.toHaveBeenCalled()

    await user.click(screen.getByText('team-a'))
    expect(p.onOpen).toHaveBeenCalledWith('overview')
  })

  it('disables the action while busy, lets an admin edit, and gives a viewer nothing', async () => {
    const p = props({ busy: true, canAdmin: true })
    const { rerender } = render(<ScheduleCard {...p} />)
    expect(screen.getByRole('button', { name: 'Scale down now' })).toHaveProperty('disabled', true)
    await userEvent.setup().click(screen.getByRole('button', { name: 'Edit' }))
    expect(p.onEdit).toHaveBeenCalledOnce()
    expect(p.onOpen).not.toHaveBeenCalled()
    rerender(<ScheduleCard {...p} busy={false} canOperate={false} canAdmin={false} />)
    expect(screen.queryAllByRole('button')).toHaveLength(0)
  })

  it('shows progress while scaling and opens the activity from it', async () => {
    const p = props({ namespaces: ['a', 'b', 'c'], status: { phase: 'ScalingUp', desiredState: 'Up', mode: 'Schedule', namespacesReady: 1, namespacesTotal: 3 } })
    render(<ScheduleCard {...p} />)
    await userEvent.setup().click(screen.getByRole('button', { name: /1 of 3 namespaces ready/ }))
    expect(p.onOpen).toHaveBeenCalledWith('activity')
    expect(p.onOpen).toHaveBeenCalledTimes(1)
  })

  it('lists dependencies, conflicts, the saving and at most six namespaces', () => {
    render(<ScheduleCard {...props({
      namespaces: ['n1', 'n2', 'n3', 'n4', 'n5', 'n6', 'n7', 'n8'],
      spec: { dependsOn: ['db'], schedules: [] },
      status: { phase: 'ScaledDown', desiredState: 'Down', mode: 'Schedule', estimatedHourlySavings: '1.5', currency: 'USD', conflictingNamespaces: ['n9'] },
    })} />)
    expect(screen.getByText('Starts after db')).toBeTruthy()
    expect(screen.getByText('Skipped, already in another schedule: n9')).toBeTruthy()
    expect(screen.getByText('saving ~$1.50/h')).toBeTruthy()
    expect(screen.getByText('n6')).toBeTruthy()
    expect(screen.queryByText('n7')).toBeNull()
    expect(screen.getByText('+2 more')).toBeTruthy()
    expect(screen.getByText('No schedule: always on')).toBeTruthy()
  })
})

describe('Schedule suggestion', () => {
  const weekdays = [{ days: [1, 2, 3, 4, 5], startTime: '08:00', endTime: '18:00', timezone: 'Europe/Berlin' }]
  const suggestion = (over: object) => ({
    namespace: 'shop dev', reason: 'pattern', schedules: weekdays, upShare: 0.3, busy: [], up: [], days: 7, timezone: 'Europe/Berlin',
    monthlySaving: 142.4, currency: 'USD', source: 'victoriametrics', ...over,
  })

  it('fills in the schedule only when usage shows a pattern', async () => {
    let answer: object = suggestion({})
    const fetch = mockFetch(on('GET', '/api/namespaces/shop%20dev/schedule-suggestion', () => json(answer)))
    const onApply = vi.fn()
    render(<ScheduleSuggestion namespace="shop dev" timezone="Europe/Berlin" onApply={onApply} />)
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'Suggest from usage' }))
    await waitFor(() => expect(onApply).toHaveBeenCalledWith(weekdays))
    expect(fetch.mock.calls[0][0]).toBe('/api/namespaces/shop%20dev/schedule-suggestion?timezone=Europe%2FBerlin')
    expect(screen.getByText(/up 30% of the week.*about \$142 a month\./)).toBeTruthy()

    answer = suggestion({ reason: 'idle', schedules: [] })
    await user.click(screen.getByRole('button', { name: 'Suggest from usage' }))
    expect(await screen.findByText('shop dev did nothing in the last 7 days: it could stay scaled down.')).toBeTruthy()
    expect(onApply).toHaveBeenCalledTimes(1)
  })

  it('says it is reading while it waits, and why it could not suggest', async () => {
    let release: (r: Response) => void = () => {}
    mockFetch((url) => (url.startsWith('/api/namespaces/shop/schedule-suggestion') ? new Promise<Response>(r => { release = r }) : undefined))
    const onApply = vi.fn()
    render(<ScheduleSuggestion namespace="shop" timezone="UTC" onApply={onApply} />)
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'Suggest from usage' }))
    expect(screen.getByRole('button', { name: 'Reading usage…' })).toHaveProperty('disabled', true)
    release(json({ error: 'VictoriaMetrics is unreachable' }, 502))
    expect(await screen.findByText('Could not suggest a schedule: VictoriaMetrics is unreachable')).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Suggest from usage' })).toHaveProperty('disabled', false)
    expect(onApply).not.toHaveBeenCalled()
  })
})
