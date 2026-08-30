/** @vitest-environment jsdom */
import { act, renderHook } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { useBackendConnection } from './useBackendConnection'

describe('useBackendConnection', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })

  afterEach(() => {
    vi.useRealTimers()
    delete (window as { go?: unknown }).go
  })

  it('settles on the detected connection once the probe delay elapses', () => {
    ;(window as { go?: unknown }).go = { main: {} }
    const { result } = renderHook(() => useBackendConnection())

    expect(result.current.status).toBe('connecting')
    act(() => { vi.runAllTimers() })
    expect(result.current.status).toBe('connected')
  })

  it('clears the pending probe timer on unmount', () => {
    const clearTimer = vi.spyOn(window, 'clearTimeout')
    const { unmount } = renderHook(() => useBackendConnection())

    const pending = vi.getTimerCount()
    expect(pending).toBeGreaterThan(0)

    unmount()

    expect(clearTimer).toHaveBeenCalled()
    expect(vi.getTimerCount()).toBe(pending - 1)

    // The stale probe must not fire into the unmounted component. React logs a
    // warning through console.error if a state update escapes; nothing does.
    const consoleError = vi.spyOn(console, 'error').mockImplementation(() => undefined)
    act(() => { vi.runAllTimers() })
    expect(consoleError).not.toHaveBeenCalled()
    consoleError.mockRestore()
    clearTimer.mockRestore()
  })

  it('keeps a stable identity across renders so memoized consumers are not invalidated', () => {
    const { result, rerender } = renderHook(() => useBackendConnection())
    const initial = result.current
    const initialRefresh = result.current.refresh

    rerender()
    expect(result.current).toBe(initial)
    expect(result.current.refresh).toBe(initialRefresh)

    act(() => { vi.runAllTimers() })
    const settled = result.current
    expect(settled).not.toBe(initial)
    // Only the connection changed; `refresh` survives every render.
    expect(settled.refresh).toBe(initialRefresh)

    rerender()
    expect(result.current).toBe(settled)
  })

  it('cancels a refresh-triggered probe that is still pending at unmount (L-27)', () => {
    const { result, unmount } = renderHook(() => useBackendConnection())
    act(() => { vi.runAllTimers() })

    act(() => { result.current.refresh() })
    // Deliberately left in flight: `refresh` used to call the probe directly and
    // throw away the cleanup it returned, so this timer outlived the component
    // and fired `setConnection` into nothing.
    expect(vi.getTimerCount()).toBe(1)

    const consoleError = vi.spyOn(console, 'error').mockImplementation(() => undefined)
    unmount()
    expect(vi.getTimerCount()).toBe(0)

    act(() => { vi.runAllTimers() })
    expect(consoleError).not.toHaveBeenCalled()
    consoleError.mockRestore()
  })

  it('re-probes on refresh and still cleans the replacement timer up', () => {
    const { result, unmount } = renderHook(() => useBackendConnection())
    act(() => { vi.runAllTimers() })
    expect(result.current.status).toBe('offline')

    ;(window as { go?: unknown }).go = { main: {} }
    act(() => { result.current.refresh() })
    expect(result.current.status).toBe('connecting')
    expect(vi.getTimerCount()).toBe(1)

    act(() => { vi.runAllTimers() })
    expect(result.current.status).toBe('connected')

    unmount()
    expect(vi.getTimerCount()).toBe(0)
  })
})
