import { describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { json, mockFetch, on } from '../test/fetch'
import ExceptionsEditor from './ExceptionsEditor'
import CalendarsEditor from './settings/CalendarsEditor'
import ScheduleWizard from './ScheduleWizard'
import SettingsPage from '../pages/SettingsPage'
import type { HolidayCalendar, ScalingConfig, ScheduleException } from '../lib/types'

const today = new Date().toISOString().slice(0, 10)
const holidays: HolidayCalendar = { name: 'de-holidays', days: [{ date: '2999-12-25', name: 'Christmas' }] }

// Harness keeps the editor's value in state, like the wizard does.
function Exceptions({ initial = [], calendars = [], available = [holidays] }: { initial?: ScheduleException[]; calendars?: string[]; available?: HolidayCalendar[] }) {
  const [ex, setEx] = useState(initial)
  const [cals, setCals] = useState(calendars)
  return (
    <>
      <ExceptionsEditor exceptions={ex} calendars={cals} available={available} timezone="Europe/Berlin" onChange={(e, c) => { setEx(e); setCals(c) }} />
      <output data-testid="value">{JSON.stringify({ ex, cals })}</output>
    </>
  )
}
const value = () => JSON.parse(screen.getByTestId('value').textContent!)

describe('ExceptionsEditor', () => {
  it('adds days to keep down or up, starting today', async () => {
    render(<Exceptions />)
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'Keep down' }))
    await user.click(screen.getByRole('button', { name: 'Keep up' }))
    expect(value().ex).toEqual([{ name: '', start: today, state: 'Down' }, { name: '', start: today, state: 'Up' }])
    expect(screen.getByText(/Times are in Europe\/Berlin/)).toBeTruthy()
  })
  it('edits a range with times and explains a range that ends before it starts', async () => {
    render(<Exceptions initial={[{ name: 'Release', start: '2999-11-07', state: 'Up' }]} />)
    const user = userEvent.setup()
    const row = screen.getByTestId('exception')
    await user.type(within(row).getByLabelText('Start time'), '0600')
    await user.type(within(row).getByLabelText('Last day'), '2999-11-06')
    expect(within(row).getByText('It ends before it starts.')).toBeTruthy()
    await user.clear(within(row).getByLabelText('Last day'))
    await user.type(within(row).getByLabelText('Last day'), '2999-11-08')
    await user.type(within(row).getByLabelText('End time'), '2200')
    expect(value().ex[0]).toEqual({ name: 'Release', start: '2999-11-07T06:00', end: '2999-11-08T22:00', state: 'Up' })
    expect(within(row).queryByText('It ends before it starts.')).toBeNull()
  })
  it('picks calendars and flags one that no longer exists', async () => {
    render(<Exceptions calendars={['gone']} />)
    const user = userEvent.setup()
    expect(screen.getByText('gone does not exist')).toBeTruthy()
    await user.click(screen.getByRole('button', { name: /de-holidays/ }))
    expect(value().cals).toEqual(['gone', 'de-holidays'])
    expect(screen.getByRole('button', { name: /de-holidays/ }).getAttribute('aria-pressed')).toBe('true')
  })
  it('removes dates that are over in one go', async () => {
    render(<Exceptions initial={[{ start: '2001-01-01', state: 'Down' }, { start: '2999-01-01', state: 'Down' }]} />)
    await userEvent.setup().click(screen.getByRole('button', { name: 'Remove 1 past date' }))
    expect(value().ex).toEqual([{ start: '2999-01-01', state: 'Down' }])
  })
})

describe('ScheduleWizard exceptions', () => {
  const config: ScalingConfig = {
    metadata: { name: 'shop-dev' },
    spec: { targetNamespace: 'shop-dev', schedules: [{ days: [1, 2, 3, 4, 5], startTime: '08:00', endTime: '20:00', timezone: 'UTC' }] },
    status: { phase: 'ScaledUp', lastAction: '' },
  }
  it('saves the exceptions and calendars with the schedule', async () => {
    const fetch = mockFetch(
      on('GET', '/api/settings', () => json({ calendars: [holidays] })),
      on('PUT', '/api/scaling/configs/shop-dev', () => json(config)),
    )
    const onSaved = vi.fn()
    render(<ScheduleWizard kind="config" existing={config} namespaces={['shop-dev']} groups={[]} onClose={() => {}} onSaved={onSaved} />)
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: /Exceptions/ }))
    await user.click(await screen.findByRole('button', { name: /de-holidays/ }))
    await user.click(screen.getByRole('button', { name: 'Keep down' }))
    await user.type(screen.getByLabelText('Name'), 'Offsite')
    await user.click(screen.getByRole('button', { name: 'Save changes' }))
    await waitFor(() => expect(onSaved).toHaveBeenCalled())
    const put = fetch.mock.calls.find(([, init]) => init?.method === 'PUT')!
    const body = JSON.parse(put[1]!.body as string)
    expect(body.spec.calendars).toEqual(['de-holidays'])
    expect(body.spec.exceptions).toEqual([{ name: 'Offsite', start: today, state: 'Down' }])
    expect(body.spec.schedules).toEqual(config.spec.schedules)
  })
  it('will not save an exception the operator would reject', async () => {
    mockFetch(on('GET', '/api/settings', () => json({ calendars: [] })))
    render(<ScheduleWizard kind="config" existing={{ ...config, spec: { ...config.spec, exceptions: [{ start: '2999-12-25', end: '2999-12-01', state: 'Down' }] } }}
      namespaces={['shop-dev']} groups={[]} onClose={() => {}} onSaved={() => {}} />)
    expect(await screen.findByText('Exceptions: It ends before it starts.')).toBeTruthy()
    expect((screen.getByRole('button', { name: 'Save changes' }) as HTMLButtonElement).disabled).toBe(true)
  })
})

