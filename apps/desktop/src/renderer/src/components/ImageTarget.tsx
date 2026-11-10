import { useState, type DragEvent } from 'react'
import { artifactURL, type Pick } from '../lib/api'
import { stagedCount, type Staged } from '../lib/draft'
import { droppedFrom, sortDropped } from '../lib/intake'
import { SIDE_VIEWS, viewHints, viewLabels, type SideView } from '../lib/views'
import { CloseIcon, FolderIcon, UploadIcon } from './icons'

const PASTE_KEY = window.forja.platform === 'darwin' ? '⌘V' : 'Ctrl+V'

/**
 * Where the images wait until "Generate": empty, it says how they get here; with one image it
 * shows it large, with its optional side views; with several, a tile each. The whole screen
 * takes a drop; this only shows where it lands.
 */
export function ImageTarget({
  images,
  sides,
  reader,
  over,
  reading,
  blocked,
  busy,
  onBrowse,
  onRemove,
  onReplace,
  onClear,
  onSide,
}: {
  images: Staged[]
  sides: Partial<Record<SideView, Staged>>
  /** The shape step whose model reads side views; none when it takes the front alone. */
  reader?: Pick
  /** Files are being dragged over the window. */
  over: boolean
  /** How many images are being copied into the store right now. */
  reading: number
  /** No preset can run yet, so there is nothing to stage images for. */
  blocked: boolean
  /** The staged images are being sent. */
  busy: boolean
  onBrowse: () => void
  onRemove: (id: string) => void
  onReplace: (id: string) => void
  onClear: () => void
  /** Sets a side view from a file, or clears it without one. */
  onSide: (view: SideView, path?: string) => Promise<void>
}) {
  const n = images.length
  const empty = n === 0 && reading === 0
  const only = n === 1 && reading === 0 && !images[0]!.folder ? images[0] : undefined
  const folders = images.some((s) => s.folder)
  return (
    <section className={`target brackets ${empty ? 'empty' : ''} ${blocked ? 'blocked' : ''}`} aria-label="Images to generate from" aria-busy={reading > 0}>
      {empty ? (
        <div className="target-empty">
          <UploadIcon size={20} />
          <strong className="target-title">{blocked ? 'Install models to add images' : 'Drop images or a folder here'}</strong>
          <span className="target-hint" id="target-hint">
            PNG, JPEG or WebP; a folder adds the images in it. Paste with {PASTE_KEY}.
          </span>
          <button className="btn" onClick={onBrowse} disabled={blocked} aria-describedby="target-hint">
            Browse…
          </button>
        </div>
      ) : (
        <>
          <header className="target-head">
            <div className="target-summary">
              <strong className="target-count">{reading > 0 && n === 0 ? 'Reading…' : stagedCount(images)}</strong>
              <span className="target-each">{only ? 'One asset is made from it.' : folders ? 'One asset is made from each image, in folders too.' : 'One asset is made from each.'}</span>
            </div>
            {only && (
              <button className="btn ghost sm target-add" onClick={onBrowse} disabled={busy}>
                Add images…
              </button>
            )}
            {n > 1 && (
              <button className="btn ghost sm target-clear" aria-label="Clear all images" onClick={onClear} disabled={busy}>
                Clear
              </button>
            )}
          </header>
          {only ? (
            <figure className="single enter">
              <div className="single-well">
                <Thumb key={only.ref} image={only} alt={`${only.name}, the image to generate from`} />
              </div>
              <figcaption className="single-foot">
                <span className="truncate" title={only.path ?? only.name}>
                  {only.name}
                </span>
                <button className="btn ghost sm" onClick={() => onReplace(only.id)} disabled={busy}>
                  Replace
                </button>
                <button className="btn ghost sm" onClick={() => onRemove(only.id)} disabled={busy}>
                  Remove
                </button>
              </figcaption>
            </figure>
          ) : (
            <ul className="tiles">
              {images.map((s) => (
                <li key={s.id} className="tile enter">
                  <div className={`tile-well ${s.folder ? 'folder' : ''}`}>
                    {s.folder ? (
                      <>
                        <FolderIcon size={24} />
                        <span className="tile-kind">Folder</span>
                      </>
                    ) : (
                      <Thumb image={s} alt="" />
                    )}
                  </div>
                  <span className="tile-name truncate" title={s.path ?? s.name}>
                    {s.name}
                  </span>
                  <button className="btn sm icon tile-remove" aria-label={`Remove ${s.name}`} onClick={() => onRemove(s.id)} disabled={busy}>
                    <CloseIcon />
                  </button>
                </li>
              ))}
              {Array.from({ length: reading }, (_, i) => (
                <li key={`reading-${i}`} className="tile reading" aria-hidden>
                  <div className="tile-well skeleton" />
                  <span className="tile-name skeleton" />
                </li>
              ))}
              <li className="tile-slot">
                <button className="tile-add" onClick={onBrowse} disabled={busy} aria-label="Add images">
                  <UploadIcon size={22} />
                  <span>Add images</span>
                </button>
              </li>
            </ul>
          )}
          {only && reader && (
            <div className="sides">
              <div className="sides-head">
                <strong>
                  Side views <span className="dim">(optional)</span>
                </strong>
                <span>{reader.model_name || reader.model} reads them to build the back and the sides.</span>
              </div>
              <div className="sides-slots">
                {SIDE_VIEWS.map((view) => (
                  <SideSlot key={view} view={view} picked={sides[view]} disabled={busy} onSide={onSide} />
                ))}
              </div>
            </div>
          )}
          <p className="target-tip">Drop images or folders anywhere on this screen, or paste a screenshot.</p>
        </>
      )}
      <div className={`target-over ${over && !blocked ? 'on' : ''}`} aria-hidden>
        <UploadIcon size={20} />
        <strong>Drop to add</strong>
      </div>
    </section>
  )
}

