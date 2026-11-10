// The library's list of assets as the core pages it, and the edits the app makes to its cached
// copy when an asset changes, so a rename or a new thumbnail never refetches the whole list.
import type { InfiniteData } from '@tanstack/react-query'
import { api, type Asset, type ExportResult } from './api'
import { request } from './http'

export type AssetSort = 'newest' | 'oldest' | 'name'

export const SORTS: { id: AssetSort; label: string }[] = [
  { id: 'newest', label: 'Newest first' },
  { id: 'oldest', label: 'Oldest first' },
  { id: 'name', label: 'Name' },
]

/** One page of the list, and how many assets match in all when the core says. */
export interface AssetPage {
  items: Asset[]
  total?: number
}

export type AssetList = InfiniteData<AssetPage, unknown>

export const PAGE_SIZE = 200

/** Under `['assets']`, so whatever refreshes or edits the assets reaches these lists too. */
export const assetListKey = (q: string, sort: AssetSort) => ['assets', 'list', q, sort] as const

/** Whether a query key is a list the core sorts by name, where a rename moves the asset. */
export const isNameSortedList = (key: readonly unknown[]): boolean => key[0] === 'assets' && key[1] === 'list' && key[3] === 'name'

export async function fetchAssetPage(q: string, sort: AssetSort, offset: number, signal?: AbortSignal): Promise<AssetPage> {
  const params = new URLSearchParams({ limit: String(PAGE_SIZE), offset: String(offset), sort, q })
  const res = await request('GET', `/v1/assets?${params}`, {}, { signal })
  const items = (await res.json()) as Asset[]
  const total = Number(res.headers.get('X-Total-Count') ?? NaN)
  return { items, total: Number.isFinite(total) ? total : undefined }
}

/** How many assets there are in all, whatever a search shows. Under `['assets']`, so a new asset refreshes it. */
export const assetCountKey = ['assets', 'count'] as const

export async function fetchAssetCount(signal?: AbortSignal): Promise<number | undefined> {
  const res = await request('GET', '/v1/assets?limit=1', {}, { signal })
  const total = Number(res.headers.get('X-Total-Count') ?? NaN)
  return Number.isFinite(total) ? total : undefined
}

/** Where the next page starts, or undefined when everything that matches is loaded. */
export function nextOffset(pages: readonly AssetPage[]): number | undefined {
  const last = pages.at(-1)
  // Without a total there is no telling whether the core pages at all: stop at what came.
  if (!last || last.total === undefined || last.items.length === 0) return undefined
  const loaded = pages.reduce((n, p) => n + p.items.length, 0)
  return loaded < last.total ? loaded : undefined
}

/** Every loaded asset once, in order: a page fetched after the list grew can repeat a neighbour's last items. */
export function flatten(list: AssetList | undefined): Asset[] {
  const seen = new Set<string>()
  const out: Asset[] = []
  for (const page of list?.pages ?? []) {
    for (const a of page.items) {
      if (seen.has(a.id)) continue
      seen.add(a.id)
      out.push(a)
    }
  }
  return out
}

/** How many assets match in all; what is loaded when the core does not say. */
export function totalOf(list: AssetList | undefined): number | undefined {
  if (!list) return undefined
  return list.pages.at(-1)?.total ?? flatten(list).length
}

const isPaged = (cache: unknown): cache is AssetList => !!cache && typeof cache === 'object' && Array.isArray((cache as AssetList).pages)

/**
 * A cached list (plain or paged) with `edit` applied to its assets; anything else comes back as
 * it is. A paged list's total follows the number of assets the edit added or took away.
 */
function editCached<T>(cache: T, edit: (items: Asset[]) => Asset[]): T {
  if (Array.isArray(cache)) return edit(cache as Asset[]) as T
  if (!isPaged(cache)) return cache
  let delta = 0
  const pages = cache.pages.map((p) => {
    const items = edit(p.items)
    delta += items.length - p.items.length
    return items === p.items ? p : { ...p, items }
  })
  if (pages.every((p, i) => p === cache.pages[i])) return cache
  const counted = delta === 0 ? pages : pages.map((p) => (p.total === undefined ? p : { ...p, total: Math.max(0, p.total + delta) }))
  return { ...cache, pages: counted } as T
}

/** A cached list without these assets. */
export function withoutAssets<T>(cache: T, ids: ReadonlySet<string>): T {
  return editCached(cache, (items) => (items.some((a) => ids.has(a.id)) ? items.filter((a) => !ids.has(a.id)) : items))
}

/** A cached list with one asset replaced by its newer self; a list that does not hold it is unchanged. */
export function withAsset<T>(cache: T, asset: Asset): T {
  return editCached(cache, (items) => (items.some((a) => a.id === asset.id) ? items.map((a) => (a.id === asset.id ? asset : a)) : items))
}

export const renameAsset = (id: string, name: string): Promise<Asset> => api('PATCH', `/v1/assets/${encodeURIComponent(id)}`, { name })

/** Why a name cannot be given to an asset, or undefined when it can. The core has the last word. */
export function nameProblem(name: string): string | undefined {
  const trimmed = name.trim()
  if (!trimmed) return 'Type a name.'
  if (trimmed.length > 120) return 'A name can be 120 characters at most.'
  // eslint-disable-next-line no-control-regex
  if (/[\u0000-\u001f\u007f]/.test(trimmed)) return 'A name cannot hold a line break.'
  return undefined
}

/** A path with the format's extension, when the Save dialog gave it without (Linux does not add one). */
export function withExtension(path: string, ext: string): string {
  return path.toLowerCase().endsWith(ext.toLowerCase()) ? path : path + ext
}

export interface ExportTarget {
  /** The file to write, for one asset. */
  file?: string
  /** The folder to write into; files whose names are taken there are numbered, never replaced. */
  folder?: string
}

/** Exports assets to one file or into a folder. Runs as long as Blender does; `signal` calls it off. */
export function exportTo(ids: string[], format: string, target: ExportTarget, signal?: AbortSignal): Promise<ExportResult> {
  const body = target.file
    ? { ids, format, path: target.file, file: true }
    : { ids, format, path: target.folder, on_conflict: 'rename' }
  return api('POST', '/v1/export', body, { signal, timeoutMs: 0 })
}

/** The number of levels of detail the core counted in an asset's file, the full one included. */
export function lodCount(asset: { stats?: Asset['stats'] }): number | undefined {
  const lods = asset.stats?.lods
  return typeof lods === 'number' ? lods + 1 : undefined
}

export const statNumber = (asset: { stats?: Asset['stats'] }, key: string): number | undefined => {
  const v = asset.stats?.[key]
  return typeof v === 'number' ? v : undefined
}

/** "1 asset", "14 assets", and "8 of 14 assets" while a search hides some. */
export function countLabel(shown: number, all?: number): string {
  const noun = (n: number): string => (n === 1 ? 'asset' : 'assets')
  if (all !== undefined && all !== shown) return `${shown} of ${all} ${noun(all)}`
  return `${shown} ${noun(shown)}`
}
