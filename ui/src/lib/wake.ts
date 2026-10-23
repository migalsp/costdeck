import { getJSON } from './api'

// WakeStatus is GET /api/wake/{ns}: the schedule that keeps a namespace down and whether
// its routes show the sleep page.
export interface WakeStatus {
  namespace: string
  schedule?: { kind: 'group' | 'config'; name: string }
  desiredState?: 'Up' | 'Down'
  phase?: string
  mode?: string
  overrideExpiresAt?: string
  sleeping: boolean
  hostnames: string[]
}

export const fetchWakeStatus = (ns: string) => getJSON<WakeStatus>(`/api/wake/${encodeURIComponent(ns)}`)

// safeReturn keeps the way back to one of the namespace's own hosts, so the wake page
// cannot send anyone elsewhere.
export function safeReturn(raw: string | null, hostnames: string[]): string | null {
  if (!raw) return null
  try {
    const u = new URL(raw)
    if ((u.protocol === 'https:' || u.protocol === 'http:') && hostnames.includes(u.hostname)) return u.toString()
  } catch {
    // not a URL
  }
  return null
}

// isAwake: the namespace is up and its routes lead to the application again.
export const isAwake = (s: WakeStatus) => s.phase === 'ScaledUp' && !s.sleeping
