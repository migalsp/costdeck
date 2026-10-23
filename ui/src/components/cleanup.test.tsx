import { describe, expect, it } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { json, mockFetch, on } from '../test/fetch'
import SettingsPage from '../pages/SettingsPage'
import { cleanupForm, cleanupProblem, days } from '../lib/cleanup'

const off = { enabled: false, dryRun: false, selector: '' }
const plan = (over = {}) => ({
  ...off, permitted: true, deleted: [{ namespace: 'pr-7', createdAt: '2026-10-01T00:00:00Z', expiresAt: '2026-10-08T00:00:00Z', deletedAt: '2026-10-08T00:01:00Z' }],
  namespaces: [
    { namespace: 'pr-12', createdAt: '2026-10-20T00:00:00Z', expiresAt: '2026-10-27T00:00:00Z', source: 'ttl', warned: false },
    { namespace: 'pr-keep', createdAt: '2026-09-01T00:00:00Z', expiresAt: '2026-09-08T00:00:00Z', source: 'ttl', warned: false, protected: 'costdeck.io/ttl=off keeps it' },
  ],
  ...over,
})

describe('Preview cleanup', () => {
  it('starts off, in a dry run, and reads durations as days', () => {
    expect(cleanupForm(off)).toMatchObject({ enabled: false, dryRun: true, ttl: '7d', warnBefore: '24h' })
    expect(cleanupForm({ enabled: true, dryRun: false, selector: 'env=preview', ttl: '72h0m0s', warnBefore: '0s' })).toMatchObject({ dryRun: false, ttl: '3d', warnBefore: '0s' })
    expect(days('36h0m0s')).toBe('36h')
  })

  it('asks for a confirmation only when deleting is turned on', () => {
    const f = { ...cleanupForm(off), enabled: true, selector: 'env=preview' }
    expect(cleanupProblem({ ...f, selector: ' ' })).toMatch(/enter a label selector/)
    expect(cleanupProblem(f)).toBeNull() // still a dry run
    expect(cleanupProblem({ ...f, dryRun: false })).toMatch(/cannot be restored/)
    expect(cleanupProblem({ ...f, dryRun: false, understood: true })).toBeNull()
    expect(cleanupProblem({ ...f, dryRun: false }, { enabled: true, dryRun: false, selector: 'env=preview' })).toBeNull()
  })

  it('warns, insists on the confirmation, then saves', async () => {
    window.history.replaceState(null, '', '/settings/cleanup')
    const settings = { providers: {}, integrations: {}, auth: { disableLocalLogin: false }, calendars: [], namespaceTTL: off }
    const fetch = mockFetch(
      on('GET', '/api/settings', () => json(settings)),
      on('PUT', '/api/settings', () => json(settings)),
      on('GET', '/api/namespace-ttl', () => json(plan())),
    )
    render(<SettingsPage section="cleanup" />)
    const user = userEvent.setup()
    await user.click(await screen.findByRole('switch', { name: 'Enable Delete preview namespaces' }))
    expect(screen.getByRole('alert').textContent).toMatch(/Dry run: nothing is deleted/)
    await user.type(screen.getByLabelText('Namespaces with the label'), 'env=preview')
    await user.click(screen.getByRole('checkbox', { name: /Dry run/ }))
    expect(screen.getByRole('alert').textContent).toMatch(/Namespaces will be deleted.*cannot be restored.*kube-\*/s)

    await user.click(screen.getByRole('button', { name: 'Save' }))
    expect(await screen.findByText('Not saved: Preview cleanup: tick that deleted namespaces cannot be restored')).toBeTruthy()
    expect(fetch.mock.calls.some(([, init]) => init?.method === 'PUT')).toBe(false)

    await user.click(screen.getByRole('checkbox', { name: /I understand/ }))
    await user.click(screen.getByRole('button', { name: 'Save' }))
    await screen.findByText('Saved')
    const put = JSON.parse(fetch.mock.calls.find(([, init]) => init?.method === 'PUT')![1]!.body as string)
    expect(put).toEqual({ namespaceTTL: { enabled: true, dryRun: false, selector: 'env=preview', ttl: '7d', warnBefore: '24h' } })
  })

  it('lists what the unsaved values would delete and keeps a namespace longer', async () => {
    window.history.replaceState(null, '', '/settings/cleanup')
    const saved = { enabled: true, dryRun: true, selector: 'env=preview', ttl: '168h0m0s', warnBefore: '24h0m0s' }
    const settings = { providers: {}, integrations: {}, auth: { disableLocalLogin: false }, calendars: [], namespaceTTL: saved }
    const fetch = mockFetch(
      on('GET', '/api/settings', () => json(settings)),
      on('GET', '/api/namespace-ttl', () => json(plan(saved))),
      on('POST', '/api/namespace-ttl/pr-12/extend', () => json({ namespace: 'pr-12', createdAt: '2026-10-20T00:00:00Z', expiresAt: '2026-10-30T00:00:00Z', source: 'extended', warned: false })),
    )
    render(<SettingsPage section="cleanup" />)
    const user = userEvent.setup()
    expect(await screen.findByText('pr-12')).toBeTruthy()
    expect(screen.getByText(/pr-keep/).parentElement!.textContent).toMatch(/costdeck.io\/ttl=off keeps it/)
    expect(screen.getByText(/pr-7/)).toBeTruthy()
    await user.selectOptions(screen.getByLabelText('Delete after'), '3d')
    await waitFor(() => expect(fetch.mock.calls.some(([url]) => String(url).includes('selector=env%3Dpreview&ttl=3d'))).toBe(true))

    await user.click(screen.getByRole('button', { name: 'Keep 3 more days' }))
    expect(await screen.findByText('extended')).toBeTruthy()
    const post = fetch.mock.calls.find(([, init]) => init?.method === 'POST')!
    expect(JSON.parse(post[1]!.body as string)).toEqual({ by: '3d' })
  })

  it('says when the chart does not let the operator delete namespaces', async () => {
    window.history.replaceState(null, '', '/settings/cleanup')
    const settings = { providers: {}, integrations: {}, auth: { disableLocalLogin: false }, calendars: [], namespaceTTL: off }
    mockFetch(on('GET', '/api/settings', () => json(settings)), on('GET', '/api/namespace-ttl', () => json(plan({ permitted: false }))))
    render(<SettingsPage section="cleanup" />)
    await userEvent.setup().click(await screen.findByRole('switch', { name: 'Enable Delete preview namespaces' }))
    expect((await screen.findByRole('note')).textContent).toMatch(/namespaceTTL.enabled=true/)
    expect((screen.getByRole('button', { name: 'Keep 3 more days' }) as HTMLButtonElement).disabled).toBe(true)
  })
})
