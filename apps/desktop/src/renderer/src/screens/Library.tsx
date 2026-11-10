import { keepPreviousData, useInfiniteQuery, useQuery, useQueryClient } from '@tanstack/react-query'
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, useSyncExternalStore, type CSSProperties, type KeyboardEvent, type RefObject } from 'react'
import { ConfirmDelete } from '../components/ConfirmDelete'
import { ErrorBlock } from '../components/Controls'
import { Empty } from '../components/Empty'
import { ExportMenu } from '../components/ExportMenu'
import { AlertIcon, CheckIcon, CloseIcon, DeleteIcon, LibraryIcon, NoResultsIcon, SearchIcon } from '../components/icons'
import { ScreenHead } from '../components/ScreenHead'
import { artifactURL, deleteAsset, fetchPresets, rigOf, type Asset } from '../lib/api'
import {
  assetCountKey,
  assetListKey,
  countLabel,
  fetchAssetCount,
  fetchAssetPage,
  flatten,
  nextOffset,
  SORTS,
  totalOf,
  withAsset,
  withoutAssets,
  type AssetSort,
} from '../lib/assets'
import { keys } from '../lib/events'
import { dragOut, readyDrag, readyDrags } from '../lib/drag'
import { count, relative } from '../lib/format'
import { gridMetrics, gridWindow, moveInGrid, scrollToShow, type GridMetrics } from '../lib/gridWindow'
import { presetName } from '../lib/jobs'
import { libraryView, useLibraryView } from '../lib/libraryView'
import { transition, useNavigate } from '../lib/nav'
import { clickSelect, dragIds, rangeBetween } from '../lib/selection'
import { useToast } from '../lib/toast'
import { ensureThumbnail, subscribeThumbnailErrors, thumbnailError, thumbnailErrorsVersion, verifyThumbnail } from '../viewer/thumbnails'

const LEAVE_MS = 200
const SEARCH_MS = 200
/** A card's name and caption, and the gap above them. */
const CARD_BELOW = 44
const MIN_CARD = 168
const SKELETONS = 12

/** Where the grid starts, measured from the top of the scrolled content. */
function topOf(scroller: HTMLElement | null, grid: HTMLElement | null): number {
  return scroller && grid ? grid.getBoundingClientRect().top - scroller.getBoundingClientRect().top + scroller.scrollTop : 0
}

const gapFor = (width: number): number => (width > 1500 ? 16 : 12)

function useDebounced<T>(value: T, ms: number): T {
  const [out, setOut] = useState(value)
  useEffect(() => {
    const t = setTimeout(() => setOut(value), ms)
    return () => clearTimeout(t)
  }, [value, ms])
  return out
}

