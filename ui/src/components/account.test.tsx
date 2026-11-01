import { StrictMode } from 'react'
import { describe, expect, it, vi } from 'vitest'
import { cleanup, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { json, mockFetch, on } from '../test/fetch'
import ApiTokens from './ApiTokens'
import ChangePasswordDialog from './ChangePasswordDialog'
import OverrideDialog from './OverrideDialog'
import AuthCallback from '../pages/AuthCallback'
import { toast } from '../lib/toast'

vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} })

const passwords = () => [...document.querySelectorAll('input[type="password"]')] as HTMLInputElement[]

describe('API tokens', () => {
  it('names every field so it can be found by its label', async () => {
    mockFetch(on('GET', '/api/tokens', () => json([])))
    render(<ApiTokens />)
    expect(await screen.findByLabelText('Name')).toBeTruthy()
    expect((screen.getByLabelText('Role') as HTMLSelectElement).value).toBe('viewer')
    expect((screen.getByLabelText('Expires (days)') as HTMLInputElement).value).toBe('90')
  })

  it('says when the token list could not be loaded instead of showing none', async () => {
    mockFetch(on('GET', '/api/tokens', () => json({ error: 'secrets "costdeck-api-tokens" is forbidden' }, 403)))
    render(<ApiTokens />)
    expect(await screen.findByText('Could not load the tokens: secrets "costdeck-api-tokens" is forbidden')).toBeTruthy()
  })

  it('does not claim a token was copied when the clipboard refuses', async () => {
    mockFetch(
      on('GET', '/api/tokens', () => json([])),
      on('POST', '/api/tokens', () => json({ token: 'cdk_secret123', info: { name: 'ci', role: 'viewer', createdAt: '2026-10-31T00:00:00Z' } }, 201)),
    )
    const failure = vi.spyOn(toast, 'error')
    render(<ApiTokens />)
    const user = userEvent.setup()
    await user.type(await screen.findByLabelText('Name'), 'ci')
    await user.click(screen.getByRole('button', { name: 'Create' }))
    expect(await screen.findByText('cdk_secret123')).toBeTruthy()
    vi.spyOn(navigator.clipboard, 'writeText').mockRejectedValue(new Error('denied'))
    await user.click(screen.getByRole('button', { name: 'Copy' }))
    await waitFor(() => expect(failure).toHaveBeenCalledWith('Could not copy the token; select it and copy it by hand.'))
    expect(screen.queryByRole('button', { name: 'Copied' })).toBeNull()
  })

  it('creates a token, shows it once and lists it', async () => {
    let list = [{ name: 'ci', role: 'viewer', createdAt: '2026-10-01T00:00:00Z' }]
    const fetch = mockFetch(
      on('GET', '/api/tokens', () => json(list)),
      on('POST', '/api/tokens', () => {
        list = [...list, { name: 'claude-desktop', role: 'operator', createdAt: '2026-10-31T00:00:00Z', expiresAt: '2026-11-30T00:00:00Z' } as typeof list[0]]
        return json({ token: 'cdk_secret123', info: list[1] }, 201)
      }),
    )
    const { unmount } = render(<ApiTokens />)
    const user = userEvent.setup()
    expect(await screen.findByText('ci')).toBeTruthy()
    expect(screen.getByText('never')).toBeTruthy()
    const create = screen.getByRole('button', { name: 'Create' })
    expect(create).toHaveProperty('disabled', true)
    await user.type(screen.getByPlaceholderText('claude-desktop'), '  ')
    expect(create).toHaveProperty('disabled', true)
    await user.type(screen.getByPlaceholderText('claude-desktop'), 'claude-desktop ')
    await user.selectOptions(screen.getByRole('combobox'), 'operator')
    await user.clear(screen.getByRole('spinbutton'))
    await user.type(screen.getByRole('spinbutton'), '30')
    await user.click(create)

    expect(await screen.findByText('cdk_secret123')).toBeTruthy()
    expect(screen.getByText(/it will not be shown again/)).toBeTruthy()
    const post = fetch.mock.calls.find(([, init]) => init?.method === 'POST')!
    expect(JSON.parse(post[1]!.body as string)).toEqual({ name: 'claude-desktop', role: 'operator', expiresInDays: 30 })
    expect((screen.getByPlaceholderText('claude-desktop') as HTMLInputElement).value).toBe('')
    expect(await screen.findByText('claude-desktop')).toBeTruthy()

    await user.click(screen.getByRole('button', { name: 'Copy' }))
    expect(await navigator.clipboard.readText()).toBe('cdk_secret123')
    expect(screen.getByRole('button', { name: 'Copied' })).toBeTruthy()

    unmount()
    render(<ApiTokens />)
    expect(await screen.findByText('claude-desktop')).toBeTruthy()
    expect(screen.queryByText('cdk_secret123')).toBeNull()
  })

  it('shows why a token was refused and shows no token', async () => {
    mockFetch(
      on('GET', '/api/tokens', () => json([])),
      on('POST', '/api/tokens', () => json({ error: 'expiresInDays must be between 0 (never) and 3650' }, 400)),
    )
    render(<ApiTokens />)
    const user = userEvent.setup()
    await user.type(screen.getByPlaceholderText('claude-desktop'), 'ci')
    await user.clear(screen.getByRole('spinbutton'))
    await user.type(screen.getByRole('spinbutton'), '5000')
    await user.click(screen.getByRole('button', { name: 'Create' }))
    expect(await screen.findByText('expiresInDays must be between 0 (never) and 3650')).toBeTruthy()
    expect(screen.queryByText(/will not be shown again/)).toBeNull()
    expect((screen.getByPlaceholderText('claude-desktop') as HTMLInputElement).value).toBe('ci')
  })
})