/** A staged picture; a file that has the signature of an image but cannot be drawn says so instead of showing a broken icon. */
function Thumb({ image, alt }: { image: Staged; alt: string }) {
  const [broken, setBroken] = useState(false)
  if (broken) return <span className="thumb-broken">Cannot preview this file</span>
  return <img src={artifactURL(image.ref)} alt={alt} decoding="async" onError={() => setBroken(true)} />
}

/** One other view of the subject: click or drop to set it, again to replace it. */
function SideSlot({
  view,
  picked,
  disabled,
  onSide,
}: {
  view: SideView
  picked?: Staged
  disabled: boolean
  onSide: (view: SideView, path?: string) => Promise<void>
}) {
  const [over, setOver] = useState(false)
  const [loading, setLoading] = useState(false)
  const label = viewLabels[view].toLowerCase()

  const set = async (path: string | undefined): Promise<void> => {
    if (!path) return
    setLoading(true)
    try {
      await onSide(view, path)
    } finally {
      setLoading(false)
    }
  }

  return (
    <div
      className={`side ${picked ? 'filled' : ''} ${over ? 'over' : ''}`}
      onDragOver={(e: DragEvent) => {
        // Taking the event here tells the screen this drop is for the slot, not for the target.
        e.preventDefault()
        if (!disabled) setOver(true)
      }}
      onDragLeave={() => setOver(false)}
      onDrop={(e: DragEvent) => {
        e.preventDefault()
        setOver(false)
        if (!disabled) void set(sortDropped(droppedFrom(e.dataTransfer, window.forja.pathForFile)).images[0])
      }}
    >
      <button
        className="side-pick"
        disabled={disabled || loading}
        title={viewHints[view]}
        aria-label={picked ? `Replace the ${label} view (${picked.name})` : `Add the ${label} view: ${viewHints[view].toLowerCase()}`}
        onClick={async () => set((await window.forja.pickImages())[0])}
      >
        {loading ? (
          <span className="side-well skeleton" aria-hidden />
        ) : picked ? (
          <span className="side-well">
            <Thumb key={picked.ref} image={picked} alt="" />
          </span>
        ) : (
          <span className="side-well">
            <UploadIcon />
          </span>
        )}
        <span className="side-name">{viewLabels[view]}</span>
        <span className="side-hint">{picked ? picked.name : viewHints[view]}</span>
      </button>
      {picked && (
        <button className="btn ghost sm icon side-remove" aria-label={`Remove the ${label} view`} disabled={disabled} onClick={() => void onSide(view)}>
          <CloseIcon />
        </button>
      )}
    </div>
  )
}
