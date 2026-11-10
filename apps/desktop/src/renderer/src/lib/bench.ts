import type { BenchComparison, BenchDelta, BenchEntry, BenchList, BenchPreset, BenchResult, BenchSample } from './api'
import { bytes, seconds } from './format'

export const sampleDone = (s: BenchSample): boolean => s.status === 'succeeded' || s.status === 'failed' || s.status === 'cancelled'

/** A kept result as the list shows it. */
export function entryOf(r: BenchResult): BenchEntry {
  return {
    id: r.id,
    label: r.label,
    status: r.status,
    suite: r.suite,
    options: r.options,
    forja: r.forja,
    device: r.device,
    started_at: r.started_at,
    samples: r.jobs.length,
    failed: r.jobs.filter((j) => j.status !== 'succeeded').length,
    presets: r.summary?.presets ?? [],
    core_overhead: r.summary?.core_overhead ?? { n: 0, median: 0, p90: 0, max: 0 },
  }
}

/** The list with a pushed result in it: running while it runs, then first among the kept ones. */
export function withBench(list: BenchList | undefined, r: BenchResult): BenchList | undefined {
  if (!list) return list
  if (r.status === 'running') return { ...list, running: r }
  return {
    running: list.running?.id === r.id ? null : list.running,
    results: [entryOf(r), ...list.results.filter((e) => e.id !== r.id)],
  }
}

/** How far a run has come, 0 to 1. */
export const progress = (r: BenchResult): number => (r.total ? r.jobs.filter(sampleDone).length / r.total : 0)

/** What a running benchmark does now, in words. */
export function phaseLabel(r: BenchResult): string {
  if (r.phase === 'warm-up') return 'Loading the models with one image per preset'
  const done = r.jobs.filter(sampleDone).length
  return `Measuring ${Math.min(done + 1, r.total)} of ${r.total}`
}

/** Only a run that completed is a fair thing to compare against. */
export const usableBaseline = (e: BenchEntry): boolean => e.status === 'succeeded'

/** Earlier completed runs of the same suite version on the same device, newest first. */
export function baselineChoices(results: BenchEntry[], r: Pick<BenchEntry, 'id' | 'suite' | 'device' | 'started_at'>): BenchEntry[] {
  return results
    .filter(
      (e) =>
        e.id !== r.id &&
        usableBaseline(e) &&
        e.suite.name === r.suite.name &&
        e.suite.version === r.suite.version &&
        e.device.id === r.device.id &&
        e.started_at < r.started_at,
    )
    .sort((a, b) => b.started_at.localeCompare(a.started_at))
}

/** The run to compare with by default: the newest completed one, never a cancelled or failed run. */
export const baselineFor = (results: BenchEntry[], r: Pick<BenchEntry, 'id' | 'suite' | 'device' | 'started_at'>): BenchEntry | undefined =>
  baselineChoices(results, r)[0]

/** "Nothing slower" is true only when something was measured against the baseline. */
export const verdictOf = (c: BenchComparison): 'regressed' | 'clean' | 'nothing' =>
  c.regressions > 0 ? 'regressed' : c.deltas.length > 0 ? 'clean' : 'nothing'

/** A preset's median against its budget; undefined when the suite sets none. */
export function budgetState(p: BenchPreset): 'ok' | 'over' | undefined {
  if (!p.budget_seconds || !p.seconds.n) return undefined
  return p.seconds.median > p.budget_seconds ? 'over' : 'ok'
}

/** What a delta measures, as a row label. */
export function deltaName(d: BenchDelta): string {
  if (d.metric === 'core_overhead') return 'Core overhead per job'
  const what = d.metric === 'failed' ? 'failed samples' : d.metric === 'peak_vram_bytes' ? 'peak VRAM' : d.stage ? 'time' : 'image to GLB'
  return [d.preset, d.stage, what].filter(Boolean).join(' · ')
}

/** A delta's value in its own unit. */
export function deltaValue(d: BenchDelta, v: number): string {
  switch (d.metric) {
    case 'peak_vram_bytes':
      return bytes(v)
    case 'failed':
      return String(v)
    default:
      return seconds(v)
  }
}

export const pct = (p: number | null): string => (p === null ? '—' : `${p > 0 ? '+' : ''}${p.toFixed(1)}%`)

export const shortCommit = (c?: string): string => (c ? c.slice(0, 7) : '—')