describe('Change password', () => {
  it('is a named dialog whose fields are found by their labels', () => {
    mockFetch()
    render(<ChangePasswordDialog required onDone={() => {}} onClose={() => {}} onSignOut={() => {}} />)
    const dialog = screen.getByRole('dialog', { name: 'Choose your own password' })
    expect(dialog.getAttribute('aria-modal')).toBe('true')
    expect(within(dialog).getByLabelText('Password you were given')).toBe(passwords()[0])
    expect(within(dialog).getByLabelText('New password')).toBe(passwords()[1])
    expect(within(dialog).getByLabelText('New password again')).toBe(passwords()[2])
  })

  it('says what is wrong with the new password and sends nothing until it is right', async () => {
    const fetch = mockFetch()
    render(<ChangePasswordDialog onDone={() => {}} onClose={() => {}} onSignOut={() => {}} />)
    const user = userEvent.setup()
    const [current, next, again] = passwords()
    const change = screen.getByRole('button', { name: 'Change password' })
    await user.type(current, 'old-password-1')
    await user.type(next, 'short')
    expect(screen.getByText('At least 12 characters')).toBeTruthy()
    await user.type(next, '-but-long')
    await user.type(again, 'short-but-lon')
    expect(screen.getByText('The two passwords differ')).toBeTruthy()
    expect(change).toHaveProperty('disabled', true)
    await user.clear(next)
    await user.clear(again)
    await user.type(next, 'old-password-1')
    await user.type(again, 'old-password-1')
    expect(screen.getByText('Choose a password different from the current one')).toBeTruthy()
    expect(change).toHaveProperty('disabled', true)
    await user.type(again, '{Enter}')
    expect(fetch).not.toHaveBeenCalled()
  })

  it('changes the password with Enter and reports a refusal', async () => {
    let refuse = true
    const fetch = mockFetch(on('POST', '/api/auth/password', () => (refuse ? json({ error: 'The current password is wrong' }, 403) : json({ status: 'ok' }))))
    const onDone = vi.fn()
    render(<ChangePasswordDialog onDone={onDone} onClose={() => {}} onSignOut={() => {}} />)
    expect(screen.getByRole('heading', { name: 'Change password' })).toBeTruthy()
    expect(screen.getByText('Your other sessions are signed out.')).toBeTruthy()
    const user = userEvent.setup()
    const [current, next, again] = passwords()
    await user.type(current, 'old-password-1')
    await user.type(next, 'new-password-22')
    await user.type(again, 'new-password-22')
    await user.click(screen.getByRole('button', { name: 'Change password' }))
    expect(await screen.findByText('The current password is wrong')).toBeTruthy()
    expect(onDone).not.toHaveBeenCalled()

    refuse = false
    await user.type(again, '{Enter}')
    await waitFor(() => expect(onDone).toHaveBeenCalledOnce())
    expect(fetch).toHaveBeenCalledTimes(2)
    expect(JSON.parse(fetch.mock.calls[1][1]!.body as string)).toEqual({ currentPassword: 'old-password-1', newPassword: 'new-password-22' })
  })

  it('cannot be dismissed when an administrator set the password, only left by signing out', async () => {
    const onClose = vi.fn()
    const onSignOut = vi.fn()
    mockFetch()
    render(<ChangePasswordDialog required onDone={() => {}} onClose={onClose} onSignOut={onSignOut} />)
    expect(screen.getByRole('heading', { name: 'Choose your own password' })).toBeTruthy()
    expect(screen.getByText('Password you were given')).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Cancel' })).toBeNull()
    const user = userEvent.setup()
    await user.keyboard('{Escape}')
    expect(onSignOut).toHaveBeenCalledTimes(1)
    await user.click(screen.getByRole('button', { name: 'Sign out' }))
    expect(onSignOut).toHaveBeenCalledTimes(2)
    expect(onClose).not.toHaveBeenCalled()
  })

  it('closes on Cancel when the user opened it', async () => {
    const onClose = vi.fn()
    const onSignOut = vi.fn()
    mockFetch()
    render(<ChangePasswordDialog onDone={() => {}} onClose={onClose} onSignOut={onSignOut} />)
    await userEvent.setup().click(screen.getByRole('button', { name: 'Cancel' }))
    expect(onClose).toHaveBeenCalledOnce()
    expect(onSignOut).not.toHaveBeenCalled()
  })
})

