import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { json, mockFetch, on } from './test/fetch'

// ResizeObserver backs the charts; jsdom has none.
vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} })

const signedIn = (role: 'viewer' | 'operator' | 'admin') => mockFetch(
  on('GET', '/api/auth/me', () => json({ sub: 'local:pat', name: 'pat', role, provider: 'local' })),
  on('GET', '/api/version', () => json({ version: '9.9.9' })),
)

// renderAt loads the app on a path, as a reload or a shared link would.
async function renderAt(path: string, role: 'viewer' | 'operator' | 'admin' = 'admin') {
  window.history.replaceState(null, '', path)
  vi.resetModules()
  signedIn(role)
  const { default: App } = await import('./App')
  render(<App />)
  await screen.findByText('9.9.9')
}

beforeEach(() => { document.title = '' })

describe('page addresses', () => {
  it('opens the page named in the address', async () => {
    await renderAt('/docs/schedules')
    expect(await screen.findByRole('heading', { level: 1, name: 'Schedules' })).toBeTruthy()
    expect(document.title).toBe('Documentation · Cost Deck')
    expect(screen.getByRole('link', { name: 'Documentation' }).getAttribute('aria-current')).toBe('page')
  })
  it('titles a namespace page after the namespace', async () => {
    await renderAt('/scaling/shop-dev')
    await waitFor(() => expect(document.title).toBe('shop-dev · Cost Deck'))
    expect(screen.getByRole('link', { name: 'Scaling Schedules' }).getAttribute('aria-current')).toBe('page')
  })
  it('says when nothing lives at an address', async () => {
    await renderAt('/no/such/page')
    expect(await screen.findByText('Page not found')).toBeTruthy()
    expect(screen.getByText('Nothing lives at /no/such/page.')).toBeTruthy()
    expect(document.title).toBe('Page not found · Cost Deck')
  })
  it('refuses a second segment where a page has none', async () => {
    await renderAt('/cluster/extra')
    expect(await screen.findByText('Page not found')).toBeTruthy()
  })
  it('keeps the settings to administrators', async () => {
    await renderAt('/settings/ai', 'viewer')
    expect(await screen.findByText('Only administrators can change the settings.')).toBeTruthy()
    expect(screen.queryByRole('link', { name: 'Settings' })).toBeNull()
  })
  it('moves between pages through the sidebar links and Back', async () => {
    await renderAt('/docs')
    const user = userEvent.setup()
    await user.click(screen.getByRole('link', { name: 'Budgets & Alerts' }))
    expect(window.location.pathname).toBe('/budgets')
    expect(document.title).toBe('Budgets & Alerts · Cost Deck')
    window.history.back()
    await waitFor(() => expect(document.title).toBe('Documentation · Cost Deck'))
  })
  it('makes the sidebar entries real links', async () => {
    await renderAt('/')
    expect(screen.getByRole('link', { name: 'Cluster Node Map' }).getAttribute('href')).toBe('/cluster')
    expect(screen.getByRole('link', { name: 'Cost Overview' }).getAttribute('href')).toBe('/')
  })
  it('keeps every page reachable on a short screen', async () => {
    await renderAt('/')
    const nav = screen.getByRole('navigation', { name: 'Pages' })
    // A flex child only scrolls when it may shrink below its content: without min-h-0 the
    // links ran under the account controls and Settings could not be reached.
    expect(nav.className).toMatch(/\bmin-h-0\b/)
    expect(nav.className).toMatch(/\boverflow-y-auto\b/)
    expect(within(nav).getAllByRole('link').map(a => a.textContent)).toEqual([
      'Cost Overview', 'Clusters', 'Namespace Insights', 'Cluster Node Map', 'Storage & Network', 'AI Costs',
      'Scaling Schedules', 'Budgets & Alerts', 'Reports', 'Health', 'Documentation', 'Settings',
    ])
    // The account controls are one row of buttons, outside the scrolling links.
    const sidebar = screen.getByRole('complementary', { name: 'Sidebar' })
    expect(within(nav).queryByRole('button', { name: 'Sign out' })).toBeNull()
    expect(within(sidebar).getByRole('button', { name: 'Sign out' })).toBeTruthy()
  })
})

describe('theme switch', () => {
  it('switches the theme from the sidebar', async () => {
    await renderAt('/docs')
    const user = userEvent.setup()
    await user.click(screen.getByRole('radio', { name: 'Dark' }))
    expect(document.documentElement.classList.contains('dark')).toBe(true)
    expect(screen.getByRole('radio', { name: 'Dark' }).getAttribute('aria-checked')).toBe('true')
    await user.click(screen.getByRole('radio', { name: 'Light' }))
    expect(document.documentElement.classList.contains('dark')).toBe(false)
  })
})
