import { useEffect, useEffectEvent, useId, useRef, useState, type ReactNode } from 'react'
import { createPortal } from 'react-dom'

const FOCUSABLE = 'a[href], button:not(:disabled), input:not(:disabled), select:not(:disabled), textarea:not(:disabled), [tabindex]:not([tabindex="-1"])'

/**
 * The one modal dialog: a scrim and a panel with a title. It closes on Escape and on a click on
 * the scrim, keeps Tab inside itself, and gives the focus back to what opened it. Pass
 * `keepOpen` while closing would lose typed work: the scrim then does nothing and only the
 * dialog's own buttons (and Escape) close it.
 */
export function Dialog({
  title,
  size,
  alert,
  keepOpen,
  onClose,
  children,
}: {
  title: string
  size?: 'narrow' | 'wide'
  /** A question that must be answered, such as a deletion. */
  alert?: boolean
  keepOpen?: boolean
  onClose: () => void
  children: ReactNode
}) {
  const panel = useRef<HTMLDivElement>(null)
  const heading = useId()
  const close = useEffectEvent(onClose)
  // Read while rendering: by the time effects run, an autoFocus field inside has taken the focus.
  const [opener] = useState(() => document.activeElement as HTMLElement | null)

  useEffect(() => {
    const el = panel.current!
    // A field or button marked autoFocus already has the focus; otherwise the panel takes it.
    if (!el.contains(document.activeElement)) el.focus()
    const onKey = (e: KeyboardEvent): void => {
      if (e.key === 'Escape') {
        e.stopPropagation()
        close()
        return
      }
      if (e.key !== 'Tab') return
      const items = [...el.querySelectorAll<HTMLElement>(FOCUSABLE)]
      if (items.length === 0) return e.preventDefault()
      const first = items[0]!
      const last = items.at(-1)!
      const at = document.activeElement
      if (e.shiftKey && (at === first || at === el)) {
        e.preventDefault()
        last.focus()
      } else if (!e.shiftKey && at === last) {
        e.preventDefault()
        first.focus()
      } else if (!el.contains(at)) {
        e.preventDefault()
        first.focus()
      }
    }
    window.addEventListener('keydown', onKey, true)
    return () => {
      window.removeEventListener('keydown', onKey, true)
      opener?.focus?.()
    }
  }, [opener])

  // In the body, so no ancestor (a width cap, a transform mid-animation) can move or clip it.
  return createPortal(
    <div
      className="dialog-backdrop"
      onMouseDown={(e) => {
        // Only a press that starts on the scrim: a drag that ends there (selecting text) does not close.
        if (e.target === e.currentTarget && !keepOpen) onClose()
      }}
    >
      <div className={`dialog ${size ?? ''}`} role={alert ? 'alertdialog' : 'dialog'} aria-modal aria-labelledby={heading} tabIndex={-1} ref={panel}>
        <h2 id={heading}>{title}</h2>
        {children}
      </div>
    </div>,
    document.body,
  )
}

/** The dialog's buttons: right-aligned, the primary one last. */
export function DialogActions({ children }: { children: ReactNode }) {
  return <div className="dialog-actions">{children}</div>
}
