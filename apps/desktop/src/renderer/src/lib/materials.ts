// An asset's materials: the few dominant colors of its base color texture, and how rough and how
// metallic the person says each is. The core finds them and writes the values into the asset's
// metallic-roughness map (GET and PUT /v1/assets/{id}/materials).
import { api } from './api'

export interface MaterialRow {
  index: number
  /** The base color texture it was found in, for an asset with several. */
  texture: number
  /** #rrggbb */
  color: string
  /** 0..1, the part of its texture that has this color. */
  share: number
  roughness: number
  metallic: number
}

export interface AssetMaterials {
  materials: MaterialRow[]
  /** The asset's model painted its roughness and metallic: setting them replaces that work. */
  painted: boolean
  /** Why there are no materials to set, when there are none. */
  reason?: string
}

export interface MaterialSet {
  index: number
  roughness?: number
  metallic?: number
}

export const materialsKey = (id: string) => ['materials', id] as const

export const fetchMaterials = (id: string, signal?: AbortSignal): Promise<AssetMaterials> =>
  api('GET', `/v1/assets/${encodeURIComponent(id)}/materials`, undefined, { signal })

/** How many edits of an asset this window has under way, so its own change is not read twice. */
const editing = new Map<string, number>()

/** Whether this window is editing the asset's materials now, or just did. */
export const isEditingHere = (id: string): boolean => (editing.get(id) ?? 0) > 0

export async function setMaterials(id: string, materials: MaterialSet[], overwrite: boolean): Promise<AssetMaterials> {
  editing.set(id, (editing.get(id) ?? 0) + 1)
  try {
    return await api('PUT', `/v1/assets/${encodeURIComponent(id)}/materials`, { materials, overwrite: overwrite || undefined })
  } finally {
    // The core announces the change before it answers; a moment after the answer is still ours.
    setTimeout(() => editing.set(id, Math.max(0, (editing.get(id) ?? 1) - 1)), 1000)
  }
}

/** Values the person moved and the core has not confirmed, by material. */
export type Edits = ReadonlyMap<number, { roughness?: number; metallic?: number }>

/** How long after the last move the values are sent: a drag is one change, not fifty. */
export const SEND_DELAY_MS = 350

export const clamp01 = (v: number): number => Math.min(1, Math.max(0, v))

/** The edits with one more value moved. */
export function withEdit(edits: Edits, index: number, field: 'roughness' | 'metallic', value: number): Edits {
  const next = new Map(edits)
  next.set(index, { ...next.get(index), [field]: clamp01(value) })
  return next
}

/** The request that carries the edits, in material order. */
export function editsToSets(edits: Edits): MaterialSet[] {
  return [...edits].sort(([a], [b]) => a - b).map(([index, v]) => ({ index, ...v }))
}

/** What a material shows: the value being moved, else the one the core has. */
export function shownValues(row: MaterialRow, edits: Edits): { roughness: number; metallic: number } {
  const e = edits.get(row.index)
  return { roughness: e?.roughness ?? row.roughness, metallic: e?.metallic ?? row.metallic }
}

/** The edits left once the core has taken `sent`: a value moved again meanwhile stays. */
export function withoutSent(edits: Edits, sent: readonly MaterialSet[]): Edits {
  const next = new Map(edits)
  for (const s of sent) {
    const e = { ...next.get(s.index) }
    if (s.roughness !== undefined && e.roughness === s.roughness) delete e.roughness
    if (s.metallic !== undefined && e.metallic === s.metallic) delete e.metallic
    if (e.roughness === undefined && e.metallic === undefined) next.delete(s.index)
    else next.set(s.index, e)
  }
  return next
}

export const percent = (share: number): string => `${Math.max(1, Math.round(share * 100))}%`

/** A value as the slider's readout shows it. */
export const valueText = (v: number): string => v.toFixed(2)

/** Words for a material, for a screen reader: its color is only a swatch to the eye. */
export const materialName = (row: MaterialRow, position: number): string => `Material ${position + 1}, ${row.color}, ${percent(row.share)}`
