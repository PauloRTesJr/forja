import { Dialog, DialogActions } from './Dialog'
import { DeleteIcon } from './icons'

/** How many names the question lists before it counts the rest. */
const LISTED = 5

/** Asks before assets leave the library: which ones go, and that exported files stay. */
export function ConfirmDelete({ names, onConfirm, onClose }: { names: string[]; onConfirm: () => void; onClose: () => void }) {
  const one = names.length === 1
  const rest = names.length - LISTED
  return (
    <Dialog title={one ? `Delete ${names[0]}?` : `Delete ${names.length} assets?`} size="narrow" alert onClose={onClose}>
      {!one && (
        <ul className="confirm-names">
          {names.slice(0, LISTED).map((name, i) => (
            <li key={i} className="truncate">
              {name}
            </li>
          ))}
          {rest > 0 && <li className="dim">and {rest} more</li>}
        </ul>
      )}
      <p className="muted">
        This removes {one ? 'it' : 'them'} from the library and the queue. Files you exported are not touched.
      </p>
      <DialogActions>
        {/* Cancel has the focus, so a stray Enter after the Delete key deletes nothing. */}
        <button className="btn ghost" autoFocus onClick={onClose}>
          Cancel
        </button>
        <button className="btn danger" onClick={onConfirm}>
          <DeleteIcon />
          Delete
        </button>
      </DialogActions>
    </Dialog>
  )
}
