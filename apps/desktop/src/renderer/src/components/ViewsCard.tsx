import { useState, type CSSProperties, type DragEvent } from 'react'
import { artifactURL, type Pick } from '../lib/api'
import { SIDE_VIEWS, viewHints, viewLabels, type SideView } from '../lib/views'

export const IMAGE = /\.(png|jpe?g|webp)$/i

/** An image the user picked, already in the core's store. */
export interface Picked {
  path: string
  ref: string
}

export interface Draft {
  front: Picked
  views: Partial<Record<SideView, Picked>>
}

const fileName = (path: string): string => path.split(/[\\/]/).pop() ?? path

/**
 * One subject's views before they become a job: the front that was dropped, and a slot for
 * each other view the shape model reads. Shown in place of the drop zone.
 */
export function ViewsCard({
  draft,
  reader,
  busy,
  onAdd,
  onRemove,
  onGenerate,
  onCancel,
}: {
  draft: Draft
  /** The step whose model reads the other views; none when the chosen model takes the front alone. */
  reader?: Pick
  busy: boolean
  onAdd: (view: SideView, path: string) => Promise<void>
  onRemove: (view: SideView) => void
  onGenerate: () => void
  onCancel: () => void
}) {
  const [loading, setLoading] = useState<SideView | null>(null)
  const added = SIDE_VIEWS.filter((v) => draft.views[v]).length

  const add = async (view: SideView, paths: string[]): Promise<void> => {
    const path = paths.find((p) => IMAGE.test(p))
    if (!path) return
    setLoading(view)
    try {
      await onAdd(view, path)
    } finally {
      setLoading(null)
    }
  }

  return (
    <section className="views-card enter" aria-label="Views of the subject">
      <span className="frame" aria-hidden />
      <div className="views-head">
        <h3 className="views-title">Add the other views</h3>
        <p className="views-sub">
          {reader
            ? `${reader.model_name || reader.model} builds the shape from every view of the same subject. Add the ones you have; the front alone works too.`
            : 'The shape model chosen now reads the front alone, so other views would not be used.'}
        </p>
      </div>
      <div className="views-grid">
        <figure className="view-slot filled enter" style={{ '--i': 0 } as CSSProperties}>
          <img src={artifactURL(draft.front.ref)} alt="The front view" decoding="async" />
          <figcaption className="view-foot">
            <span className="view-name">{viewLabels.front}</span>
            <span className="view-file" title={draft.front.path}>
              {fileName(draft.front.path)}
            </span>
          </figcaption>
        </figure>
        {SIDE_VIEWS.map((view, i) => (
          <Slot
            key={view}
            view={view}
            index={i + 1}
            picked={draft.views[view]}
            loading={loading === view}
            disabled={!reader || busy}
            onPick={async () => add(view, await window.forja.pickImages())}
            onDropFiles={(paths) => add(view, paths)}
            onRemove={() => onRemove(view)}
          />
        ))}
      </div>
      <div className="views-actions">
        <button className="btn ghost" onClick={onCancel} disabled={busy}>
          Cancel
        </button>
        <button className="btn primary" onClick={onGenerate} disabled={busy || loading !== null}>
          {busy ? 'Queuing…' : added ? `Generate from ${added + 1} views` : 'Generate from the front'}
        </button>
      </div>
    </section>
  )
}

function Slot({
  view,
  index,
  picked,
  loading,
  disabled,
  onPick,
  onDropFiles,
  onRemove,
}: {
  view: SideView
  index: number
  picked?: Picked
  loading: boolean
  disabled: boolean
  onPick: () => void
  onDropFiles: (paths: string[]) => void
  onRemove: () => void
}) {
  const [over, setOver] = useState(false)
  const style = { '--i': index } as CSSProperties
  if (picked) {
    return (
      <figure className="view-slot filled enter" style={style}>
        <img key={picked.ref} src={artifactURL(picked.ref)} alt={`The ${viewLabels[view].toLowerCase()} view`} decoding="async" />
        <figcaption className="view-foot">
          <span className="view-name">{viewLabels[view]}</span>
          <button className="view-remove" onClick={onRemove} aria-label={`Remove the ${viewLabels[view].toLowerCase()} view`}>
            Remove
          </button>
        </figcaption>
      </figure>
    )
  }
  return (
    <button
      className={`view-slot empty enter ${over ? 'over' : ''} ${loading ? 'loading' : ''}`}
      style={style}
      disabled={disabled || loading}
      onClick={onPick}
      onDragOver={(e: DragEvent) => {
        e.preventDefault()
        if (!disabled) setOver(true)
      }}
      onDragLeave={() => setOver(false)}
      onDrop={(e: DragEvent) => {
        e.preventDefault()
        setOver(false)
        if (!disabled) onDropFiles([...e.dataTransfer.files].map((f) => window.forja.pathForFile(f)))
      }}
    >
      {loading && <span className="skeleton view-loading" aria-hidden />}
      <span className="view-empty">
        <span className="view-name">{viewLabels[view]}</span>
        <span className="view-hint">{viewHints[view]}</span>
        <span className="view-cta">Drop or click</span>
      </span>
    </button>
  )
}
