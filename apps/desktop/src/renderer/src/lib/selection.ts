// Which assets a click selects in the Library, and the small helpers its actions share.
import { withoutAssets } from './assets'

/** The ids from `anchor` to `target` in list order, both included; just `target` when the anchor is gone. */
export function rangeBetween(ids: readonly string[], anchor: string | null, target: string): string[] {
  const from = anchor === null ? -1 : ids.indexOf(anchor)
  const to = ids.indexOf(target)
  if (to < 0) return []
  if (from < 0) return [target]
  return ids.slice(Math.min(from, to), Math.max(from, to) + 1)
}

/** A click on a card's check: Shift selects the range from the last one clicked, else it toggles. */
export function clickSelect(selected: ReadonlySet<string>, ids: readonly string[], anchor: string | null, id: string, shift: boolean): Set<string> {
  const next = new Set(selected)
  if (shift) {
    for (const x of rangeBetween(ids, anchor, id)) next.add(x)
  } else if (next.has(id)) {
    next.delete(id)
  } else {
    next.add(id)
  }
  return next
}

/** What dragging a card carries: the whole selection, in list order, when the card is part of it. */
export function dragIds(ids: readonly string[], selected: ReadonlySet<string>, id: string): string[] {
  return selected.has(id) ? ids.filter((x) => selected.has(x)) : [id]
}

/** A cached list of assets, plain or paged, without one of them. */
export const withoutAsset = <T>(cache: T, id: string): T => withoutAssets(cache, new Set([id]))

/** The file name at the end of a path the core wrote, from any OS. */
export const baseName = (path: string): string => path.split(/[\\/]/).pop() || path

/** A file name for an asset, whose name may be the path of its image. */
export function fileStem(name: string): string {
  const stem = baseName(name).replace(/\.[^.]*$/, '')
  // eslint-disable-next-line no-control-regex
  const safe = stem.replace(/[<>:"/\\|?*\u0000-\u001f]/g, '-').trim()
  return safe && safe !== '.' ? safe : 'asset'
}
