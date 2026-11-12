export function seconds(s: number): string {
  if (!Number.isFinite(s) || s <= 0) return '—'
  if (s < 1) return `${Math.round(s * 1000)} ms`
  if (s < 59.5) return `${s.toFixed(s < 10 ? 1 : 0)} s`
  // Rounded before it is split, so 119.6 s reads "2 min 0 s", never "1 min 60 s".
  const whole = Math.round(s)
  return `${Math.floor(whole / 60)} min ${whole % 60} s`
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
  rig: 'Rig',
  'mesh.lods': 'LODs',
  export: 'Export',
}

export const stageLabel = (use: string): string => stageLabels[use] ?? use

const sideViews: Record<string, string> = { left: 'left side', back: 'back', right: 'right side' }

/**
 * A job's stage by name: the copies that prepare another view say which ("bg-left"), and the
 * denser mesh a preset can paint is told apart from the low-poly it sits beside.
 */
export function stageName(s: { id: string; use: string }): string {
  const view = /-(left|back|right)$/.exec(s.id)?.[1]
  if (view && (s.use === 'background-removal' || s.use === 'framing')) return `${stageLabel(s.use)} · ${sideViews[view]}`
  return s.id === 'dense' && s.use === 'mesh.decimate' ? 'Dense mesh' : stageLabel(s.use)
}

/** A stage type as a noun in a sentence: "a background removal model". */
export const taskNoun = (use: string): string =>
  use === 'background-removal' ? 'background removal' : use === 'rig' ? 'rigging' : stageLabel(use).toLowerCase()

/** A rig's skeleton as the asset page names it. */
export const skeletonName = (rig: { skeleton?: string }): string => (rig.skeleton === 'humanoid' ? 'Humanoid' : 'Generic')

/** "a", "a and b", "a, b and c". */
export function listOf(items: string[]): string {
  if (items.length <= 1) return items.join('')
  return `${items.slice(0, -1).join(', ')} and ${items[items.length - 1]}`
}

/** The core's unit for a price per generation; any other unit cannot be added up per image. */
export const PER_CALL = 'generation'

export function money(p: { amount: number; unit: string; currency: string } | undefined): string {
  if (!p) return 'price unknown'
  const symbol = p.currency === 'USD' ? '$' : `${p.currency} `
  const amount = `${symbol}${p.amount < 0.1 ? p.amount.toFixed(3) : p.amount.toFixed(2)}`
  return `${amount} per ${p.unit === PER_CALL ? 'image' : p.unit.replace(/s$/, '')}`
}
