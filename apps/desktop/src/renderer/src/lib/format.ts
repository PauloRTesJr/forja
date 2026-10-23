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
  'mesh.uv': 'UV unwrap',
  texture: 'Texture',
  'texture.match': 'Texture match',
  bake: 'Bake',
  'mesh.lods': 'LODs',
  export: 'Export',
}

export const stageLabel = (use: string): string => stageLabels[use] ?? use

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
