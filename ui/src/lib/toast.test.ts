import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, renderHook } from '@testing-library/react'

let mod: typeof import('./toast')
beforeEach(async () => {
  vi.resetModules()
  vi.useFakeTimers()
  mod = await import('./toast')
})
afterEach(() => vi.useRealTimers())

describe('toast', () => {
  it('fades confirmations after five seconds', () => {
    const { result } = renderHook(() => mod.useToasts())
    act(() => mod.toast.success('Saved'))
    expect(result.current.map(t => t.text)).toEqual(['Saved'])
    act(() => vi.advanceTimersByTime(4999))
    expect(result.current).toHaveLength(1)
    act(() => vi.advanceTimersByTime(1))
    expect(result.current).toHaveLength(0)
  })
  it('keeps errors until they are closed', () => {
    const { result } = renderHook(() => mod.useToasts())
    act(() => mod.toast.error('Could not save'))
    act(() => vi.advanceTimersByTime(60_000))
    expect(result.current.map(t => [t.tone, t.text])).toEqual([['error', 'Could not save']])
    act(() => mod.dismissToast(result.current[0].id))
    expect(result.current).toHaveLength(0)
  })
  it('shows only the latest four', () => {
    const { result } = renderHook(() => mod.useToasts())
    act(() => { for (let i = 1; i <= 6; i++) mod.toast.error(`e${i}`) })
    expect(result.current.map(t => t.text)).toEqual(['e3', 'e4', 'e5', 'e6'])
  })
})
