import { afterEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { json, mockFetch, on } from '../test/fetch'
import RecommendationsPanel from './RecommendationsPanel'
import SettingsPage from '../pages/SettingsPage'
import { AuthContext, type User } from '../lib/auth'
import { describeState, describeValues, rightsizingForm, rightsizingProblem, rightsizingRequest, type RightsizingChange } from '../lib/rightsizing'

const operator: User = { sub: 'user:ops', name: 'ops', role: 'operator', provider: 'local' }
const viewer: User = { ...operator, role: 'viewer' }
const report = {
  namespace: 'shop', source: 'metrics-server', historical: false, basis: 'A single reading.', currency: 'USD', monthlySavings: 40,
  workloads: [{ kind: 'Deployment', name: 'api', replicas: 2, monthlySavings: 40, containers: [{ name: 'main',
    cpu: { request: '2', observed: '100m', recommended: '150m', action: 'reduce' }, memory: { request: '4Gi', observed: '300Mi', recommended: '384Mi', action: 'reduce' } }] }],
}
const change = (over: Partial<RightsizingChange>): RightsizingChange => ({
  namespace: 'shop', kind: 'Deployment', name: 'api', original: { cpuRequest: '2', memoryRequest: '4Gi' }, optimized: { cpuRequest: '150m', memoryRequest: '384Mi' },
  containers: [{ name: 'main', original: { cpuRequest: '2', memoryRequest: '4Gi' }, optimized: { cpuRequest: '150m', memoryRequest: '384Mi' } }],
  appliedAt: '2026-10-23T12:00:00Z', appliedBy: 'ops', ...over,
})

afterEach(() => { vi.restoreAllMocks() })

describe('Right-sizing', () => {
  it('describes changes and builds the settings request', () => {
    expect(describeValues(change({}))).toBe('CPU 2 → 150m, memory 4Gi → 384Mi')
    expect(describeState(change({ state: 'reverted', reason: 'container main of pod api-1 restarted 1 time(s)' }), 30)).toBe('Put back: container main of pod api-1 restarted 1 time(s)')
    expect(describeState(change({ state: 'applied' }), 30, new Date('2026-10-23T12:05:00Z'))).toMatch(/^Applied by ops .*; its new pods are watched until .* and get the old requests back if one fails$/)
    const f = rightsizingForm({ apply: true, guardMinutes: 60 })
    expect(rightsizingRequest(f)).toEqual({ apply: true, guardMinutes: 60, removeGit: true })
    const g = { provider: 'gitea' as const, repository: 'acme/apps', apiUrl: '', baseBranch: 'main', path: '', token: 't', hasToken: false }
    expect(rightsizingProblem({ ...f, git: g })).toMatch(/needs its API URL/)
    expect(rightsizingProblem({ ...f, git: { ...g, apiUrl: 'http://gitea/api/v1', token: '' } })).toMatch(/access token/)
    expect(rightsizingProblem({ ...f, git: { ...g, apiUrl: 'http://gitea/api/v1' } })).toBeNull()
  })

  it('applies advice after a confirmation, opens a pull request, and puts a change back', async () => {
    let changes = { applyEnabled: true, guardMinutes: 30, repository: 'acme/apps (gitea)', changes: [change({ name: 'old', state: 'reverted', reason: 'container main of pod old-1 was OOMKilled' })] }
    const fetch = mockFetch(
      on('GET', '/api/namespaces/shop/recommendations', () => json(report)),
      on('GET', '/api/rightsizing/changes', () => json(changes)),
      on('POST', '/api/namespaces/shop/rightsizing/apply', () => {
        changes = { ...changes, changes: [change({ state: 'applied' }), ...changes.changes] }
        return json(change({ state: 'applied' }))
      }),
      on('POST', '/api/namespaces/shop/rightsizing/pull-request', () => json(change({ state: 'proposed', pullRequest: 'http://gitea/acme/apps/pulls/1' }))),
      on('POST', '/api/namespaces/shop/rightsizing/revert', () => json(change({ state: 'reverted' }))),
    )
    const opened = vi.spyOn(window, 'open').mockReturnValue(null)
    render(<AuthContext.Provider value={operator}><RecommendationsPanel namespace="shop" /></AuthContext.Provider>)
    const user = userEvent.setup()
    expect(await screen.findByText(/watches its new pods for 30 minutes/)).toBeTruthy()
    expect(screen.getByText('Put back: container main of pod old-1 was OOMKilled')).toBeTruthy()

    await user.click(await screen.findByRole('button', { name: 'Pull request' }))
    await waitFor(() => expect(opened).toHaveBeenCalledWith('http://gitea/acme/apps/pulls/1', '_blank', 'noopener'))

    await user.click(screen.getByRole('button', { name: 'Apply' }))
    expect(screen.getByText('Pods of api restart.')).toBeTruthy()
    await user.click(screen.getByRole('button', { name: 'Apply' }))
    const post = fetch.mock.calls.find(([url]) => url === '/api/namespaces/shop/rightsizing/apply')!
    expect(JSON.parse(post[1]!.body as string)).toEqual({ kind: 'Deployment', name: 'api' })
    const list = await screen.findByRole('list', { name: 'Right-sizing changes' })
    await waitFor(() => expect(within(list).getByText(/its new pods are watched/)).toBeTruthy())
    expect(screen.queryByRole('button', { name: 'Apply' })).toBeNull()

    await user.click(within(list).getByRole('button', { name: 'Put back' }))
    await waitFor(() => expect(fetch.mock.calls.some(([url]) => url === '/api/namespaces/shop/rightsizing/revert')).toBe(true))
  })

  it('offers no action to a viewer', async () => {
    mockFetch(
      on('GET', '/api/namespaces/shop/recommendations', () => json(report)),
      on('GET', '/api/rightsizing/changes', () => json({ applyEnabled: true, guardMinutes: 30, repository: 'acme/apps (github)', changes: [change({ state: 'applied' })] })),
    )
    render(<AuthContext.Provider value={viewer}><RecommendationsPanel namespace="shop" /></AuthContext.Provider>)
    expect(await screen.findByText(/its new pods are watched/)).toBeTruthy()
    expect(screen.queryByRole('button', { name: /Apply|Pull request|Put back/ })).toBeNull()
  })

  it('turns applying on with a warning, tests a repository and saves', async () => {
    window.history.replaceState(null, '', '/settings/rightsizing')
    const settings = { providers: {}, integrations: {}, auth: { disableLocalLogin: false }, calendars: [], rightsizing: { apply: false, guardMinutes: 30 } }
    const fetch = mockFetch(
      on('GET', '/api/settings', () => json(settings)),
      on('PUT', '/api/settings', () => json(settings)),
      on('POST', '/api/settings/providers/git/test', () => json({ connected: true, message: 'Reached acme/apps' })),
    )
    render(<SettingsPage section="rightsizing" />)
    const user = userEvent.setup()
    await user.click(await screen.findByRole('switch', { name: 'Enable Apply advice to workloads' }))
    expect(screen.getByRole('alert').textContent).toMatch(/new pods are watched.*GitOps tool manages are left alone/s)
    await user.selectOptions(screen.getByLabelText('Watch new pods for'), '60')
    await user.click(screen.getByRole('button', { name: 'Add a repository' }))
    await user.selectOptions(screen.getByLabelText('Provider'), 'gitea')
    await user.type(screen.getByLabelText('Repository'), 'acme/apps')
    await user.type(screen.getByLabelText(/API URL/), 'http://gitea.example.com/api/v1')
    await user.type(screen.getByLabelText('Git access token'), 'gt')
    await user.click(screen.getByRole('button', { name: 'Test connection' }))
    expect(await screen.findByText('Reached acme/apps')).toBeTruthy()
    await user.click(screen.getByRole('button', { name: 'Save' }))
    await screen.findByText('Saved')
    const put = JSON.parse(fetch.mock.calls.find(([, init]) => init?.method === 'PUT')![1]!.body as string)
    expect(put).toEqual({ rightsizing: { apply: true, guardMinutes: 60, git: { provider: 'gitea', repository: 'acme/apps', apiUrl: 'http://gitea.example.com/api/v1', baseBranch: 'main', path: '', token: 'gt' } } })
  })
})
