import { useSyncExternalStore } from 'react'

// Toasts report what an action did, wherever the button that started it was: a schedule
// saved, a namespace starting, a token revoked. Confirmations fade after a few seconds;
// errors stay until they are closed, so a failed action never goes unnoticed. Errors that
// belong to a form field are still shown next to the field.
export interface Toast { id: number; tone: 'success' | 'error'; text: string }

const SUCCESS_MS = 5000
const MAX = 4

let toasts: Toast[] = []
let nextId = 1
const listeners = new Set<() => void>()
const emit = () => listeners.forEach(l => l())

export function dismissToast(id: number) {
  toasts = toasts.filter(t => t.id !== id)
  emit()
}

function show(tone: Toast['tone'], text: string) {
  const id = nextId++
  // A burst of actions keeps only the latest few on screen.
  toasts = [...toasts, { id, tone, text }].slice(-MAX)
  emit()
  if (tone === 'success') setTimeout(() => dismissToast(id), SUCCESS_MS)
}

export const toast = {
  success: (text: string) => show('success', text),
  error: (text: string) => show('error', text),
}

function subscribe(onChange: () => void) {
  listeners.add(onChange)
  return () => { listeners.delete(onChange) }
}

export function useToasts(): Toast[] {
  return useSyncExternalStore(subscribe, () => toasts)
}