function Calendars({ initial = [] as HolidayCalendar[] }) {
  const [v, setV] = useState(initial)
  return (<><CalendarsEditor value={v} onChange={setV} /><output data-testid="value">{JSON.stringify(v)}</output></>)
}

describe('CalendarsEditor', () => {
  it('creates a calendar and adds days by hand', async () => {
    render(<Calendars />)
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'New calendar' }))
    await user.click(screen.getByRole('button', { name: 'Add a day' }))
    await user.type(screen.getByLabelText('Date'), '2999-01-01')
    await user.type(screen.getByLabelText('Day name'), 'New Year')
    await user.selectOptions(screen.getByLabelText('Keep'), 'Up')
    expect(JSON.parse(screen.getByTestId('value').textContent!)).toEqual([{ name: 'holidays', state: 'Up', days: [{ date: '2999-01-01', name: 'New Year' }] }])
  })
  it('imports the days of an .ics file it does not have yet', async () => {
    render(<Calendars initial={[{ name: 'de', days: [{ date: '2026-12-25', name: 'Christmas' }] }]} />)
    const user = userEvent.setup()
    const ics = 'BEGIN:VEVENT\nDTSTART;VALUE=DATE:20261225\nSUMMARY:Christmas\nEND:VEVENT\nBEGIN:VEVENT\nDTSTART;VALUE=DATE:20261226\nSUMMARY:Boxing Day\nEND:VEVENT'
    await user.click(screen.getByRole('button', { name: 'Import .ics' }))
    await user.upload(screen.getByLabelText('iCalendar file'), new File([ics], 'de.ics', { type: 'text/calendar' }))
    expect(await screen.findByText('Added 1 day to de.')).toBeTruthy()
    expect(JSON.parse(screen.getByTestId('value').textContent!)[0].days).toEqual([{ date: '2026-12-25', name: 'Christmas' }, { date: '2026-12-26', name: 'Boxing Day' }])
  })
  it('says what the server would refuse', () => {
    render(<Calendars initial={[{ name: 'Bad Name', days: [] }]} />)
    expect(screen.getByText('Calendar name "Bad Name" must be lowercase letters, digits and dashes.')).toBeTruthy()
  })
})

describe('Settings: holiday calendars', () => {
  const settings = { providers: {}, integrations: {}, auth: { disableLocalLogin: false }, calendars: [holidays] }
  it('saves the calendars section and marks it saved', async () => {
    window.history.replaceState(null, '', '/settings/calendars')
    const fetch = mockFetch(
      on('GET', '/api/settings', () => json(settings)),
      on('PUT', '/api/settings', (_, init) => json({ ...settings, calendars: JSON.parse(init!.body as string).calendars })),
    )
    render(<SettingsPage section="calendars" />)
    const user = userEvent.setup()
    expect(await screen.findByDisplayValue('de-holidays')).toBeTruthy()
    await user.click(screen.getByRole('button', { name: 'Add a day' }))
    expect(screen.getByText('Unsaved changes')).toBeTruthy()
    const dates = screen.getAllByLabelText('Date')
    await user.type(dates[dates.length - 1], '2999-12-26')
    await user.click(screen.getByRole('button', { name: 'Save' }))
    expect(await screen.findByText('Saved')).toBeTruthy()
    const put = fetch.mock.calls.find(([, init]) => init?.method === 'PUT')!
    expect(JSON.parse(put[1]!.body as string)).toEqual({ calendars: [{ ...holidays, days: [...holidays.days, { date: '2999-12-26' }] }] })
  })
  it('does not send calendars the server would refuse', async () => {
    const fetch = mockFetch(on('GET', '/api/settings', () => json({ ...settings, calendars: [] })))
    render(<SettingsPage section="calendars" />)
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: 'New calendar' }))
    await user.click(screen.getByRole('button', { name: 'Add a day' }))
    await user.click(screen.getByRole('button', { name: 'Save' }))
    expect(await screen.findByText(/Not saved: holidays: "an empty date" is not a date\./)).toBeTruthy()
    expect(fetch.mock.calls.some(([, init]) => init?.method === 'PUT')).toBe(false)
  })
})
