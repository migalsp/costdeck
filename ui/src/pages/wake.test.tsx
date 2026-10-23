import { describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { json, mockFetch, on } from '../test/fetch'
import WakePage from './WakePage'
import ScheduleWizard from '../components/ScheduleWizard'
import { safeReturn, type WakeStatus } from '../lib/wake'

const asleep: WakeStatus = { namespace: 'shop-dev', schedule: { kind: 'group', name: 'shop' }, desiredState: 'Down', phase: 'ScaledDown', mode: 'Schedule', sleeping: true, hostnames: ['shop-dev.example.com'] }

describe('Waking a sleeping namespace', () => {
  it('starts the schedule for a while and leads back to the application', async () => {
    window.history.replaceState(null, '', '/wake/shop-dev?return=' + encodeURIComponent('https://shop-dev.example.com/cart'))
    let state = asleep
    const fetch = mockFetch(
      on('GET', '/api/wake/shop-dev', () => json(state)),
      on('POST', '/api/scaling/groups/shop/manual', () => {
        state = { ...asleep, desiredState: 'Up', phase: 'ScaledUp', mode: 'ManualUp', sleeping: false, overrideExpiresAt: '2026-10-22T18:00:00Z' }
        return json({})
      }),
    )
    render(<WakePage namespace="shop-dev" />)
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: '4 hours' }))
    const post = fetch.mock.calls.find(([, init]) => init?.method === 'POST')!
    expect(JSON.parse(post[1]!.body as string)).toEqual({ active: true, until: '4h' })
    expect(await screen.findByText(/Starting shop-dev/)).toBeTruthy()
  })

  it('offers the way back only to the namespace’s own hosts', async () => {
    expect(safeReturn('https://shop-dev.example.com/cart', ['shop-dev.example.com'])).toBe('https://shop-dev.example.com/cart')
    expect(safeReturn('https://evil.example/phish', ['shop-dev.example.com'])).toBeNull()
    expect(safeReturn('javascript:alert(1)', ['shop-dev.example.com'])).toBeNull()
    expect(safeReturn(null, ['shop-dev.example.com'])).toBeNull()

    window.history.replaceState(null, '', '/wake/shop-dev?return=' + encodeURIComponent('https://evil.example/'))
    mockFetch(on('GET', '/api/wake/shop-dev', () => json({ ...asleep, phase: 'ScaledUp', desiredState: 'Up', sleeping: false })))
    render(<WakePage namespace="shop-dev" />)
    expect(await screen.findByText(/shop-dev is up/)).toBeTruthy()
    expect(screen.queryByRole('link', { name: /Open/ })).toBeNull()
  })

  it('links back to the application once it is up', async () => {
    window.history.replaceState(null, '', '/wake/shop-dev?return=' + encodeURIComponent('https://shop-dev.example.com/cart'))
    mockFetch(on('GET', '/api/wake/shop-dev', () => json({ ...asleep, phase: 'ScaledUp', desiredState: 'Up', sleeping: false })))
    render(<WakePage namespace="shop-dev" />)
    const link = await screen.findByRole('link', { name: /Open shop-dev.example.com/ })
    expect(link.getAttribute('href')).toBe('https://shop-dev.example.com/cart')
  })

  it('says when nothing schedules the namespace', async () => {
    mockFetch(on('GET', '/api/wake/lab', () => json({ namespace: 'lab', sleeping: false, hostnames: [] })))
    render(<WakePage namespace="lab" />)
    expect(await screen.findByText('No schedule scales lab, so there is nothing to wake.')).toBeTruthy()
  })

  it('saves a schedule that shows the sleep page', async () => {
    const fetch = mockFetch(
      on('GET', '/api/settings', () => json({ calendars: [] })),
      on('POST', '/api/scaling/groups', (_, init) => json(JSON.parse(init!.body as string), 201)),
    )
    const onSaved = vi.fn()
    render(<ScheduleWizard kind="group" namespaces={['shop-dev']} groups={[]} onClose={() => {}} onSaved={onSaved} />)
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'shop-dev' }))
    await user.click(screen.getByRole('checkbox', { name: /Show a sleep page while down/ }))
    await user.click(screen.getByRole('button', { name: 'Create schedule' }))
    await waitFor(() => expect(onSaved).toHaveBeenCalled())
    const body = JSON.parse(fetch.mock.calls.find(([url, init]) => url === '/api/scaling/groups' && init?.method === 'POST')![1]!.body as string)
    expect(body.spec.sleepPage).toBe(true)
  })
})
