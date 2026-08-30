import { useEffect, useRef } from 'react'
import type { ReactNode, RefObject } from 'react'

const focusableSelector = [
  'a[href]',
  'button:not([disabled])',
  'input:not([disabled])',
  'select:not([disabled])',
  'textarea:not([disabled])',
  '[tabindex]:not([tabindex="-1"])',
].join(',')

function focusableWithin(container: HTMLElement): HTMLElement[] {
  return Array.from(container.querySelectorAll<HTMLElement>(focusableSelector))
    .filter((element) => element.getAttribute('aria-hidden') !== 'true')
}

/**
 * The modal behaviour shared by every dialog in the app.
 *
 * It owns the three guarantees a surrounding view cannot provide: focus never
 * leaves the dialog while it is open, Escape and the backdrop always offer a
 * way out, and the rest of the view is hidden from assistive technology and
 * pointer input for as long as the modal is up. Deciding *what* Escape means
 * belongs to the caller — denying a tool call and abandoning a consent form
 * are different acts — so the shell only reports the gesture.
 *
 * Extracted from ApprovalDialog when the plugin consent dialog needed the same
 * behaviour; a second copy would have been a second set of accessibility bugs.
 */
export function ModalShell({
  backdropClassName,
  children,
  className,
  describedBy,
  initialFocusRef,
  labelledBy,
  onEscape,
}: {
  backdropClassName: string
  children: ReactNode
  className: string
  describedBy?: string
  /** Receives focus on open; the caller points it at the safe default. */
  initialFocusRef?: RefObject<HTMLElement | null>
  labelledBy: string
  /** Escape or a backdrop click. The caller decides what that means. */
  onEscape: () => void
}) {
  const dialogRef = useRef<HTMLElement>(null)
  const restoreFocusRef = useRef<Element | null>(null)

  // Declared before the focus effect so that its cleanup lifts `inert` before
  // focus is handed back to the trigger.
  useEffect(() => {
    const backdrop = dialogRef.current?.parentElement
    const parent = backdrop?.parentElement
    if (!backdrop || !parent) return
    const marked = Array.from(parent.children).filter((child): child is HTMLElement => child !== backdrop && child instanceof HTMLElement)
    marked.forEach((element) => {
      element.setAttribute('inert', '')
      element.setAttribute('aria-hidden', 'true')
    })
    return () => {
      marked.forEach((element) => {
        element.removeAttribute('inert')
        element.removeAttribute('aria-hidden')
      })
    }
  }, [])

  useEffect(() => {
    restoreFocusRef.current = document.activeElement
    initialFocusRef?.current?.focus()
    return () => {
      const previous = restoreFocusRef.current
      if (previous instanceof HTMLElement && previous.isConnected) previous.focus()
    }
    // The ref identity is stable; re-running this would steal focus mid-edit.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        event.preventDefault()
        onEscape()
        return
      }
      if (event.key !== 'Tab') return
      const container = dialogRef.current
      if (!container) return
      const focusable = focusableWithin(container)
      if (focusable.length === 0) {
        event.preventDefault()
        return
      }
      const first = focusable[0]
      const last = focusable[focusable.length - 1]
      const active = document.activeElement
      if (!(active instanceof HTMLElement) || !container.contains(active)) {
        event.preventDefault()
        ;(event.shiftKey ? last : first).focus()
        return
      }
      if (event.shiftKey && active === first) {
        event.preventDefault()
        last.focus()
        return
      }
      if (!event.shiftKey && active === last) {
        event.preventDefault()
        first.focus()
      }
    }
    document.addEventListener('keydown', onKeyDown, true)
    return () => document.removeEventListener('keydown', onKeyDown, true)
  }, [onEscape])

  return (
    <div
      className={backdropClassName}
      onClick={(event) => {
        if (event.target === event.currentTarget) onEscape()
      }}
    >
      <section
        aria-describedby={describedBy}
        aria-labelledby={labelledBy}
        aria-modal="true"
        className={className}
        ref={dialogRef}
        role="dialog"
      >
        {children}
      </section>
    </div>
  )
}