describe('Microsoft sign-in callback', () => {
  it('hands the code and state to the server once and goes where the sign-in started', async () => {
    window.history.replaceState(null, '', '/auth/callback?code=abc&state=xyz')
    const fetch = mockFetch(on('POST', '/api/auth/entra/callback-spa', () => json({ returnTo: '/scaling' })))
    const onSignedIn = vi.fn()
    render(<StrictMode><AuthCallback onSignedIn={onSignedIn} /></StrictMode>)
    expect(screen.getByText('Completing Microsoft sign-in…')).toBeTruthy()
    await waitFor(() => expect(onSignedIn).toHaveBeenCalledWith('/scaling'))
    expect(fetch).toHaveBeenCalledTimes(1)
    expect(JSON.parse(fetch.mock.calls[0][1]!.body as string)).toEqual({ code: 'abc', state: 'xyz', error: '', error_description: '' })
  })

  it('goes to the overview when the server names no page', async () => {
    window.history.replaceState(null, '', '/auth/callback?code=abc&state=xyz')
    mockFetch(on('POST', '/api/auth/entra/callback-spa', () => json({})))
    const onSignedIn = vi.fn()
    render(<AuthCallback onSignedIn={onSignedIn} />)
    await waitFor(() => expect(onSignedIn).toHaveBeenCalledWith('/'))
  })

  it("passes on Microsoft's error and shows why the sign-in failed", async () => {
    window.history.replaceState(null, '', '/auth/callback?error=access_denied&error_description=User+cancelled&state=xyz')
    const fetch = mockFetch(on('POST', '/api/auth/entra/callback-spa', () => json({ error: 'access_denied', detail: 'You cancelled the sign-in.' }, 400)))
    const onSignedIn = vi.fn()
    render(<AuthCallback onSignedIn={onSignedIn} />)
    expect(await screen.findByRole('heading', { name: 'Microsoft sign-in failed' })).toBeTruthy()
    expect(screen.getByText('You cancelled the sign-in.')).toBeTruthy()
    expect(screen.getByRole('link', { name: 'Back to sign-in' }).getAttribute('href')).toBe('/')
    expect(JSON.parse(fetch.mock.calls[0][1]!.body as string)).toEqual({ code: '', state: 'xyz', error: 'access_denied', error_description: 'User cancelled' })
    expect(onSignedIn).not.toHaveBeenCalled()
  })

  it('says the sign-in failed when the server answers without JSON', async () => {
    window.history.replaceState(null, '', '/auth/callback?code=abc&state=xyz')
    mockFetch(on('POST', '/api/auth/entra/callback-spa', () => new Response('<html>Bad gateway</html>', { status: 502 })))
    render(<AuthCallback onSignedIn={() => {}} />)
    expect(await screen.findByText('Sign-in failed')).toBeTruthy()
  })
})