export function Library() {
  const view = useLibraryView()
  const query = useDebounced(view.q.trim(), SEARCH_MS)
  const qc = useQueryClient()
  const toast = useToast()
  const navigate = useNavigate()
  useSyncExternalStore(subscribeThumbnailErrors, thumbnailErrorsVersion)
  const presets = useQuery({ queryKey: keys.presets, queryFn: () => fetchPresets() })

  const assets = useInfiniteQuery({
    queryKey: assetListKey(query, view.sort),
    queryFn: ({ pageParam, signal }) => fetchAssetPage(query, view.sort, pageParam, signal),
    initialPageParam: 0,
    getNextPageParam: (_last, pages) => nextOffset(pages),
    placeholderData: keepPreviousData,
  })
  const list = useMemo(() => flatten(assets.data), [assets.data])
  const total = totalOf(assets.data)
  // "8 of 14" needs the 14 while a search shows 8.
  const hasQuery = query !== ''
  const everything = useQuery({
    queryKey: assetCountKey,
    queryFn: ({ signal }) => fetchAssetCount(signal),
    enabled: hasQuery,
    placeholderData: keepPreviousData,
  })

  const { selected, anchor, active } = view
  const [leaving, setLeaving] = useState<ReadonlySet<string>>(new Set())
  const [confirming, setConfirming] = useState(false)
  const scroller = useRef<HTMLDivElement>(null)
  const sizer = useRef<HTMLDivElement>(null)
  const bar = useRef<HTMLDivElement>(null)
  const focusAfter = useRef<string | null>(null)
  // Only what the search shows counts: nothing hidden is exported or deleted by surprise.
  const chosen = useMemo(() => list.filter((a) => selected.has(a.id)), [list, selected])
  const selecting = chosen.length > 0

  // The grid's size and where it is scrolled to decide which cards exist at all.
  const [width, setWidth] = useState(0)
  const [scroll, setScroll] = useState({ top: 0, viewport: 800 })
  const metrics = useMemo<GridMetrics>(() => gridMetrics(width, MIN_CARD, gapFor(width), CARD_BELOW), [width])
  const win = useMemo(() => gridWindow(list.length, metrics, scroll.top, scroll.viewport), [list.length, metrics, scroll])
  const metricsRef = useRef(metrics)
  const countRef = useRef(list.length)
  useEffect(() => {
    metricsRef.current = metrics
    countRef.current = list.length
  })
  const gridTop = (): number => topOf(scroller.current, sizer.current)
  const measure = useCallback((): void => {
    const s = scroller.current
    if (!s) return
    const top = s.scrollTop - topOf(s, sizer.current)
    setScroll((prev) => {
      if (prev.viewport === s.clientHeight) {
        const a = gridWindow(countRef.current, metricsRef.current, prev.top, prev.viewport)
        const b = gridWindow(countRef.current, metricsRef.current, top, s.clientHeight)
        if (a.from === b.from && a.to === b.to) return prev
      }
      return { top, viewport: s.clientHeight }
    })
  }, [])

  useLayoutEffect(() => {
    const el = sizer.current
    if (!el) return
    setWidth(el.clientWidth)
    const watch = new ResizeObserver(() => {
      setWidth(el.clientWidth)
      measure()
    })
    watch.observe(el)
    if (scroller.current) watch.observe(scroller.current)
    return () => watch.disconnect()
  }, [measure])

  // Back from an asset's page: the scroll offset it was left at, and the card that was opened.
  const restored = useRef(false)
  const ready = width > 0 && list.length > 0
  useLayoutEffect(() => {
    const s = scroller.current
    if (!ready || restored.current || !s) return
    restored.current = true
    s.scrollTop = libraryView.scrollTop()
    if (bar.current) bar.current.dataset.stuck = String(s.scrollTop > 0)
    measure()
    const id = libraryView.returnedFrom()
    const at = id ? list.findIndex((a) => a.id === id) : -1
    if (id && at >= 0) {
      const to = scrollToShow(at, metrics, s.scrollTop - topOf(s, sizer.current), s.clientHeight)
      if (to !== undefined) s.scrollTop = to + topOf(s, sizer.current)
      measure()
      libraryView.set({ active: id })
      focusAfter.current = id
    }
  }, [ready, list, metrics, measure])

  // A scroll that ends further down than what was loaded asks for the next page.
  useEffect(() => {
    if (win.to >= list.length - metrics.columns * 2 && assets.hasNextPage && !assets.isFetchingNextPage && !assets.isPlaceholderData) {
      void assets.fetchNextPage()
    }
  }, [win.to, list.length, metrics.columns, assets])

  const shown = useMemo(() => list.slice(win.from, win.to), [list, win.from, win.to])
  useGlide(sizer, shown, win.from, metrics)

  useEffect(() => {
    const upsert = (u: Asset): void => {
      qc.setQueriesData({ queryKey: ['assets'] }, (cache) => withAsset(cache, u))
    }
    for (const a of shown) {
      ensureThumbnail(a, upsert)
      verifyThumbnail(a, upsert)
    }
  }, [shown, qc])

  useLayoutEffect(() => {
    const id = focusAfter.current
    if (!id) return
    const el = sizer.current?.querySelector<HTMLElement>(`[data-id="${CSS.escape(id)}"] .card-main`)
    if (!el) return
    focusAfter.current = null
    el.focus({ preventScroll: true })
  })

  const onScroll = (): void => {
    const s = scroller.current
    if (!s) return
    libraryView.rememberScroll(s.scrollTop)
    // A hairline under the toolbar once content scrolls beneath it.
    if (bar.current) bar.current.dataset.stuck = String(s.scrollTop > 0)
    measure()
  }

  const select = (next: ReadonlySet<string>, from: string | null = anchor): void => libraryView.set({ selected: next, anchor: from })
  const toggle = (id: string, shift: boolean): void => {
    // The store's own state, not this render's: two clicks may land before it renders again.
    const now = libraryView.get()
    select(clickSelect(now.selected, list.map((a) => a.id), now.anchor, id, shift), shift ? now.anchor : id)
  }
  const clear = useCallback(() => libraryView.set({ selected: new Set() }), [])
  const open = (id: string): void => {
    libraryView.open(id)
    transition(() => navigate({ screen: 'asset', id }))
  }

  const remove = async (targets: Asset[]): Promise<void> => {
    const ids = targets.map((a) => a.id)
    const neighbour = list.find((a, i) => i >= list.findIndex((x) => x.id === ids[0]) && !ids.includes(a.id)) ?? list.findLast((a) => !ids.includes(a.id))
    setConfirming(false)
    // The cards fade first; the list then closes the gaps as the core confirms each one.
    setLeaving(new Set(ids))
    await new Promise((r) => setTimeout(r, LEAVE_MS))
    const results = await Promise.allSettled(ids.map(deleteAsset))
    const failed = ids.filter((_, i) => results[i]!.status === 'rejected')
    const gone = new Set(ids.filter((id) => !failed.includes(id)))
    qc.setQueriesData({ queryKey: ['assets'] }, (cache) => withoutAssets(cache, gone))
    void qc.invalidateQueries({ queryKey: assetCountKey })
    setLeaving(new Set())
    libraryView.set({ selected: new Set(failed), active: neighbour?.id ?? null })
    if (neighbour) focusAfter.current = neighbour.id
    const first = results.find((r): r is PromiseRejectedResult => r.status === 'rejected')
    if (first) toast(`${failed.length} of ${ids.length} could not be deleted: ${(first.reason as Error).message}`, true)
    if (gone.size > 0) toast(gone.size === 1 ? `Deleted ${targets.find((t) => gone.has(t.id))!.name}` : `Deleted ${gone.size} assets`)
  }

  const askDelete = (): void => {
    if (!selecting) {
      const focused = list.find((a) => a.id === active)
      if (!focused) return
      select(new Set([focused.id]), focused.id)
    }
    setConfirming(true)
  }

  useEffect(() => {
    const onKey = (e: globalThis.KeyboardEvent): void => {
      const t = e.target as HTMLElement
      if (confirming || t.closest('input, textarea, select, [role="menu"], [role="dialog"], [role="alertdialog"]')) return
      if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 'a') {
        e.preventDefault()
        libraryView.set({ selected: new Set(list.map((a) => a.id)) })
      } else if (e.key === 'Escape') {
        clear()
      } else if (e.key === 'Delete' && list.length > 0 && (selecting || t.closest('.card'))) {
        e.preventDefault()
        askDelete()
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  })

  const onGridKey = (e: KeyboardEvent): void => {
    const card = (e.target as HTMLElement).closest<HTMLElement>('.card')
    if (!card || e.ctrlKey || e.metaKey || e.altKey) return
    const at = list.findIndex((a) => a.id === card.dataset.id)
    if (at < 0) return
    if (e.key === ' ') {
      e.preventDefault()
      toggle(list[at]!.id, e.shiftKey)
      return
    }
    if (!['ArrowLeft', 'ArrowRight', 'ArrowUp', 'ArrowDown', 'Home', 'End'].includes(e.key)) return
    e.preventDefault()
    const to = moveInGrid(at, e.key, list.length, metrics.columns)
    const target = list[to]!
    if (e.shiftKey) {
      select(new Set([...selected, ...rangeBetween(list.map((a) => a.id), anchor ?? list[at]!.id, target.id)]), anchor ?? list[at]!.id)
    }
    const s = scroller.current
    if (s) {
      const y = scrollToShow(to, metrics, s.scrollTop - gridTop(), s.clientHeight)
      if (y !== undefined) s.scrollTop = y + gridTop()
    }
    libraryView.set({ active: target.id })
    focusAfter.current = target.id
    measure()
  }

  const setQuery = (q: string): void => {
    libraryView.set({ q })
    if (scroller.current) scroller.current.scrollTop = 0
  }
  const empty = !assets.isPending && !assets.isError && list.length === 0
  const sub = total === undefined ? ' ' : countLabel(total, hasQuery ? everything.data : undefined)
  const tabStop = list.find((a) => a.id === active && shown.includes(a))?.id ?? shown[0]?.id
  const hidden = selected.size - chosen.length

  return (
    <>
      <div className="screen library" ref={scroller} onScroll={onScroll}>
        <ScreenHead title="Library" sub={sub} />
        <div className="library-bar" ref={bar}>
          <label className="search">
            <SearchIcon />
            <span className="sr-only">Search assets</span>
            <input
              className="input"
              type="search"
              placeholder="Search assets"
              value={view.q}
              onChange={(e) => setQuery(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Escape' && view.q) {
                  e.stopPropagation()
                  setQuery('')
                }
              }}
            />
          </label>
          <select
            className="input"
            aria-label="Sort by"
            value={view.sort}
            onChange={(e) => {
              libraryView.set({ sort: e.target.value as AssetSort })
              if (scroller.current) scroller.current.scrollTop = 0
            }}
          >
            {SORTS.map((s) => (
              <option key={s.id} value={s.id}>
                {s.label}
              </option>
            ))}
          </select>
        </div>
        {assets.isError && (
          <div className="library-error">
            <ErrorBlock role="alert">The library could not be loaded. {assets.error.message}</ErrorBlock>
            <button className="btn" onClick={() => void assets.refetch()}>
              Try again
            </button>
          </div>
        )}
        {empty &&
          (hasQuery ? (
            <Empty
              icon={NoResultsIcon}
              title={`No assets match "${query}"`}
              action={
                <button className="btn" onClick={() => setQuery('')}>
                  Clear search
                </button>
              }
            >
              Check the spelling or clear the search.
            </Empty>
          ) : (
            <Empty
              icon={LibraryIcon}
              title="No assets yet"
              action={
                <button className="btn" onClick={() => transition(() => navigate({ screen: 'generate' }))}>
                  Generate an asset
                </button>
              }
            >
              Assets you generate appear here.
            </Empty>
          ))}
        {assets.isPending && (
          <div className="grid-static" aria-hidden>
            {Array.from({ length: SKELETONS }, (_, i) => (
              <div key={i} className="card-skeleton">
                <div className="skeleton card-skeleton-img" />
                <div className="skeleton card-skeleton-line" />
              </div>
            ))}
          </div>
        )}
        <div className="grid-sizer" ref={sizer} style={{ height: win.height }} onKeyDown={onGridKey}>
          <div
            className="grid"
            style={
              {
                transform: `translateY(${win.offset}px)`,
                gridTemplateColumns: `repeat(${metrics.columns}, minmax(0, 1fr))`,
                gridAutoRows: `${metrics.row}px`,
                gap: metrics.gap,
              } as CSSProperties
            }
          >
            {shown.map((a) => (
              <AssetCard
                key={a.id}
                asset={a}
                caption={presetName(a.preset, presets.data)}
                selected={selected.has(a.id)}
                leaving={leaving.has(a.id)}
                tabStop={a.id === tabStop}
                onOpen={open}
                onToggle={toggle}
                onFocusCard={(id) => active !== id && libraryView.set({ active: id })}
                onPress={(asset) => readyDrags(selected.has(asset.id) ? chosen : [asset])}
                onDragOut={(e, asset) =>
                  dragOut(
                    e,
                    dragIds(
                      list.map((x) => x.id),
                      selected,
                      asset.id,
                    ),
                  )
                }
              />
            ))}
          </div>
        </div>
      </div>
      <div className="selection-bar" data-open={selecting} inert={!selecting} role="toolbar" aria-label="Selected assets">
        <span className="selection-count" aria-live="polite">
          {chosen.length} selected{hidden > 0 && <span className="dim"> · {hidden} hidden</span>}
        </span>
        {chosen.length < list.length && (
          <button className="btn ghost sm" onClick={() => libraryView.set({ selected: new Set(list.map((a) => a.id)) })}>
            Select all
          </button>
        )}
        <ExportMenu assets={chosen} up small />
        <button className="btn danger sm" onClick={() => setConfirming(true)}>
          <DeleteIcon />
          Delete
        </button>
        <button className="btn ghost icon sm" aria-label="Clear selection" title="Clear selection" onClick={clear}>
          <CloseIcon />
        </button>
      </div>
      {confirming && <ConfirmDelete names={chosen.map((a) => a.name)} onConfirm={() => void remove(chosen)} onClose={() => setConfirming(false)} />}
    </>
  )
}

