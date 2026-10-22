import { beforeEach, describe, expect, it, vi } from 'vitest'
import { act, renderHook, waitFor } from '@testing-library/react'
import type { MouseEvent } from 'react'

// The router keeps module state (what is shown, the guard), so every test loads a fresh
// copy on a fresh address.
let router: typeof import('./router')

beforeEach(async () => {
  window.history.replaceState(null, '', '/')
  vi.resetModules()
  router = await import('./router')
})

const useLocation = () => ({ path: router.usePath(), hash: router.useHash(), pending: router.usePendingLeave() })
const staysInSettings = (path: string) => path.startsWith('/settings')

describe('splitPath', () => {
  it('splits and decodes segments', () => {
    expect(router.splitPath('/namespaces/shop%20dev/')).toEqual(['namespaces', 'shop dev'])
    expect(router.splitPath('/')).toEqual([])
  })
  it('keeps a segment that is not valid percent-encoding', () => {
    expect(router.splitPath('/docs/100%')).toEqual(['docs', '100%'])
  })
  it('encodes a name as one segment', () => {
    expect(router.segment('a/b c')).toBe('a%2Fb%20c')
  })
})

describe('navigate', () => {
  it('adds a history entry and shows the new path and hash', () => {
    const { result } = renderHook(useLocation)
    const before = window.history.length
    act(() => router.navigate('/docs/schedules#gitops'))
    expect(result.current.path).toBe('/docs/schedules')
    expect(result.current.hash).toBe('gitops')
    expect(window.history.length).toBe(before + 1)
  })
  it('does nothing for the page already shown', () => {
    act(() => router.navigate('/cluster'))
    const before = window.history.length
    act(() => router.navigate('/cluster'))
    expect(window.history.length).toBe(before)
  })
  it('replaces the entry when asked to', () => {
    const { result } = renderHook(useLocation)
    const before = window.history.length
    act(() => router.navigate('/budgets', { replace: true }))
    expect(result.current.path).toBe('/budgets')
    expect(window.history.length).toBe(before)
  })
  it('follows Back and Forward', async () => {
    const { result } = renderHook(useLocation)
    act(() => router.navigate('/cluster'))
    act(() => router.navigate('/storage'))
    act(() => window.history.back())
    await waitFor(() => expect(result.current.path).toBe('/cluster'))
    act(() => window.history.forward())
    await waitFor(() => expect(result.current.path).toBe('/storage'))
  })
})

describe('followLink', () => {
  const click = (init: Partial<MouseEvent<HTMLAnchorElement>> = {}) => {
    const e = { defaultPrevented: false, button: 0, metaKey: false, ctrlKey: false, shiftKey: false, altKey: false, preventDefault: vi.fn(), ...init }
    router.followLink(e as unknown as MouseEvent<HTMLAnchorElement>, '/reports')
    return e
  }
  it('navigates in place on a plain click', () => {
    const e = click()
    expect(e.preventDefault).toHaveBeenCalled()
    expect(window.location.pathname).toBe('/reports')
  })
  it.each([['metaKey'], ['ctrlKey'], ['shiftKey'], ['altKey']])('leaves a click with %s to the browser', key => {
    const e = click({ [key]: true })
    expect(e.preventDefault).not.toHaveBeenCalled()
    expect(window.location.pathname).toBe('/')
  })
  it('leaves a middle click to the browser', () => {
    expect(click({ button: 1 }).preventDefault).not.toHaveBeenCalled()
  })
})

describe('useLeaveGuard', () => {
  const guarded = (unsaved: string | null) => renderHook(({ u }) => { router.useLeaveGuard(u, staysInSettings); return useLocation() }, { initialProps: { u: unsaved } })

  beforeEach(() => {
    act(() => router.navigate('/settings/notifications'))
  })

  it('lets navigation within the page through', () => {
    const { result } = guarded('Notifications')
    act(() => router.navigate('/settings/ai'))
    expect(result.current.path).toBe('/settings/ai')
    expect(result.current.pending).toBeNull()
  })
  it('asks before leaving, and staying keeps the page', () => {
    const { result } = guarded('Notifications')
    act(() => router.navigate('/'))
    expect(result.current.pending).toBe('Notifications')
    expect(result.current.path).toBe('/settings/notifications')
    act(() => router.stayOnPage())
    expect(result.current.pending).toBeNull()
    expect(window.location.pathname).toBe('/settings/notifications')
  })
  it('leaves after all when the user says so', () => {
    const { result } = guarded('Notifications')
    act(() => router.navigate('/budgets'))
    act(() => router.leavePage())
    expect(result.current.pending).toBeNull()
    expect(result.current.path).toBe('/budgets')
  })
  it('holds the browser Back, restores the address, and goes back when told to', async () => {
    const { result } = guarded('Notifications')
    act(() => window.history.back())
    await waitFor(() => expect(result.current.pending).toBe('Notifications'))
    await waitFor(() => expect(window.location.pathname).toBe('/settings/notifications'))
    expect(result.current.path).toBe('/settings/notifications')
    act(() => router.leavePage())
    await waitFor(() => expect(result.current.path).toBe('/'))
    expect(window.location.pathname).toBe('/')
  })
  it('keeps working after staying on a held Back', async () => {
    const { result } = guarded('Notifications')
    act(() => window.history.back())
    await waitFor(() => expect(result.current.pending).toBe('Notifications'))
    act(() => router.stayOnPage())
    act(() => router.navigate('/settings/mcp'))
    expect(result.current.path).toBe('/settings/mcp')
    act(() => router.navigate('/cluster'))
    expect(result.current.pending).toBe('Notifications')
  })
  it('does not hold anything once nothing is unsaved', () => {
    const { result, rerender } = guarded('Notifications')
    rerender({ u: null })
    act(() => router.navigate('/'))
    expect(result.current.pending).toBeNull()
    expect(result.current.path).toBe('/')
  })
  it('asks the browser to warn before a reload only while something is unsaved', () => {
    const fire = () => {
      const e = new Event('beforeunload', { cancelable: true })
      window.dispatchEvent(e)
      return e.defaultPrevented
    }
    const { unmount } = guarded('Notifications')
    expect(fire()).toBe(true)
    unmount()
    expect(fire()).toBe(false)
  })
})
