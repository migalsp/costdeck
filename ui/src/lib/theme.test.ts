import { beforeEach, describe, expect, it, vi } from 'vitest'
import { act, renderHook } from '@testing-library/react'

// A controllable prefers-color-scheme.
let systemDark = false
const listeners: (() => void)[] = []
const setSystemDark = (v: boolean) => { systemDark = v; listeners.forEach(l => l()) }

beforeEach(() => {
  systemDark = false
  listeners.length = 0
  vi.resetModules()
  window.matchMedia = ((query: string) => ({
    get matches() { return systemDark }, media: query,
    addEventListener: (_: string, l: () => void) => listeners.push(l), removeEventListener() {},
  })) as unknown as typeof window.matchMedia
})

const load = () => import('./theme')
const isDark = () => document.documentElement.classList.contains('dark')

describe('theme', () => {
  it('follows the system by default, also when it changes', async () => {
    systemDark = true
    await load()
    expect(isDark()).toBe(true)
    setSystemDark(false)
    expect(isDark()).toBe(false)
  })
  it('applies and remembers an explicit choice', async () => {
    const theme = await load()
    const { result } = renderHook(() => theme.useTheme())
    expect(result.current).toBe('system')
    act(() => theme.setTheme('dark'))
    expect(result.current).toBe('dark')
    expect(isDark()).toBe(true)
    expect(localStorage.getItem('costdeck.theme')).toBe('dark')
    setSystemDark(false)
    expect(isDark()).toBe(true)
  })
  it('forgets the choice when set back to the system', async () => {
    localStorage.setItem('costdeck.theme', 'light')
    systemDark = true
    const theme = await load()
    expect(isDark()).toBe(false)
    act(() => theme.setTheme('system'))
    expect(localStorage.getItem('costdeck.theme')).toBeNull()
    expect(isDark()).toBe(true)
  })
  it('picks up a choice made in another tab', async () => {
    const theme = await load()
    const { result } = renderHook(() => theme.useTheme())
    localStorage.setItem('costdeck.theme', 'dark')
    act(() => { window.dispatchEvent(new StorageEvent('storage', { key: 'costdeck.theme' })) })
    expect(result.current).toBe('dark')
    expect(isDark()).toBe(true)
  })
  it('ignores a stored value it does not know', async () => {
    localStorage.setItem('costdeck.theme', 'sepia')
    const theme = await load()
    const { result } = renderHook(() => theme.useTheme())
    expect(result.current).toBe('system')
  })
})

describe('theme before the first paint', () => {
  it('comes from a script file, since the content security policy refuses inline scripts', async () => {
    const { default: html } = await import('../../index.html?raw')
    expect(html).toContain('<script src="/theme-init.js"></script>')
    expect(html).not.toMatch(/<script>\s*\S/)
  })

  it('applies the saved choice the way the theme hook reads it', async () => {
    const { default: script } = await import('../../public/theme-init.js?raw')
    const apply = new Function(script) as () => void
    for (const [saved, system, dark] of [['dark', false, true], ['light', true, false], [null, true, true], [null, false, false]] as const) {
      document.documentElement.classList.remove('dark')
      if (saved) localStorage.setItem('costdeck.theme', saved)
      else localStorage.removeItem('costdeck.theme')
      setSystemDark(system)
      apply()
      expect(isDark(), `saved ${saved}, system dark ${system}`).toBe(dark)
    }
  })
})
