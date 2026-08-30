import { useCallback, useEffect, useState } from 'react'

const storageKey = 'yuri.shell.sidebar-collapsed'

/**
 * The width below which the rail has no room for its labels. It matches the
 * `820px` breakpoint the shell stylesheet already uses for the workspace
 * padding, so the rail and the content narrow together.
 */
export const sidebarNarrowQuery = '(max-width: 820px)'

function readPreference(): boolean {
  try {
    return window.localStorage.getItem(storageKey) === 'true'
  } catch {
    // Hardened WebViews can deny localStorage. An expanded rail is the safe
    // default: nothing is hidden from the owner.
    return false
  }
}

function writePreference(collapsed: boolean): void {
  try {
    window.localStorage.setItem(storageKey, String(collapsed))
  } catch {
    // Best effort. Losing the preference must never break navigation.
  }
}

export type SidebarCollapse = {
  /** What the rail actually renders as, preference and window width combined. */
  collapsed: boolean
  /**
   * `undefined` while the window is too narrow to expand: the shell hides the
   * toggle rather than offering a state the layout cannot honour.
   */
  toggle?: () => void
}

/**
 * Owns whether the navigation rail shows labels. The owner's choice is
 * remembered across launches, and a window narrow enough to squeeze the
 * workspace collapses the rail regardless of it.
 */
export function useSidebarCollapse(): SidebarCollapse {
  const [preferred, setPreferred] = useState(readPreference)
  const [narrow, setNarrow] = useState(() => {
    if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') return false
    return window.matchMedia(sidebarNarrowQuery).matches
  })

  useEffect(() => {
    if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') return
    const query = window.matchMedia(sidebarNarrowQuery)
    const sync = (event: MediaQueryListEvent | MediaQueryList) => setNarrow(event.matches)
    sync(query)
    query.addEventListener('change', sync)
    return () => query.removeEventListener('change', sync)
  }, [])

  const toggle = useCallback(() => {
    setPreferred((current) => {
      const next = !current
      writePreference(next)
      return next
    })
  }, [])

  return { collapsed: narrow || preferred, toggle: narrow ? undefined : toggle }
}
