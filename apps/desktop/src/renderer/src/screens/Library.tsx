import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useCallback, useDeferredValue, useEffect, useLayoutEffect, useMemo, useRef, useState, useSyncExternalStore, type RefObject } from 'react'
import { ConfirmDelete } from '../components/ConfirmDelete'
import { Empty } from '../components/Empty'
import { ExportMenu } from '../components/ExportMenu'
import { Check, Search as SearchIcon, Sparkle, Trash } from '../components/icons'
import { ScreenHead } from '../components/ScreenHead'
import { api, artifactURL, deleteAsset, type Asset } from '../lib/api'
import { keys } from '../lib/events'
import { count, relative } from '../lib/format'
import { dragOut, readyDrag } from '../lib/drag'
import { transition, useNavigate } from '../lib/nav'
import { clickSelect, dragIds } from '../lib/selection'
import { useToast } from '../lib/toast'
import { ensureThumbnail, subscribeThumbnailErrors, thumbnailError, thumbnailErrorsVersion } from '../viewer/thumbnails'

const LEAVE_MS = 200

export function Library() {
  const [q, setQ] = useState('')
  const query = useDeferredValue(q.trim())
  const qc = useQueryClient()
  const toast = useToast()
  useSyncExternalStore(subscribeThumbnailErrors, thumbnailErrorsVersion)
  const assets = useQuery({
    queryKey: keys.assets(query),
    queryFn: () => api<Asset[]>('GET', `/v1/assets?limit=200&q=${encodeURIComponent(query)}`),
    placeholderData: (prev) => prev,
  })
  const list = useMemo(() => assets.data ?? [], [assets.data])
  const [selected, setSelected] = useState<ReadonlySet<string>>(new Set())
  const [leaving, setLeaving] = useState<ReadonlySet<string>>(new Set())
  const [confirming, setConfirming] = useState(false)
  const anchor = useRef<string | null>(null)
  const grid = useRef<HTMLDivElement>(null)
  // Only what the search shows counts: nothing hidden is exported or deleted by surprise.
  const chosen = useMemo(() => list.filter((a) => selected.has(a.id)), [list, selected])
  const selecting = chosen.length > 0
  useFlip(grid, list.map((a) => a.id).join())

  useEffect(() => {
    for (const a of assets.data ?? []) {
      ensureThumbnail(a, () => void qc.invalidateQueries({ queryKey: ['assets'] }))
    }
  }, [assets.data, qc])

  // Dragging a selected card carries the whole selection: have every file ready.
  useEffect(() => {
    for (const a of chosen) readyDrag(a)
  }, [chosen])

  const toggle = (id: string, shift: boolean): void => {
    // Read before it moves: React runs the update later.
    const from = anchor.current
    const ids = list.map((a) => a.id)
    setSelected((sel) => clickSelect(sel, ids, from, id, shift))
    anchor.current = id
  }
  const clear = useCallback(() => setSelected(new Set()), [])

  const remove = async (): Promise<void> => {
    const ids = chosen.map((a) => a.id)
    setConfirming(false)
    // The cards fade first; the list then closes the gaps as the core confirms each one.
    setLeaving(new Set(ids))
    await new Promise((r) => setTimeout(r, LEAVE_MS))
    const results = await Promise.allSettled(ids.map(deleteAsset))
    const failed = ids.filter((_, i) => results[i]!.status === 'rejected')
    const gone = new Set(ids.filter((id) => !failed.includes(id)))
    qc.setQueriesData<Asset[]>({ queryKey: ['assets'] }, (l) => l?.filter((a) => !gone.has(a.id)))
    setLeaving(new Set())
    setSelected(new Set(failed))
    const first = results.find((r): r is PromiseRejectedResult => r.status === 'rejected')
    if (first) toast(`${failed.length} of ${ids.length} could not be deleted: ${(first.reason as Error).message}`, true)
  }

  useEffect(() => {
    const onKey = (e: KeyboardEvent): void => {
      const t = e.target as HTMLElement
      if (confirming || t.closest('input, textarea, select, [role="menu"], [role="dialog"], [role="alertdialog"]')) return
      if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 'a') {
        e.preventDefault()
        setSelected(new Set(list.map((a) => a.id)))
      } else if (e.key === 'Escape') {
        clear()
      } else if ((e.key === 'Delete' || e.key === 'Backspace') && selecting) {
        e.preventDefault()
        setConfirming(true)
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [confirming, list, selecting, clear])

  const shown = !assets.data ? ' ' : assets.data.length ? `${assets.data.length} assets · drag them into Blender, Unity or a folder` : '0 assets'
  return (
    <>
      <div className="screen library">
        <ScreenHead eyebrow={['Arcanum III', 'The Grimoire']} title="Library" sub={shown}>
          <label className="search">
            <SearchIcon />
            <span className="sr-only">Search assets</span>
            <input type="search" placeholder="Search by name or preset" value={q} onChange={(e) => setQ(e.target.value)} />
          </label>
        </ScreenHead>
        {assets.data?.length === 0 && <Empty title={query ? 'No matches' : 'No assets yet'}>Finished jobs land here.</Empty>}
        <div className={`grid ${selecting ? 'selecting' : ''}`} ref={grid}>
          {assets.isPending && [0, 1, 2, 3, 4, 5].map((i) => <div key={i} className="skeleton" style={{ aspectRatio: '1 / 1.2' }} />)}
          {list.map((a, i) => (
            <AssetCard
              key={a.id}
              asset={a}
              index={i}
              selected={selected.has(a.id)}
              leaving={leaving.has(a.id)}
              selecting={selecting}
              onToggle={(shift) => toggle(a.id, shift)}
              onDragOut={(e) =>
                dragOut(
                  e,
                  dragIds(
                    list.map((x) => x.id),
                    selected,
                    a.id,
                  ),
                )
              }
            />
          ))}
        </div>
      </div>
      <div className="selection-bar" data-open={selecting} inert={!selecting} role="toolbar" aria-label="Selected assets">
        <span className="selection-count">{chosen.length === 1 ? '1 asset selected' : `${chosen.length} assets selected`}</span>
        {chosen.length < list.length && (
          <button className="btn ghost" onClick={() => setSelected(new Set(list.map((a) => a.id)))}>
            Select all
          </button>
        )}
        <ExportMenu assets={chosen} up />
        <button className="btn danger" onClick={() => setConfirming(true)}>
          <Trash />
          Delete
        </button>
        <button className="btn ghost" onClick={clear}>
          Done
        </button>
      </div>
      {confirming && <ConfirmDelete names={chosen.map((a) => a.name)} onConfirm={() => void remove()} onClose={() => setConfirming(false)} />}
    </>
  )
}

/**
 * Glides cards to their new places when the list changes (a deletion, a new asset, a search)
 * instead of letting them jump. Positions are kept relative to the grid, and re-read when it
 * resizes, so scrolling and resizing the window never animate.
 */
function useFlip(grid: RefObject<HTMLDivElement | null>, ids: string): void {
  const last = useRef(new Map<string, { x: number; y: number }>())

  useLayoutEffect(() => {
    const now = positions(grid.current)
    if (!matchMedia('(prefers-reduced-motion: reduce)').matches) {
      for (const [id, at] of now) {
        const before = last.current.get(id)
        if (!before) continue
        const dx = before.x - at.x
        const dy = before.y - at.y
        if (Math.abs(dx) < 1 && Math.abs(dy) < 1) continue
        grid.current
          ?.querySelector<HTMLElement>(`[data-id="${id}"]`)
          ?.animate([{ transform: `translate(${dx}px, ${dy}px)` }, { transform: 'none' }], { duration: 280, easing: 'cubic-bezier(0.2, 0.8, 0.2, 1)' })
      }
    }
    last.current = now
  }, [ids, grid])

  useEffect(() => {
    const el = grid.current
    if (!el) return
    const watch = new ResizeObserver(() => {
      last.current = positions(el)
    })
    watch.observe(el)
    return () => watch.disconnect()
  }, [grid])
}

/** Where each card sits, relative to the grid. */
function positions(grid: HTMLElement | null): Map<string, { x: number; y: number }> {
  const out = new Map<string, { x: number; y: number }>()
  if (!grid) return out
  const origin = grid.getBoundingClientRect()
  for (const card of grid.querySelectorAll<HTMLElement>('[data-id]')) {
    const r = card.getBoundingClientRect()
    out.set(card.dataset.id!, { x: r.left - origin.left, y: r.top - origin.top })
  }
  return out
}

function AssetCard({
  asset,
  index,
  selected,
  leaving,
  selecting,
  onToggle,
  onDragOut,
}: {
  asset: Asset
  index: number
  selected: boolean
  leaving: boolean
  selecting: boolean
  onToggle: (shift: boolean) => void
  onDragOut: (e: React.DragEvent) => void
}) {
  const navigate = useNavigate()
  const tris = asset.stats?.triangles
  const error = asset.thumbnail ? undefined : thumbnailError(asset)
  return (
    <div
      className={`card enter ${selected ? 'selected' : ''} ${leaving ? 'leaving' : ''}`}
      style={{ '--i': Math.min(index, 12) } as React.CSSProperties}
      data-id={asset.id}
    >
      <button
        className="card-main"
        draggable
        onPointerEnter={() => readyDrag(asset)}
        onDragStart={onDragOut}
        aria-label={selecting ? `${selected ? 'Deselect' : 'Select'} ${asset.name}` : `Open ${asset.name}`}
        onClick={(e) => {
          // Once something is selected, a click selects; Ctrl, Cmd or Shift start a selection.
          if (selecting || e.ctrlKey || e.metaKey || e.shiftKey) onToggle(e.shiftKey)
          else transition(() => navigate({ screen: 'asset', id: asset.id }))
        }}
      >
        <div className="card-img">
          <Sparkle size={11} className="dust a" />
          <Sparkle size={8} className="dust b" />
          {asset.thumbnail ? (
            <img src={artifactURL(asset.thumbnail, 'image/webp')} alt="" decoding="async" loading="lazy" draggable={false} />
          ) : error ? (
            <div className="card-failed" title={error}>
              Could not load this model
            </div>
          ) : (
            <div className="skeleton" style={{ width: '60%', height: '60%', borderRadius: 12 }} />
          )}
        </div>
        <div className="card-body">
          <strong>{asset.name}</strong>
          <span className="muted">
            {asset.preset}
            {typeof tris === 'number' && ` · ${count(tris)} tris`} · {relative(asset.created_at)}
          </span>
        </div>
      </button>
      <button
        className="card-check"
        role="checkbox"
        aria-checked={selected}
        aria-label={`Select ${asset.name}`}
        onClick={(e) => onToggle(e.shiftKey)}
      >
        <Check />
      </button>
    </div>
  )
}
