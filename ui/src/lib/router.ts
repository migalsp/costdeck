import { useSyncExternalStore, type MouseEvent } from 'react'

// The dashboard keeps the page it shows in the address bar, so a reload stays on it, the
// browser's Back and Forward move between pages and a link can be shared. The server
// answers every path that is not a file with index.html (spaHandler), so any of these
// URLs can be opened directly.

const NAVIGATED = 'costdeck:navigated'

function subscribe(onChange: () => void) {
  window.addEventListener('popstate', onChange)
  window.addEventListener(NAVIGATED, onChange)
  return () => {
    window.removeEventListener('popstate', onChange)
    window.removeEventListener(NAVIGATED, onChange)
  }
}

const currentPath = () => window.location.pathname
const currentHash = () => decodeURIComponent(window.location.hash.slice(1))

// usePath returns the current path, such as /namespaces/shop-dev.
export function usePath(): string {
  return useSyncExternalStore(subscribe, currentPath)
}

// useHash returns the fragment without its #, which the docs use for a topic.
export function useHash(): string {
  return useSyncExternalStore(subscribe, currentHash)
}

// navigate opens a page. replace swaps the current history entry instead of adding one,
// for corrections the user should not step back through.
export function navigate(to: string, { replace = false }: { replace?: boolean } = {}) {
  if (to === window.location.pathname + window.location.search + window.location.hash) return
  window.history[replace ? 'replaceState' : 'pushState'](null, '', to)
  window.dispatchEvent(new Event(NAVIGATED))
}

// followLink handles a click on an <a href> inside the app: a plain click navigates in
// place, while a click with a modifier or the middle button keeps the browser's own
// behaviour (open in a new tab or window).
export function followLink(e: MouseEvent<HTMLAnchorElement>, to: string) {
  if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return
  e.preventDefault()
  navigate(to)
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
