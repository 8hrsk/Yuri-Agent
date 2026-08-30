/** @vitest-environment jsdom */
import { act, renderHook } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { sidebarNarrowQuery, useSidebarCollapse } from './useSidebarCollapse'

type Listener = (event: MediaQueryListEvent) => void

/**
 * jsdom has no layout, so `matchMedia` is a stub that always reports "no
 * match". The rail's width rule is the whole point of these tests, so the
 * query is faked with something that can actually change.
 */
function stubMatchMedia(matches: boolean) {
  const listeners = new Set<Listener>()
  const query = {
    matches,
    media: sidebarNarrowQuery,
    addEventListener: (_type: string, listener: Listener) => { listeners.add(listener) },
    removeEventListener: (_type: string, listener: Listener) => { listeners.delete(listener) },
  }
  Object.defineProperty(window, 'matchMedia', {
    configurable: true,
    writable: true,
    value: vi.fn(() => query),
  })
  return {
    resize(next: boolean) {
      query.matches = next
      listeners.forEach((listener) => listener({ matches: next } as MediaQueryListEvent))
    },
    listenerCount: () => listeners.size,
  }
}

afterEach(() => {
  window.localStorage.clear()
  delete (window as { matchMedia?: unknown }).matchMedia
})

describe('useSidebarCollapse', () => {
  it('starts expanded and collapses on the owner’s toggle', () => {
    stubMatchMedia(false)
    const { result } = renderHook(() => useSidebarCollapse())

    expect(result.current.collapsed).toBe(false)
    act(() => result.current.toggle?.())
    expect(result.current.collapsed).toBe(true)
    act(() => result.current.toggle?.())
    expect(result.current.collapsed).toBe(false)
  })

  it('remembers the choice across a remount', () => {
    stubMatchMedia(false)
    const first = renderHook(() => useSidebarCollapse())
    act(() => first.result.current.toggle?.())
    first.unmount()

    const second = renderHook(() => useSidebarCollapse())
    expect(second.result.current.collapsed).toBe(true)
  })

  it('collapses a narrow window and withdraws the toggle it cannot honour', () => {
    const media = stubMatchMedia(true)
    const { result } = renderHook(() => useSidebarCollapse())

    expect(result.current.collapsed).toBe(true)
    // Offering a control that cannot expand the rail would be a lie about what
    // the layout can do, so there is nothing to call.
    expect(result.current.toggle).toBeUndefined()

    act(() => media.resize(false))
    expect(result.current.collapsed).toBe(false)
    expect(result.current.toggle).toBeDefined()
  })

  it('restores the owner’s collapsed choice after a narrow spell, not the window’s', () => {
    const media = stubMatchMedia(false)
    const { result } = renderHook(() => useSidebarCollapse())

    act(() => media.resize(true))
    expect(result.current.collapsed).toBe(true)
    act(() => media.resize(false))

    // The window collapsed the rail; the owner never did, so it comes back.
    expect(result.current.collapsed).toBe(false)
  })

  it('stops listening for width changes on unmount', () => {
    const media = stubMatchMedia(false)
    const { unmount } = renderHook(() => useSidebarCollapse())

    expect(media.listenerCount()).toBe(1)
    unmount()
    expect(media.listenerCount()).toBe(0)
  })

  it('falls back to an expanded rail when storage is denied', () => {
    stubMatchMedia(false)
    const getItem = vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => { throw new Error('denied') })
    const setItem = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new Error('denied') })

    const { result } = renderHook(() => useSidebarCollapse())
    expect(result.current.collapsed).toBe(false)

    // A write that throws must not take the toggle down with it.
    act(() => result.current.toggle?.())
    expect(result.current.collapsed).toBe(true)

    getItem.mockRestore()
    setItem.mockRestore()
  })
})
