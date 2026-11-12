import type { Stage } from './api'

/**
 * What stages measured about their outputs, in the words `forja jobs show` uses. The wording and
 * the order mirror core/internal/quality; change them together.
 */

const share = (v: number): string => `${Math.round(v * 100)}%`
const plain = (v: number): string => (Number.isInteger(v) ? String(v) : String(Number(v.toPrecision(4))))

/** The side views texture.match fits, in the order they are shown. */
const VIEWS = ['left', 'back', 'right']

/** The metrics the built-in stages emit, in the order lines show them. */
const KNOWN: { key: string; words: (v: number) => string }[] = [
  { key: 'match_iou', words: (v) => `reference fit ${share(v)}` },
  { key: 'match_reprojected', words: (v) => `reference projected onto ${share(v)} of the texture` },
  { key: 'bake_hit_ratio', words: (v) => `bake reached the high-poly on ${share(v)} of texels` },
  { key: 'bake_cage', words: (v) => `bake cage ${(v * 100).toFixed(1)}% of the size` },
  { key: 'uv_coverage', words: (v) => `UV space used ${share(v)}` },
  { key: 'lod_open_edges', words: (v) => `LOD cracks: ${plain(v)} open ${v === 1 ? 'edge' : 'edges'}` },
]

/** "match_left_iou" is the fit of the left view. */
function sideView(key: string): string | undefined {
  const m = /^match_([a-z0-9]+)_iou$/.exec(key)
  return m?.[1]
}

/** One metric as a person reads it: "reference fit 93%". A key nobody knows reads "key value". */
export function describeMetric(key: string, value: number): string {
  const known = KNOWN.find((k) => k.key === key)
  if (known) return known.words(value)
  const view = sideView(key)
  if (view) return `${view} view fit ${share(value)}`
  return `${key} ${plain(value)}`
}

/** The keys in the order lines show them: the known ones, the side views after the front fit, the rest by name. */
function orderedKeys(metrics: Record<string, number>): string[] {
  const keys = Object.keys(metrics)
  const known = KNOWN.map((k) => k.key).filter((k) => keys.includes(k))
  const views = keys
    .filter((k) => sideView(k) !== undefined)
    .sort((a, b) => {
      const rank = (k: string): number => {
        const i = VIEWS.indexOf(sideView(k)!)
        return i < 0 ? VIEWS.length : i
      }
      return rank(a) - rank(b) || (a < b ? -1 : a > b ? 1 : 0)
    })
  const rest = keys.filter((k) => !known.includes(k) && !views.includes(k)).sort()
  // The side views follow the front view's fit; without it they follow the known ones.
  const after = known.includes('match_iou') ? known.indexOf('match_iou') + 1 : known.length
  return [...known.slice(0, after), ...views, ...known.slice(after), ...rest]
}

/** Every metric worded, one string each: ["reference fit 93%", "left view fit 62%"]. */
export function describeMetrics(metrics: Record<string, number> | undefined): string[] {
  if (!metrics) return []
  return orderedKeys(metrics).map((k) => describeMetric(k, metrics[k]!))
}

/** What a stage measured about its outputs, merged; a name two outputs carry keeps the later one's value. */
export function stageMetrics(stage: Pick<Stage, 'outputs'>): Record<string, number> {
  return Object.assign({}, ...stage.outputs.map((o) => o.metrics ?? {})) as Record<string, number>
}

/** The words for a finished stage's metrics, none for a stage that has not finished or measured nothing. */
export function measureLines(stage: Pick<Stage, 'outputs' | 'status'>): string[] {
  return stage.status === 'succeeded' ? describeMetrics(stageMetrics(stage)) : []
}

/** A metric's name as a row label in a comparison: "reference fit", "left view fit". */
export function metricName(key: string): string {
  const names: Record<string, string> = {
    match_iou: 'reference fit',
    match_reprojected: 'reference projected',
    bake_hit_ratio: 'bake reached the high-poly',
    bake_cage: 'bake cage',
    uv_coverage: 'UV space used',
    lod_open_edges: 'LOD open edges',
  }
  const view = sideView(key)
  return names[key] ?? (view ? `${view} view fit` : key)
}

/** A metric's number in its own unit, as the core's delta table shows it: "93%", "0", "5.0%". */
export function metricValue(key: string, value: number): string {
  if (key === 'bake_cage') return `${(value * 100).toFixed(1)}%`
  if (key === 'lod_open_edges') return plain(value)
  if (KNOWN.some((k) => k.key === key) || sideView(key)) return share(value)
  return plain(value)
}
