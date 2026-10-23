import { describe, expect, it } from 'vitest'
import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { json, mockFetch, on } from '../test/fetch'
import LoginPage from './LoginPage'
import SettingsPage from './SettingsPage'
import { oidcProblem, oidcRequest, emptyOIDC } from '../lib/oidc'

describe('OpenID Connect sign-in', () => {
  it('offers the provider on the login page under its name', async () => {
    mockFetch(on('GET', '/api/auth/config', () => json({ localLogin: true, entra: { enabled: false }, oidc: { enabled: true, displayName: 'Keycloak' } })))
    render(<LoginPage onLogin={() => {}} />)
    expect(await screen.findByRole('button', { name: 'Sign in with Keycloak' })).toBeTruthy()
    expect(screen.getByText('or')).toBeTruthy()
  })

  it('fills in the Keycloak defaults and saves the settings', async () => {
    window.history.replaceState(null, '', '/settings/sso')
    const settings = { providers: {}, integrations: {}, calendars: [], auth: { disableLocalLogin: false } }
    const fetch = mockFetch(on('GET', '/api/settings', () => json(settings)), on('PUT', '/api/settings', () => json(settings)))
    render(<SettingsPage section="sso" />)
    const user = userEvent.setup()
    await user.click(await screen.findByRole('switch', { name: 'Enable OpenID Connect' }))
    await user.click(screen.getByRole('button', { name: 'Keycloak defaults' }))
    await user.type(screen.getByLabelText('Issuer URL'), 'https://keycloak.example.com/realms/costdeck')
    await user.type(screen.getByLabelText('Client ID'), 'costdeck')
    await user.type(screen.getByLabelText('Client secret'), 's3cr3t')
    const card = within(screen.getByTestId('oidc-settings'))
    await user.click(card.getByRole('button', { name: 'Add a group' }))
    await user.type(card.getByLabelText('Group'), '/costdeck-admins')
    await user.selectOptions(card.getByLabelText('Role'), 'admin')
    await user.click(screen.getByRole('button', { name: 'Save' }))
    await screen.findByText('Saved')
    const put = fetch.mock.calls.find(([, init]) => init?.method === 'PUT')!
    expect(JSON.parse(put[1]!.body as string).auth.oidc).toEqual({
      enabled: true, displayName: 'Keycloak', issuerUrl: 'https://keycloak.example.com/realms/costdeck', clientId: 'costdeck', redirectUrl: '',
      scopes: ['profile', 'email'], groupsClaim: 'groups', rolesClaim: 'realm_access.roles', defaultRole: 'viewer', autoProvision: true,
      skipSslVerify: false, groupRoleMapping: { '/costdeck-admins': 'admin' }, clientSecret: 's3cr3t',
    })
  })

  it('refuses what the operator would refuse', async () => {
    expect(oidcProblem({ ...emptyOIDC, issuerUrl: 'http://keycloak.local/realms/x' })).toContain('must be HTTPS')
    expect(oidcProblem({ ...emptyOIDC, enabled: true })).toContain('are required')
    expect(oidcProblem({ ...emptyOIDC, enabled: true, issuerUrl: 'https://kc/realms/x', clientId: 'c' })).toBeNull()
    expect(oidcRequest({ ...emptyOIDC, scopes: ' profile, email groups ' }).scopes).toEqual(['profile', 'email', 'groups'])
    expect('clientSecret' in oidcRequest(emptyOIDC)).toBe(false)

    window.history.replaceState(null, '', '/settings/sso')
    const settings = { providers: {}, integrations: {}, calendars: [], auth: { disableLocalLogin: false } }
    const fetch = mockFetch(on('GET', '/api/settings', () => json(settings)))
    render(<SettingsPage section="sso" />)
    const user = userEvent.setup()
    await user.type(await screen.findByLabelText('Issuer URL'), 'http://keycloak.local/realms/x')
    await user.click(screen.getByRole('button', { name: 'Save' }))
    expect(await screen.findByText(/Not saved: The issuer URL must be HTTPS/)).toBeTruthy()
    expect(fetch.mock.calls.some(([, init]) => init?.method === 'PUT')).toBe(false)
  })

  it('shows single sign-on as on in the settings menu when only OpenID Connect is set up', async () => {
    window.history.replaceState(null, '', '/settings/sso')
    const settings = { providers: {}, integrations: {}, calendars: [], auth: { disableLocalLogin: false, entra: { enabled: false }, oidc: { enabled: true, issuerUrl: 'https://keycloak.example.com/realms/x', clientId: 'costdeck', hasClientSecret: true, scopes: [], groupRoleMapping: {} } } }
    mockFetch(on('GET', '/api/settings', () => json(settings)), on('GET', '/api/users', () => json([])), on('GET', '/api/tokens', () => json([])))
    render(<SettingsPage section="sso" />)
    expect(await screen.findByRole('button', { name: /^Single sign-on\s*On$/ })).toBeTruthy()
  })
})
