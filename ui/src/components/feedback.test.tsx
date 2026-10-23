import { beforeEach, describe, expect, it, vi } from 'vitest'
import { act, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { json, mockFetch, on } from '../test/fetch'

beforeEach(() => {
  window.history.replaceState(null, '', '/settings/notifications')
  vi.resetModules()
})

describe('Toaster', () => {
  it('shows confirmations as status and errors as alerts, and closes them', async () => {
    const { toast } = await import('../lib/toast')
    const { default: Toaster } = await import('./Toaster')
    render(<Toaster />)
    act(() => { toast.success('Saved the budget team-a'); toast.error('Could not delete x: forbidden') })
    expect(screen.getByRole('status').textContent).toContain('Saved the budget team-a')
    expect(screen.getByRole('alert').textContent).toContain('Could not delete x: forbidden')
    await userEvent.setup().click(screen.getAllByRole('button', { name: 'Dismiss' })[1])
    expect(screen.queryByRole('alert')).toBeNull()
  })
})

describe('LeaveDialog', () => {
  it('asks before leaving unsaved input, defaults to staying, and leaves when told', async () => {
    const router = await import('../lib/router')
    const { default: LeaveDialog } = await import('./LeaveDialog')
    const stays = (p: string) => p.startsWith('/settings')
    function Page() {
      router.useLeaveGuard('Notifications and AI assistant', stays)
      return <LeaveDialog />
    }
    render(<Page />)
    const user = userEvent.setup()
    act(() => router.navigate('/'))
    expect(screen.getByText('The changes in Notifications and AI assistant are not saved yet and will be lost.')).toBeTruthy()
    expect(document.activeElement?.textContent).toBe('Stay')
    await user.keyboard('{Escape}')
    expect(screen.queryByText(/not saved yet/)).toBeNull()
    expect(window.location.pathname).toBe('/settings/notifications')
    act(() => router.navigate('/reports'))
    await user.click(screen.getByRole('button', { name: 'Leave without saving' }))
    expect(window.location.pathname).toBe('/reports')
  })
})

describe('ApiTokens', () => {
  const tokens = [{ name: 'ci', role: 'viewer', createdAt: '2026-10-01T00:00:00Z' }]
  it('asks before revoking a token and confirms it in a toast', async () => {
    const fetch = mockFetch(
      on('GET', '/api/tokens', () => json(tokens)),
      on('DELETE', '/api/tokens/ci', () => new Response(null, { status: 204 })),
    )
    const { useToasts } = await import('../lib/toast')
    const { default: ApiTokens } = await import('./ApiTokens')
    let shown: string[] = []
    function Probe() { shown = useToasts().map(t => t.text); return null }
    render(<><ApiTokens /><Probe /></>)
    const user = userEvent.setup()
    await user.click(await screen.findByTitle('Revoke'))
    await user.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(fetch.mock.calls.some(([, init]) => init?.method === 'DELETE')).toBe(false)
    await user.click(screen.getByTitle('Revoke'))
    await user.click(screen.getByRole('button', { name: 'Revoke' }))
    await waitFor(() => expect(shown).toEqual(['Revoked the token ci']))
    expect(fetch.mock.calls.some(([url, init]) => url === '/api/tokens/ci' && init?.method === 'DELETE')).toBe(true)
  })
  it('reports a failed revoke as an error', async () => {
    mockFetch(
      on('GET', '/api/tokens', () => json(tokens)),
      on('DELETE', '/api/tokens/ci', () => json({ error: 'forbidden' }, 403)),
    )
    const { useToasts } = await import('../lib/toast')
    const { default: ApiTokens } = await import('./ApiTokens')
    let shown: { tone: string; text: string }[] = []
    function Probe() { shown = useToasts(); return null }
    render(<><ApiTokens /><Probe /></>)
    const user = userEvent.setup()
    await user.click(await screen.findByTitle('Revoke'))
    await user.click(screen.getByRole('button', { name: 'Revoke' }))
    await waitFor(() => expect(shown).toEqual([expect.objectContaining({ tone: 'error', text: 'Could not revoke ci: forbidden' })]))
  })
})
