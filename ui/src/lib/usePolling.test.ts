import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, renderHook } from '@testing-library/react'
import { usePolling } from './usePolling'

beforeEach(() => { vi.useFakeTimers() })
afterEach(() => { vi.useRealTimers() })

describe('usePolling', () => {
  it('loads at once and then on every interval until unmounted', () => {
    const load = vi.fn()
    const { unmount } = renderHook(() => usePolling(load, 1000))
    expect(load).toHaveBeenCalledTimes(1)
    act(() => vi.advanceTimersByTime(2999))
    expect(load).toHaveBeenCalledTimes(3)
    unmount()
    act(() => vi.advanceTimersByTime(5000))
    expect(load).toHaveBeenCalledTimes(3)
  })

  it('loads once when there is no interval', () => {
    const load = vi.fn()
    renderHook(() => usePolling(load, null))
    act(() => vi.advanceTimersByTime(60_000))
    expect(load).toHaveBeenCalledTimes(1)
  })

  it('always calls the latest load without restarting the timer', () => {
    const seen: string[] = []
    const { rerender } = renderHook(({ ns }) => usePolling(() => seen.push(ns), 1000), { initialProps: { ns: 'a' } })
    act(() => vi.advanceTimersByTime(500))
    rerender({ ns: 'b' })
    act(() => vi.advanceTimersByTime(500))
    expect(seen).toEqual(['a', 'b'])
  })

  it('starts over when the key changes', () => {
    const seen: string[] = []
    const { rerender } = renderHook(({ ns }) => usePolling(() => seen.push(ns), 1000, ns), { initialProps: { ns: 'a' } })
    act(() => vi.advanceTimersByTime(800))
    rerender({ ns: 'b' })
    expect(seen).toEqual(['a', 'b'])
    act(() => vi.advanceTimersByTime(800))
    expect(seen).toEqual(['a', 'b'])
    act(() => vi.advanceTimersByTime(200))
    expect(seen).toEqual(['a', 'b', 'b'])
  })
})
