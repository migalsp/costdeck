import { useSyncExternalStore } from 'react'

// The colour theme: follow the operating system, or always light or dark. The choice is
// a per-browser convenience kept in localStorage; public/theme-init.js applies it before the first
// paint with the same rules, so a reload never flashes the other theme.
export type Theme = 'system' | 'light' | 'dark'

const KEY = 'costdeck.theme'
const systemDark = window.matchMedia('(prefers-color-scheme: dark)')
const listeners = new Set<() => void>()

function stored(): Theme {
  try {
    const v = localStorage.getItem(KEY)
    return v === 'light' || v === 'dark' ? v : 'system'
  } catch {
    return 'system'
  }
}

let theme = stored()

function apply() {
  document.documentElement.classList.toggle('dark', theme === 'dark' || (theme === 'system' && systemDark.matches))
}

function changed() {
  apply()
  listeners.forEach(l => l())
}

apply()
systemDark.addEventListener('change', apply)
// Another tab changed the theme.
window.addEventListener('storage', e => {
  if (e.key !== KEY) return
  theme = stored()
  changed()
})

export function setTheme(t: Theme) {
  theme = t
  try {
    if (t === 'system') localStorage.removeItem(KEY)
    else localStorage.setItem(KEY, t)
  } catch {
    // Not remembered; it still applies until the page is reloaded.
  }
  changed()
}

function subscribe(onChange: () => void) {
  listeners.add(onChange)
  return () => { listeners.delete(onChange) }
}

export function useTheme(): Theme {
  return useSyncExternalStore(subscribe, () => theme)
}
