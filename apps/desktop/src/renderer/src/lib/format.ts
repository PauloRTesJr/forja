export function seconds(s: number): string {
  if (!Number.isFinite(s) || s <= 0) return '—'
  if (s < 1) return `${Math.round(s * 1000)} ms`
  if (s < 60) return `${s.toFixed(s < 10 ? 1 : 0)} s`
  const m = Math.floor(s / 60)
  return `${m} min ${Math.round(s % 60)} s`
}

export function count(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`
  if (n >= 10_000) return `${Math.round(n / 1000)}k`
  if (n >= 1000) return `${(n / 1000).toFixed(1)}k`
  return String(n)
}

export function bytes(n: number): string {
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let i = 0
  while (n >= 1024 && i < units.length - 1) {
    n /= 1024
    i++
  }
  return `${n.toFixed(i === 0 || n >= 100 ? 0 : 1)} ${units[i]}`
}

export function relative(iso: string, now = Date.now()): string {
  const diff = (now - new Date(iso).getTime()) / 1000
  if (diff < 45) return 'just now'
  if (diff < 3600) return `${Math.round(diff / 60)} min ago`
  if (diff < 86400) return `${Math.round(diff / 3600)} h ago`
  return new Date(iso).toLocaleDateString()
}

const stageLabels: Record<string, string> = {
  'background-removal': 'Background',
  framing: 'Framing',
  shape: 'Shape',
  'mesh.cleanup': 'Cleanup',
  'mesh.decimate': 'Low-poly',
  'mesh.remesh': 'Quad remesh',
  'mesh.uv': 'UV unwrap',
  'mesh.uv.smart': 'Smart UV unwrap',
  texture: 'Texture',
  'texture.match': 'Texture match',
  bake: 'Bake',
  'mesh.lods': 'LODs',
  export: 'Export',
}

export const stageLabel = (use: string): string => stageLabels[use] ?? use

const sideViews: Record<string, string> = { left: 'left side', back: 'back', right: 'right side' }

/** A job's stage by name: the copies that prepare another view say which ("bg-left"). */
export function stageName(s: { id: string; use: string }): string {
  const view = /-(left|back|right)$/.exec(s.id)?.[1]
  return view && (s.use === 'background-removal' || s.use === 'framing') ? `${stageLabel(s.use)} · ${sideViews[view]}` : stageLabel(s.use)
}

/** A stage type as a noun in a sentence: "a background removal model". */
export const taskNoun = (use: string): string =>
  use === 'background-removal' ? 'background removal' : stageLabel(use).toLowerCase()

/** "a", "a and b", "a, b and c". */
export function listOf(items: string[]): string {
  if (items.length <= 1) return items.join('')
  return `${items.slice(0, -1).join(', ')} and ${items[items.length - 1]}`
}

const numerals: [number, string][] = [
  [100, 'C'],
  [90, 'XC'],
  [50, 'L'],
  [40, 'XL'],
  [10, 'X'],
  [9, 'IX'],
  [5, 'V'],
  [4, 'IV'],
  [1, 'I'],
]

/** Tarot-style card numbers. */
export function roman(n: number): string {
  let out = ''
  for (const [value, glyph] of numerals) {
    while (n >= value) {
      out += glyph
      n -= value
    }
  }
  return out
}

/** "$0.30", or "$0.02 per second" when the unit is not one generation. */
/** The core's unit for a price per generation; any other unit cannot be added up per image. */
export const PER_CALL = 'generation'

export function money(p: { amount: number; unit: string; currency: string } | undefined): string {
  if (!p) return 'price unknown'
  const symbol = p.currency === 'USD' ? '$' : `${p.currency} `
  const amount = `${symbol}${p.amount < 0.1 ? p.amount.toFixed(3) : p.amount.toFixed(2)}`
  return `${amount} per ${p.unit === PER_CALL ? 'image' : p.unit.replace(/s$/, '')}`
}
