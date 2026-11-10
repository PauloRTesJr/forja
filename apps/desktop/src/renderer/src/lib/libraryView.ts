// What the Library screen was showing: its search, order, selection and place. The screen is
// unmounted whenever another one opens (an asset's page, the queue), so this lives outside it:
// coming back shows the same cards, selected as they were, scrolled to where they were.
import { useSyncExternalStore } from 'react'
import type { AssetSort } from './assets'

export interface LibraryView {
  q: string
  sort: AssetSort
  selected: ReadonlySet<string>
  /** The card a Shift-click or Shift-arrow extends the selection from. */
  anchor: string | null
  /** The card that holds the keyboard focus within the grid. */
  active: string | null
}

const START: LibraryView = { q: '', sort: 'newest', selected: new Set(), anchor: null, active: null }

let view = START
// Not part of what the screen renders from: read once when it mounts.
let scrollTop = 0
let opened: string | null = null
const listeners = new Set<() => void>()

export const libraryView = {
  get: (): LibraryView => view,
  set(patch: Partial<LibraryView>): void {
    view = { ...view, ...patch }
    for (const fn of listeners) fn()
  },
  subscribe(fn: () => void): () => void {
    listeners.add(fn)
    return () => listeners.delete(fn)
  },
  /** Where the grid was scrolled to, kept as it scrolls. */
  rememberScroll(top: number): void {
    scrollTop = top
  },
  scrollTop: (): number => scrollTop,
  /** The asset whose page was opened from the grid; read (and forgotten) on the way back, to focus its card. */
  open(id: string): void {
    opened = id
    view = { ...view, active: id }
  },
  returnedFrom(): string | null {
    const id = opened
    opened = null
    return id
  },
  reset(): void {
    view = START
    scrollTop = 0
    opened = null
    for (const fn of listeners) fn()
  },
}

export function useLibraryView(): LibraryView {
  return useSyncExternalStore(libraryView.subscribe, libraryView.get)
}

/** A selection without the assets that are no longer in the library. */
export function pruned(selected: ReadonlySet<string>, ids: Iterable<string>): ReadonlySet<string> {
  const have = new Set(ids)
  const kept = [...selected].filter((id) => have.has(id))
  return kept.length === selected.size ? selected : new Set(kept)
}