describe('Override dialog', () => {
  it('holds until the next scheduled change by default, or for the chosen time', async () => {
    const onConfirm = vi.fn()
    render(<OverrideDialog name="team-a" kind="group" active={false} hasSchedule onCancel={() => {}} onConfirm={onConfirm} />)
    expect(screen.getByRole('heading', { name: 'Scale team-a down now' })).toBeTruthy()
    expect(screen.getByText('Schedule')).toBeTruthy()
    const radios = screen.getAllByRole('radio') as HTMLInputElement[]
    expect(radios).toHaveLength(6)
    expect(screen.getByRole('radio', { name: /Until the next scheduled change/ })).toHaveProperty('checked', true)
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'Scale down now' }))
    expect(onConfirm).toHaveBeenLastCalledWith('nextTransition')
    await user.click(screen.getByRole('radio', { name: /For 4 hours/ }))
    await user.click(screen.getByRole('button', { name: 'Scale down now' }))
    expect(onConfirm).toHaveBeenLastCalledWith('4h')
    await user.click(screen.getByRole('radio', { name: /Until I hand it back/ }))
    await user.click(screen.getByRole('button', { name: 'Scale down now' }))
    expect(onConfirm).toHaveBeenLastCalledWith('forever')
  })

  it('offers no "next scheduled change" without a schedule and starts with an hour', async () => {
    const onConfirm = vi.fn()
    const onCancel = vi.fn()
    render(<OverrideDialog name="shop-dev" kind="config" active hasSchedule={false} onCancel={onCancel} onConfirm={onConfirm} />)
    expect(screen.getByRole('heading', { name: 'Start shop-dev now' })).toBeTruthy()
    expect(screen.getByText('Namespace')).toBeTruthy()
    expect(screen.queryByRole('radio', { name: /next scheduled change/ })).toBeNull()
    expect(screen.getByRole('radio', { name: /For 1 hour/ })).toHaveProperty('checked', true)
    const user = userEvent.setup()
    await user.click(within(screen.getByRole('heading', { name: 'Start shop-dev now' }).closest('.rounded-2xl') as HTMLElement).getByRole('button', { name: 'Start now' }))
    expect(onConfirm).toHaveBeenCalledWith('1h')
    await user.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(onCancel).toHaveBeenCalledOnce()
  })
})

describe('Password changes in the app', () => {
  const session = (user: () => object, extra: Parameters<typeof mockFetch> = []) => mockFetch(
    on('GET', '/api/auth/me', () => json(user())),
    on('GET', '/api/version', () => json({ version: '9.9.9' })),
    ...extra,
  )
  const app = async () => {
    window.history.replaceState(null, '', '/docs')
    vi.resetModules()
    const { default: App } = await import('../App')
    render(<App />)
  }

  it('shows nothing else until a password an administrator chose is replaced', async () => {
    let must = true
    const fetch = session(() => ({ sub: 'user:anna', name: 'anna', role: 'viewer', provider: 'local', mustChangePassword: must }), [
      on('POST', '/api/auth/password', () => { must = false; return json({ status: 'ok' }) }),
    ])
    await app()
    expect(await screen.findByRole('heading', { name: 'Choose your own password' })).toBeTruthy()
    expect(screen.queryByRole('navigation', { name: 'Pages' })).toBeNull()
    const user = userEvent.setup()
    const [current, next, again] = passwords()
    await user.type(current, 'given-by-admin')
    await user.type(next, 'my-own-password')
    await user.type(again, 'my-own-password{Enter}')
    expect(await screen.findByRole('navigation', { name: 'Pages' })).toBeTruthy()
    expect(screen.queryByRole('heading', { name: 'Choose your own password' })).toBeNull()
    expect(fetch.mock.calls.filter(([url]) => url === '/api/auth/me').length).toBe(2)
  })

  it('offers a password change to managed users only', async () => {
    session(() => ({ sub: 'local:admin', name: 'admin', role: 'admin', provider: 'local' }))
    await app()
    expect(await screen.findByText('9.9.9')).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Change password' })).toBeNull()
    cleanup()

    session(() => ({ sub: 'user:anna', name: 'anna', role: 'viewer', provider: 'local' }), [on('POST', '/api/auth/password', () => json({ status: 'ok' }))])
    await app()
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: 'Change password' }))
    const [current, next, again] = passwords()
    await user.type(current, 'old-password-1')
    await user.type(next, 'new-password-22')
    await user.type(again, 'new-password-22')
    await user.click(within(screen.getByRole('heading', { name: 'Change password' }).closest('.rounded-2xl') as HTMLElement).getByRole('button', { name: 'Change password' }))
    await waitFor(() => expect(screen.queryByRole('heading', { name: 'Change password' })).toBeNull())
  })
})
