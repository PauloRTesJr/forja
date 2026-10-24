import { useEffect, useRef } from 'react'
import { Trash } from './icons'

/** Asks before assets leave the library: what goes, and that exported files stay. */
export function ConfirmDelete({ names, onConfirm, onClose }: { names: string[]; onConfirm: () => void; onClose: () => void }) {
  const cancel = useRef<HTMLButtonElement>(null)
  useEffect(() => {
    // Cancel has the focus, so a stray Enter after the Delete key deletes nothing.
    cancel.current?.focus()
    const onKey = (e: KeyboardEvent): void => {
      if (e.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose])
  const one = names.length === 1
  return (
    <div className="dialog-backdrop" onClick={onClose}>
      <div className="dialog narrow" role="alertdialog" aria-modal aria-labelledby="confirm-delete" onClick={(e) => e.stopPropagation()}>
        <h2 id="confirm-delete">{one ? `Delete ${names[0]}?` : `Delete ${names.length} assets?`}</h2>
        <p className="muted">
          {one ? 'It leaves' : 'They leave'} the library and the queue. Files exported into projects or saved elsewhere stay where
          they are.
        </p>
        <div className="row dialog-actions">
          <button className="btn ghost" ref={cancel} onClick={onClose}>
            Cancel
          </button>
          <button className="btn danger" onClick={onConfirm}>
            <Trash />
            Delete
          </button>
        </div>
      </div>
    </div>
  )
}
