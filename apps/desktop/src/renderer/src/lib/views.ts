// Several views of one subject: the views card on Generate and the split card in the queue.
import type { Pick, SheetPart, View } from './api'

export const VIEWS: readonly View[] = ['front', 'left', 'back', 'right']
export const SIDE_VIEWS = ['left', 'back', 'right'] as const
export type SideView = (typeof SIDE_VIEWS)[number]

export const viewLabels: Record<View, string> = { front: 'Front', left: 'Left side', back: 'Back', right: 'Right side' }

/** For the label on a box over a sheet, which may be narrower than the words. */
export const viewShort: Record<View, string> = { front: 'Front', left: 'Left', back: 'Back', right: 'Right' }

/** How each view shows the subject, so a side view goes in the right slot. */
export const viewHints: Record<View, string> = {
  front: 'Facing you',
  left: 'Facing the left edge',
  back: 'Seen from behind',
  right: 'Facing the right edge',
}

/**
 * The shape step, when its model would read other views of the subject, from a plan resolved
 * as if the job brought them. Texturing may read them too, but only the shape needs them.
 */
export function viewReader(plan: Pick[]): Pick | undefined {
  return plan.find((p) => p.use === 'shape' && !p.skipped && (p.views ?? []).some((v) => v !== 'front'))
}

/**
 * Gives part `index` a view. A view belongs to one part, so the part that had it takes this
 * part's old view: relabelling front and back swaps them instead of losing one.
 */
export function assignView(labels: (View | '')[], index: number, view: View | ''): (View | '')[] {
  const next = labels.slice()
  const previous = next[index] ?? ''
  if (view) {
    const holder = next.findIndex((v, i) => i !== index && v === view)
    if (holder >= 0) next[holder] = previous
  }
  next[index] = view
  return next
}

/** The parts to cut out, in view order: what answering the sheet sends. */
export function chosenParts(parts: SheetPart[], labels: (View | '')[]): SheetPart[] {
  const out: SheetPart[] = []
  for (const view of VIEWS) {
    const i = labels.indexOf(view)
    if (i >= 0) out.push({ box: parts[i]!.box, kind: parts[i]!.kind, view })
  }
  return out
}

/** A box as percentages of the image, for placing it over the picture at any size. */
export function boxStyle(box: SheetPart['box'], size: [number, number]): Record<string, string> {
  const [w, h] = size
  const pct = (n: number, of: number): string => `${((n / of) * 100).toFixed(3)}%`
  return {
    left: pct(box[0], w),
    top: pct(box[1], h),
    width: pct(box[2] - box[0], w),
    height: pct(box[3] - box[1], h),
  }
}