/**
 * Glides cards to their new places when the list changes (a deletion, a new asset, another
 * sort) instead of letting them jump. Places come from the grid's arithmetic, not from the
 * page, so scrolling and resizing the window never animate.
 */
function useGlide(sizer: RefObject<HTMLDivElement | null>, shown: Asset[], from: number, m: GridMetrics): void {
  const last = useRef(new Map<string, { x: number; y: number }>())
  const lastLayout = useRef('')
  useLayoutEffect(() => {
    const layout = `${m.columns}:${m.cell}:${m.row}:${m.gap}`
    const now = new Map<string, { x: number; y: number }>()
    shown.forEach((a, i) => {
      const at = from + i
      now.set(a.id, { x: (at % m.columns) * (m.cell + m.gap), y: Math.floor(at / m.columns) * (m.row + m.gap) })
    })
    if (layout === lastLayout.current && !matchMedia('(prefers-reduced-motion: reduce)').matches) {
      for (const [id, at] of now) {
        const before = last.current.get(id)
        if (!before) continue
        const dx = before.x - at.x
        const dy = before.y - at.y
        if (Math.abs(dx) < 1 && Math.abs(dy) < 1) continue
        sizer.current
          ?.querySelector<HTMLElement>(`[data-id="${CSS.escape(id)}"]`)
          ?.animate([{ transform: `translate(${dx}px, ${dy}px)` }, { transform: 'none' }], { duration: 260, easing: 'cubic-bezier(0.2, 0.8, 0.2, 1)' })
      }
    }
    last.current = now
    lastLayout.current = layout
  }, [shown, from, m, sizer])
}

