// The part of the asset grid that is worth mounting: the rows on screen and a few around them.
// Pure layout arithmetic, so the grid stays light with thousands of assets and it can be tested.

export interface GridMetrics {
  /** Cards per row. */
  columns: number
  /** A card's width; its thumbnail is a square of this size. */
  cell: number
  /** One row's height, without the gap under it. */
  row: number
  gap: number
}

/** Columns that fit `width` when a card is at least `min` wide, and what a card then measures. */
export function gridMetrics(width: number, min: number, gap: number, below: number): GridMetrics {
  const columns = Math.max(1, Math.floor((width + gap) / (min + gap)))
  const cell = Math.max(0, (width - gap * (columns - 1)) / columns)
  return { columns, cell, row: cell + below, gap }
}

export interface GridWindow {
  /** Index of the first and one past the last item to mount. */
  from: number
  to: number
  /** Where the first mounted row starts, from the top of the grid. */
  offset: number
  /** The whole grid's height, as if every row were mounted. */
  height: number
}

/**
 * Which items to mount for a grid of `count` items scrolled so that `top` (measured from the
 * grid's own top; negative while the grid starts below the fold) is the first visible pixel of
 * a viewport `viewport` tall. `overscan` rows are kept on each side so a scroll shows cards, not gaps.
 */
export function gridWindow(count: number, m: GridMetrics, top: number, viewport: number, overscan = 2): GridWindow {
  const rows = Math.ceil(count / m.columns)
  const pitch = m.row + m.gap
  const height = rows === 0 ? 0 : rows * pitch - m.gap
  if (rows === 0 || pitch <= 0) return { from: 0, to: 0, offset: 0, height }
  const first = Math.min(rows - 1, Math.max(0, Math.floor(top / pitch) - overscan))
  const last = Math.min(rows - 1, Math.max(first, Math.floor((top + viewport) / pitch) + overscan))
  return { from: first * m.columns, to: Math.min(count, (last + 1) * m.columns), offset: first * pitch, height }
}

/** The scroll position that brings item `index` fully into view, or undefined when it already is. */
export function scrollToShow(index: number, m: GridMetrics, top: number, viewport: number, margin = 8): number | undefined {
  const pitch = m.row + m.gap
  const y = Math.floor(index / m.columns) * pitch
  if (y - margin < top) return y - margin
  if (y + m.row + margin > top + viewport) return y + m.row + margin - viewport
  return undefined
}

/** The item an arrow key moves to from `index`, or `index` itself at an edge. */
export function moveInGrid(index: number, key: string, count: number, columns: number): number {
  if (count === 0) return -1
  const at = Math.max(0, index)
  switch (key) {
    case 'ArrowLeft':
      return Math.max(0, at - 1)
    case 'ArrowRight':
      return Math.min(count - 1, at + 1)
    case 'ArrowUp':
      return at - columns >= 0 ? at - columns : at
    case 'ArrowDown':
      // From the row before the last, down goes to the last card when the last row is short.
      return at + columns < count ? at + columns : Math.floor(at / columns) < Math.floor((count - 1) / columns) ? count - 1 : at
    case 'Home':
      return 0
    case 'End':
      return count - 1
    default:
      return at
  }
}
