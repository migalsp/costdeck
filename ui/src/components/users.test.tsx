import { describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { json, mockFetch, on } from '../test/fetch'
import UsersSettings from './settings/UsersSettings'
import { AuthContext, type User } from '../lib/auth'
import { toast } from '../lib/toast'
import Toaster from './Toaster'

const me: User = { sub: 'user:ops', name: 'ops', role: 'admin', provider: 'local' }
const created = '2026-10-01T00:00:00Z'
const minutesAgo = (n: number) => new Date(Date.now() - n * 60000).toISOString()

const listing = (over: Record<string, unknown> = {}) => ({
  builtin: { username: 'admin', secret: 'costdeck/costdeck-operator-admin-credentials' },
  users: [
    { username: 'ops', name: 'Olga Ops', role: 'admin', createdAt: created, passwordChangedAt: created },
    { username: 'bob', name: 'Bob', email: 'bob@example.com', role: 'viewer', createdAt: created, passwordChangedAt: created },
    { username: 'carol', name: 'Carol', role: 'operator', disabled: true, createdAt: created, passwordChangedAt: created },
    { username: 'dan', name: 'Dan', role: 'viewer', mustChangePassword: true, createdAt: created, passwordChangedAt: created },
  ],
  signIns: [
    { subject: 'user:bob', name: 'Bob', provider: 'local', role: 'viewer', at: minutesAgo(5), count: 3 },
    { subject: 'entra:erin', name: 'Erin Entra', email: 'erin@corp.example', provider: 'entra', role: 'operator', at: minutesAgo(30), count: 7 },
  ],
  sso: true,
  minPasswordLength: 12,
  ...over,
})

const show = (onOpenSSO = () => {}) =>
  render(<AuthContext.Provider value={me}><UsersSettings onOpenSSO={onOpenSSO} /><Toaster /></AuthContext.Provider>)

const row = (target: string | HTMLElement) => (typeof target === 'string' ? screen.getByText(target) : target).closest('tr') as HTMLElement
const dialog = (title: string) => screen.getByRole('heading', { name: title }).closest('.rounded-2xl') as HTMLElement
const bodyOf = (fetch: ReturnType<typeof mockFetch>, method: string, url: string) => {
  const call = fetch.mock.calls.find(([u, init]) => u === url && init?.method === method)
  return call ? JSON.parse(call[1]!.body as string) : undefined
}
const calls = (fetch: ReturnType<typeof mockFetch>, method: string, url: string) =>
  fetch.mock.calls.filter(([u, init]) => u === url && (init?.method || 'GET') === method).length

describe('Users settings', () => {
  it('lists the built-in admin, local users with their state, and single sign-on users', async () => {
    mockFetch(on('GET', '/api/users', () => json(listing())))
    const onOpenSSO = vi.fn()
    show(onOpenSSO)
    const builtin = row(await screen.findByText('admin', { selector: 'div' }))
    expect(within(builtin).getByText('Managed by Helm')).toBeTruthy()
    expect(within(builtin).getByText('costdeck/costdeck-operator-admin-credentials')).toBeTruthy()
    expect(screen.getByText(/at least 12 characters/)).toBeTruthy()

    const mine = row('Olga Ops')
    expect(within(mine).getByText('(you)')).toBeTruthy()
    expect(within(mine).getByRole('button', { name: 'Edit' })).toBeTruthy()
    expect(within(mine).queryByRole('button', { name: /Disable|Delete/ })).toBeNull()

    expect(within(row('Bob')).getByText('5 min ago')).toBeTruthy()
    expect(within(row('Bob')).getByRole('button', { name: /^Disable/ })).toBeTruthy()
    expect(within(row('Carol')).getByText('Disabled')).toBeTruthy()
    expect(within(row('Carol')).getByRole('button', { name: 'Enable' })).toBeTruthy()
    expect(within(row('Dan')).getByText('Sets password at sign-in')).toBeTruthy()
    expect(within(row('Dan')).getByText('Never')).toBeTruthy()

    const erin = row('Erin Entra')
    expect(within(erin).getByText('erin@corp.example')).toBeTruthy()
    expect(within(erin).getByText('operator')).toBeTruthy()
    expect(within(erin).getByText('7')).toBeTruthy()
    expect(screen.queryAllByText('Bob')).toHaveLength(1)

    await userEvent.setup().click(screen.getByRole('button', { name: 'Edit group mappings' }))
    expect(onOpenSSO).toHaveBeenCalledOnce()
  })

  it('says when there are no local users and single sign-on is off', async () => {
    mockFetch(on('GET', '/api/users', () => json(listing({ builtin: null, users: [], signIns: [], sso: false }))))
    show()
    expect(await screen.findByText(/No local users yet/)).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Connect Microsoft Entra ID' })).toBeTruthy()
    expect(screen.queryByText('Nobody has signed in with Microsoft yet.')).toBeNull()
  })

  it('says why the users could not be loaded', async () => {
    mockFetch(on('GET', '/api/users', () => json({ error: 'This action needs the admin role; you are signed in as operator.' }, 403)))
    show()
    expect(await screen.findByText('Could not load users: This action needs the admin role; you are signed in as operator.')).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Add user' })).toBeNull()
  })

  it('adds a user with a generated password and shows it once to hand over', async () => {
    const fetch = mockFetch(
      on('GET', '/api/users', () => json(listing())),
      on('POST', '/api/users', () => json({ user: { username: 'anna', role: 'operator', createdAt: created, passwordChangedAt: created }, password: 'Gen-Pass-0123456' }, 201)),
    )
    const success = vi.spyOn(toast, 'success')
    show()
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: 'Add user' }))
    const form = dialog('Add a user')
    expect(screen.getByRole('dialog', { name: 'Add a user' })).toBeTruthy()
    expect(within(form).getByLabelText('Username')).toBe(within(form).getByPlaceholderText('anna'))
    expect(within(form).getByLabelText(/Display name/)).toBe(within(form).getByPlaceholderText('Anna Petrova'))
    expect(within(form).getByLabelText(/Email/)).toBe(within(form).getByPlaceholderText('anna@example.com'))
    const add = within(form).getByRole('button', { name: 'Add user' })
    expect(add).toHaveProperty('disabled', true)
    await user.type(within(form).getByPlaceholderText('anna'), '-anna')
    expect(add).toHaveProperty('disabled', true)
    await user.clear(within(form).getByPlaceholderText('anna'))
    await user.type(within(form).getByPlaceholderText('anna'), 'Anna')
    await user.type(within(form).getByPlaceholderText('Anna Petrova'), 'Anna Petrova')
    await user.type(within(form).getByPlaceholderText('anna@example.com'), 'anna@example.com')
    await user.click(within(form).getByRole('radio', { name: /^operator/ }))
    expect(add).toHaveProperty('disabled', false)
    await user.click(add)

    const done = await screen.findByRole('heading', { name: 'anna was added' })
    expect(bodyOf(fetch, 'POST', '/api/users')).toEqual({ username: 'anna', name: 'Anna Petrova', email: 'anna@example.com', role: 'operator', mustChangePassword: true })
    expect(success).toHaveBeenCalledWith('Created the user anna')
    const card = done.closest('.rounded-2xl') as HTMLElement
    expect(within(card).getByText('Gen-Pass-0123456')).toBeTruthy()
    await user.click(within(card).getByRole('button', { name: 'Copy' }))
    expect(await navigator.clipboard.readText()).toBe('anna / Gen-Pass-0123456')
    expect(within(card).getByRole('button', { name: 'Copied' })).toBeTruthy()
    await user.click(within(card).getByRole('button', { name: 'Done' }))
    expect(screen.queryByText('Gen-Pass-0123456')).toBeNull()
    expect(calls(fetch, 'GET', '/api/users')).toBeGreaterThanOrEqual(2)
  })

  it('adds a user with a typed password only once it is long enough', async () => {
    const fetch = mockFetch(
      on('GET', '/api/users', () => json(listing())),
      on('POST', '/api/users', () => json({ user: { username: 'eve', role: 'viewer', createdAt: created, passwordChangedAt: created } }, 201)),
    )
    show()
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: 'Add user' }))
    const form = dialog('Add a user')
    await user.type(within(form).getByPlaceholderText('anna'), 'eve')
    await user.click(within(form).getByRole('button', { name: 'Type one' }))
    const add = within(form).getByRole('button', { name: 'Add user' })
    await user.type(within(form).getByPlaceholderText('At least 12 characters'), 'short-pass1')
    expect(add).toHaveProperty('disabled', true)
    await user.type(within(form).getByPlaceholderText('At least 12 characters'), '2')
    await user.click(within(form).getByRole('checkbox', { name: /choose their own password/ }))
    await user.click(add)
    await waitFor(() => expect(screen.queryByRole('heading', { name: 'Add a user' })).toBeNull())
    expect(bodyOf(fetch, 'POST', '/api/users')).toEqual({ username: 'eve', name: '', email: '', role: 'viewer', mustChangePassword: false, password: 'short-pass12' })
    expect(screen.queryByRole('heading', { name: /was added/ })).toBeNull()
  })

  it('keeps the form open with the reason when the server refuses a new user', async () => {
    mockFetch(
      on('GET', '/api/users', () => json(listing())),
      on('POST', '/api/users', () => json({ error: 'a user named bob already exists' }, 400)),
    )
    const success = vi.spyOn(toast, 'success')
    show()
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: 'Add user' }))
    const form = dialog('Add a user')
    await user.type(within(form).getByPlaceholderText('anna'), 'bob')
    await user.click(within(form).getByRole('button', { name: 'Add user' }))
    expect(await within(form).findByText('a user named bob already exists')).toBeTruthy()
    expect(screen.getByRole('heading', { name: 'Add a user' })).toBeTruthy()
    expect(success).not.toHaveBeenCalled()
  })

  it("changes another user's role", async () => {
    const fetch = mockFetch(
      on('GET', '/api/users', () => json(listing())),
      on('PATCH', '/api/users/bob', () => json({ username: 'bob', role: 'operator' })),
    )
    const success = vi.spyOn(toast, 'success')
    show()
    const user = userEvent.setup()
    await user.click(within(row(await screen.findByText('Bob'))).getByRole('button', { name: 'Edit' }))
    const form = dialog('Edit Bob')
    expect(within(form).getByPlaceholderText('anna')).toHaveProperty('disabled', true)
    expect(within(form).queryByPlaceholderText('At least 12 characters')).toBeNull()
    await user.click(within(form).getByRole('radio', { name: /^operator/ }))
    await user.click(within(form).getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(success).toHaveBeenCalledWith('Saved bob'))
    expect(bodyOf(fetch, 'PATCH', '/api/users/bob')).toEqual({ name: 'Bob', email: 'bob@example.com', role: 'operator' })
    expect(screen.queryByRole('heading', { name: 'Edit Bob' })).toBeNull()
  })

  it('does not let an administrator change their own role', async () => {
    const fetch = mockFetch(
      on('GET', '/api/users', () => json(listing())),
      on('PATCH', '/api/users/ops', () => json({ username: 'ops', role: 'admin' })),
    )
    show()
    const user = userEvent.setup()
    await user.click(within(row(await screen.findByText('Olga Ops'))).getByRole('button', { name: 'Edit' }))
    const form = dialog('Edit Olga Ops')
    expect(within(form).getByText('You cannot change your own role.')).toBeTruthy()
    expect(within(form).getAllByRole('radio').every(r => (r as HTMLInputElement).disabled)).toBe(true)
    const name = within(form).getByPlaceholderText('Anna Petrova')
    await user.clear(name)
    await user.type(name, 'Olga')
    await user.click(within(form).getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(bodyOf(fetch, 'PATCH', '/api/users/ops')).toEqual({ name: 'Olga', email: '' }))
  })

  it('disables and enables users, and shows a refused change', async () => {
    let refuse = false
    const fetch = mockFetch(
      on('GET', '/api/users', () => json(listing())),
      on('PATCH', '/api/users/bob', () => (refuse ? json({ error: 'You cannot disable your own account or take away your own admin role' }, 400) : json({}))),
      on('PATCH', '/api/users/carol', () => json({})),
    )
    const success = vi.spyOn(toast, 'success')
    show()
    const user = userEvent.setup()
    await user.click(within(row(await screen.findByText('Bob'))).getByRole('button', { name: /^Disable/ }))
    await waitFor(() => expect(success).toHaveBeenCalledWith('Disabled bob'))
    expect(bodyOf(fetch, 'PATCH', '/api/users/bob')).toEqual({ disabled: true })
    await user.click(within(row('Carol')).getByRole('button', { name: 'Enable' }))
    await waitFor(() => expect(success).toHaveBeenCalledWith('Enabled carol'))
    expect(bodyOf(fetch, 'PATCH', '/api/users/carol')).toEqual({ disabled: false })

    refuse = true
    const failure = vi.spyOn(toast, 'error')
    await user.click(within(row('Bob')).getByRole('button', { name: /^Disable/ }))
    expect(await screen.findByText('You cannot disable your own account or take away your own admin role')).toBeTruthy()
    expect(failure).toHaveBeenCalledWith('You cannot disable your own account or take away your own admin role')
    expect(success).toHaveBeenCalledTimes(2)
  })

  it('resets a password to a generated one and shows it', async () => {
    const fetch = mockFetch(
      on('GET', '/api/users', () => json(listing())),
      on('POST', '/api/users/bob/password', () => json({ status: 'ok', password: 'Fresh-Pass-987654' })),
    )
    show()
    const user = userEvent.setup()
    await user.click(within(row(await screen.findByText('Bob'))).getByRole('button', { name: 'Reset password' }))
    const form = dialog('Reset the password of Bob')
    await user.click(within(form).getByRole('button', { name: 'Reset password' }))
    const done = await screen.findByRole('heading', { name: 'New password for bob' })
    expect(within(done.closest('.rounded-2xl') as HTMLElement).getByText('Fresh-Pass-987654')).toBeTruthy()
    expect(bodyOf(fetch, 'POST', '/api/users/bob/password')).toEqual({})
  })

  it('resets a password to a typed one only once it is long enough', async () => {
    const fetch = mockFetch(
      on('GET', '/api/users', () => json(listing())),
      on('POST', '/api/users/bob/password', () => json({ status: 'ok' })),
    )
    show()
    const user = userEvent.setup()
    await user.click(within(row(await screen.findByText('Bob'))).getByRole('button', { name: 'Reset password' }))
    const form = dialog('Reset the password of Bob')
    await user.click(within(form).getByRole('button', { name: 'Type one' }))
    const reset = within(form).getByRole('button', { name: 'Reset password' })
    expect(reset).toHaveProperty('disabled', true)
    await user.type(within(form).getByPlaceholderText('At least 12 characters'), 'twelve-chars')
    await user.click(reset)
    await waitFor(() => expect(screen.queryByRole('heading', { name: 'Reset the password of Bob' })).toBeNull())
    expect(bodyOf(fetch, 'POST', '/api/users/bob/password')).toEqual({ password: 'twelve-chars' })
    expect(screen.queryByRole('heading', { name: /New password/ })).toBeNull()
  })

  it('confirms a password reset to a typed one', async () => {
    mockFetch(
      on('GET', '/api/users', () => json(listing())),
      on('POST', '/api/users/bob/password', () => json({ status: 'ok' })),
    )
    const success = vi.spyOn(toast, 'success')
    show()
    const user = userEvent.setup()
    await user.click(within(row(await screen.findByText('Bob'))).getByRole('button', { name: 'Reset password' }))
    const form = dialog('Reset the password of Bob')
    await user.click(within(form).getByRole('button', { name: 'Type one' }))
    await user.type(within(form).getByPlaceholderText('At least 12 characters'), 'twelve-chars')
    await user.click(within(form).getByRole('button', { name: 'Reset password' }))
    await waitFor(() => expect(screen.queryByRole('heading', { name: 'Reset the password of Bob' })).toBeNull())
    expect(success).toHaveBeenCalledWith(expect.stringContaining('bob'))
  })

  it('deletes a user only after the confirmation', async () => {
    const fetch = mockFetch(
      on('GET', '/api/users', () => json(listing())),
      on('DELETE', '/api/users/bob', () => new Response(null, { status: 204 })),
    )
    const success = vi.spyOn(toast, 'success')
    show()
    const user = userEvent.setup()
    await user.click(within(row(await screen.findByText('Bob'))).getByRole('button', { name: 'Delete' }))
    await user.click(within(dialog('Delete Bob?')).getByRole('button', { name: 'Cancel' }))
    expect(screen.queryByRole('heading', { name: 'Delete Bob?' })).toBeNull()
    expect(calls(fetch, 'DELETE', '/api/users/bob')).toBe(0)

    await user.click(within(row('Bob')).getByRole('button', { name: 'Delete' }))
    await user.click(within(dialog('Delete Bob?')).getByRole('button', { name: 'Delete user' }))
    await waitFor(() => expect(success).toHaveBeenCalledWith('Deleted the user bob'))
    expect(calls(fetch, 'DELETE', '/api/users/bob')).toBe(1)
    expect(screen.queryByRole('heading', { name: 'Delete Bob?' })).toBeNull()
  })

  it('keeps the delete confirmation open with the reason when deleting fails', async () => {
    mockFetch(
      on('GET', '/api/users', () => json(listing())),
      on('DELETE', '/api/users/bob', () => json({ error: 'No such user' }, 404)),
    )
    const success = vi.spyOn(toast, 'success')
    show()
    const user = userEvent.setup()
    await user.click(within(row(await screen.findByText('Bob'))).getByRole('button', { name: 'Delete' }))
    const confirm = dialog('Delete Bob?')
    await user.click(within(confirm).getByRole('button', { name: 'Delete user' }))
    expect(await within(confirm).findByText('No such user')).toBeTruthy()
    expect(within(confirm).getByRole('button', { name: 'Delete user' })).toHaveProperty('disabled', false)
    expect(success).not.toHaveBeenCalled()
  })
})