interface CardProps {
  asset: Asset
  caption: string
  selected: boolean
  leaving: boolean
  tabStop: boolean
  onOpen: (id: string) => void
  onToggle: (id: string, shift: boolean) => void
  onFocusCard: (id: string) => void
  onPress: (asset: Asset) => void
  onDragOut: (e: React.DragEvent, asset: Asset) => void
}

function AssetCard({ asset, caption, selected, leaving, tabStop, onOpen, onToggle, onFocusCard, onPress, onDragOut }: CardProps) {
  const tris = asset.stats?.triangles
  const error = asset.thumbnail ? undefined : thumbnailError(asset)
  const line = typeof tris === 'number' ? `${caption} · ${count(tris)} tris` : caption
  return (
    <div className={`card ${selected ? 'selected' : ''} ${leaving ? 'leaving' : ''}`} data-id={asset.id}>
      <button
        className="card-main"
        draggable
        tabIndex={tabStop ? 0 : -1}
        aria-label={`Open ${asset.name}`}
        aria-describedby={undefined}
        title={`${asset.name}\nCreated ${relative(asset.created_at)}`}
        onFocus={() => onFocusCard(asset.id)}
        onPointerEnter={() => readyDrag(asset)}
        onPointerDown={() => onPress(asset)}
        onDragStart={(e) => onDragOut(e, asset)}
        onClick={(e) => {
          // Ctrl, Cmd or Shift select; a plain click always opens.
          if (e.shiftKey || e.ctrlKey || e.metaKey) onToggle(asset.id, e.shiftKey)
          else onOpen(asset.id)
        }}
      >
        <div className="card-img">
          {asset.thumbnail ? (
            <img src={artifactURL(asset.thumbnail, 'image/webp')} alt="" decoding="async" draggable={false} />
          ) : error ? (
            <div className="card-failed" title={error}>
              <AlertIcon />
              Preview unavailable
            </div>
          ) : (
            <div className="skeleton card-pending" />
          )}
          {rigOf(asset) && <span className="card-tag">Rigged</span>}
        </div>
        <div className="card-body">
          <strong>{asset.name}</strong>
          <span className="dim">{line}</span>
        </div>
      </button>
      <button
        className="card-check"
        role="checkbox"
        tabIndex={-1}
        aria-checked={selected}
        aria-label={`Select ${asset.name}`}
        onClick={(e) => onToggle(asset.id, e.shiftKey)}
      >
        <CheckIcon size={14} strokeWidth={2.5} />
      </button>
    </div>
  )
}
