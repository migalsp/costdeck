import { useEffect, useSyncExternalStore, type MouseEvent } from 'react'

// The dashboard keeps the page it shows in the address bar, so a reload stays on it, the
// browser's Back and Forward move between pages and a link can be shared. The server
// answers every path that is not a file with index.html (spaHandler), so any of these
// URLs can be opened directly.
//
// A page with unsaved input can hold the user back (useLeaveGuard): leaving it through a
// link, navigate() or the browser's Back and Forward first asks (LeaveDialog), and a
// reload or closing the tab gets the browser's own warning.

const listeners = new Set<() => void>()
const notify = () => listeners.forEach(l => l())

function subscribe(onChange: () => void) {
  listeners.add(onChange)
  return () => { listeners.delete(onChange) }
}

function locationHash(): string {
  const raw = window.location.hash.slice(1)
  try {
    return decodeURIComponent(raw)
  } catch {
    return raw
  }
}

// Every history entry the app creates carries its position, so a Back or Forward that
// has to wait for an answer can be stepped back with history.go().
const positionOf = (state: unknown): number =>
  typeof (state as { idx?: unknown } | null)?.idx === 'number' ? (state as { idx: number }).idx : 0
if (typeof (window.history.state as { idx?: unknown } | null)?.idx !== 'number') {
  window.history.replaceState({ ...(window.history.state as object | null), idx: 0 }, '')
}

// What the app shows. It follows the address bar, except while a held Back or Forward
// is being stepped back.
let shownPath = window.location.pathname
let shownHash = locationHash()
let position = positionOf(window.history.state)

function show() {
  shownPath = window.location.pathname
  shownHash = locationHash()
  position = positionOf(window.history.state)
  notify()
}

interface Guard { unsaved: string; stays: (path: string) => boolean }
let guard: Guard | null = null
// The navigation waiting for the user's answer, and what it would lose.
let pending: { unsaved: string; proceed: () => void } | null = null
// The popstate that history.go() causes when a held Back or Forward is stepped back
// (ignored), or carried out after all (shown).
let stepping: 'back' | 'through' | null = null

const holds = (path: string) => guard !== null && !guard.stays(path)

function ask(proceed: () => void) {
  pending = { unsaved: guard!.unsaved, proceed }
  notify()
}

window.addEventListener('popstate', e => {
  if (stepping === 'back') {
    stepping = null
    return
  }
  const delta = positionOf(e.state) - position
  if (stepping === null && delta !== 0 && holds(window.location.pathname)) {
    stepping = 'back'
    window.history.go(-delta)
    ask(() => {
      stepping = 'through'
      window.history.go(delta)
    })
    return
  }
  stepping = null
  show()
})

// usePath returns the current path, such as /namespaces/shop-dev.
export function usePath(): string {
  return useSyncExternalStore(subscribe, () => shownPath)
}

// useHash returns the fragment without its #, which the docs use for a topic.
export function useHash(): string {
  return useSyncExternalStore(subscribe, () => shownHash)
}

// navigate opens a page. replace swaps the current history entry instead of adding one,
// for corrections the user should not step back through.
export function navigate(to: string, { replace = false }: { replace?: boolean } = {}) {
  const url = new URL(to, window.location.href)
  if (url.pathname + url.search + url.hash === window.location.pathname + window.location.search + window.location.hash) return
  const go = () => {
    window.history[replace ? 'replaceState' : 'pushState']({ idx: replace ? position : position + 1 }, '', to)
    show()
  }
  if (holds(url.pathname)) ask(go)
  else go()
}

// followLink handles a click on an <a href> inside the app: a plain click navigates in
// place, while a click with a modifier or the middle button keeps the browser's own
// behaviour (open in a new tab or window).
export function followLink(e: MouseEvent<HTMLAnchorElement>, to: string) {
  if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return
  e.preventDefault()
  navigate(to)
}

// useLeaveGuard holds the user on the page while unsaved names what would be lost, such
// as "Notifications". Paths for which stays returns true (other parts of the same page)
// are not held. stays should keep its identity between renders.
export function useLeaveGuard(unsaved: string | null, stays: (path: string) => boolean) {
  useEffect(() => {
    if (!unsaved) return
    const mine: Guard = { unsaved, stays }
    guard = mine
    const warn = (e: BeforeUnloadEvent) => {
      e.preventDefault()
      e.returnValue = '' // browsers that predate preventDefault() here
    }
    window.addEventListener('beforeunload', warn)
    return () => {
      if (guard === mine) guard = null
      window.removeEventListener('beforeunload', warn)
    }
  }, [unsaved, stays])
}

// usePendingLeave returns what a held navigation would lose, or null when none waits.
export function usePendingLeave(): string | null {
  return useSyncExternalStore(subscribe, () => pending?.unsaved ?? null)
}

export function stayOnPage() {
  pending = null
  notify()
}

export function leavePage() {
  const p = pending
  pending = null
  guard = null
  notify()
  p?.proceed()
}

// segment encodes a name for use as one path segment.
export const segment = (name: string) => encodeURIComponent(name)

// splitPath turns /a/b%20c into ['a', 'b c'].
export function splitPath(path: string): string[] {
  return path.split('/').filter(Boolean).map(s => {
    try {
      return decodeURIComponent(s)
    } catch {
      return s
    }
  })
}
