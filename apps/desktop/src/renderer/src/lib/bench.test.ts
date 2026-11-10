import { describe, expect, it } from 'vitest'
import type { BenchComparison, BenchEntry, BenchList, BenchResult, BenchSample } from './api'
import { baselineChoices, baselineFor, budgetState, deltaName, deltaValue, entryOf, pct, phaseLabel, progress, verdictOf, withBench } from './bench'

const sample = (status: BenchSample['status']): BenchSample => ({
  image: 'chest',
  preset: 'draft',
  repeat: 1,
  status,
  wall_seconds: 1,
  stage_seconds: 1,
  load_seconds: 0,
  core_overhead_seconds: 0.1,
})

const run = (over: Partial<BenchResult> = {}): BenchResult => ({
  id: 'b1',
  status: 'running',
  suite: { name: 'ci', version: 1, path: '/suites/ci.yaml' },
  options: { presets: ['draft'], cold: false, batch: false, repeat: 1 },
  forja: { version: '0.1.0', commit: 'abcdef123456' },
  device: { id: 'gpu', provider: 'local' },
  started_at: '2026-10-24T10:00:00Z',
  total: 4,
  skipped: [],
  jobs: [],
  ...over,
})

describe('withBench', () => {
  const empty: BenchList = { running: null, results: [] }

  it('shows a run as running until it ends, then first among the kept results', () => {
    const running = run({ jobs: [sample('succeeded')] })
    expect(withBench(empty, running)).toEqual({ running, results: [] })
    const done = run({ status: 'succeeded', jobs: [sample('succeeded'), sample('failed')] })
    const old = entryOf(run({ id: 'b0', status: 'succeeded' }))
    const after = withBench({ running, results: [old] }, done)!
    expect(after.running).toBeNull()
    expect(after.results.map((e) => e.id)).toEqual(['b1', 'b0'])
    expect(after.results[0]).toMatchObject({ samples: 2, failed: 1 })
  })

  it('leaves a list nobody asked for alone', () => {
    expect(withBench(undefined, run())).toBeUndefined()
  })
})

describe('progress', () => {
  it('counts the samples that ended, and says what runs now', () => {
    const r = run({ jobs: [sample('succeeded'), sample('failed'), sample('running')] })
    expect(progress(r)).toBe(0.5)
    expect(phaseLabel(r)).toBe('Measuring 3 of 4')
    expect(phaseLabel(run({ phase: 'warm-up' }))).toMatch(/Loading the models/)
  })
})

describe('baselineFor', () => {
  const entry = (id: string, started: string, over: Partial<BenchEntry> = {}): BenchEntry => ({
    ...entryOf(run({ id, status: 'succeeded', started_at: started })),
    ...over,
  })

  it('picks the newest earlier run of the same suite version on the same device', () => {
    const current = entry('c', '2026-10-24T12:00:00Z')
    const list = [
      current,
      entry('other-device', '2026-10-24T11:30:00Z', { device: { id: 'cpu', provider: 'local' } }),
      entry('other-version', '2026-10-24T11:20:00Z', { suite: { name: 'ci', version: 2, path: '' } }),
      entry('newer', '2026-10-24T13:00:00Z'),
      entry('older', '2026-10-24T09:00:00Z'),
      entry('base', '2026-10-24T11:00:00Z'),
    ]
    expect(baselineFor(list, current)?.id).toBe('base')
    expect(baselineFor([current], current)).toBeUndefined()
  })
})

describe('baselines', () => {
  const entry = (id: string, started: string, status: BenchResult['status']): BenchEntry => entryOf(run({ id, status, started_at: started }))
  const current = entry('c', '2026-10-24T12:00:00Z', 'succeeded')

  it('skips a cancelled or failed run for the newest completed one', () => {
    const list = [
      current,
      entry('cancelled-newest', '2026-10-24T11:30:00Z', 'cancelled'),
      entry('failed', '2026-10-24T11:20:00Z', 'failed'),
      entry('done', '2026-10-24T11:00:00Z', 'succeeded'),
    ]
    expect(baselineFor(list, current)?.id).toBe('done')
    expect(baselineChoices(list, current).map((e) => e.id)).toEqual(['done'])
  })

  it('has no baseline when every earlier run was cancelled, or when there is none', () => {
    const onlyCancelled = [current, entry('x', '2026-10-24T11:00:00Z', 'cancelled')]
    expect(baselineFor(onlyCancelled, current)).toBeUndefined()
    expect(baselineChoices(onlyCancelled, current)).toEqual([])
    expect(baselineFor([current], current)).toBeUndefined()
  })
})

describe('verdictOf', () => {
  const cmp = (over: Partial<BenchComparison> = {}): BenchComparison => ({
    baseline: { id: 'a', forja: { version: '0.1.0' }, started_at: '2026-10-24T10:00:00Z' },
    current: { id: 'b', forja: { version: '0.1.0' }, started_at: '2026-10-24T11:00:00Z' },
    thresholds: { pct: 12.5, min_seconds: 0.1, min_vram_mb: 64 },
    deltas: [],
    regressions: 0,
    warnings: [],
    ...over,
  })
  const delta = { preset: 'draft', metric: 'seconds' as const, baseline: 1, current: 1, pct: 0, regression: false }

  it('says nothing was compared when the baseline shares no measure with the run', () => {
    expect(verdictOf(cmp({ warnings: ["the baseline's draft has no stage bg"] }))).toBe('nothing')
  })
  it('is clean only with measures compared and none past the threshold', () => {
    expect(verdictOf(cmp({ deltas: [delta] }))).toBe('clean')
    expect(verdictOf(cmp({ deltas: [{ ...delta, regression: true }], regressions: 1 }))).toBe('regressed')
  })
})

describe('budgets and deltas', () => {
  it('judges a preset by its median, and only against a budget it has', () => {
    const p = { preset: 'draft', images: 2, failed: 0, seconds: { n: 2, median: 12, p90: 13, max: 13 }, over_budget: 1 }
    expect(budgetState({ ...p, budget_seconds: 10 })).toBe('over')
    expect(budgetState({ ...p, budget_seconds: 20 })).toBe('ok')
    expect(budgetState(p)).toBeUndefined()
  })

  it('names and formats each kind of delta', () => {
    const d = { preset: 'draft', stage: 'shape', metric: 'peak_vram_bytes' as const, baseline: 0, current: 0, pct: 12.5, regression: true }
    expect(deltaName(d)).toBe('draft · shape · peak VRAM')
    expect(deltaValue(d, 6 * 2 ** 30)).toBe('6.0 GB')
    expect(deltaName({ ...d, stage: undefined, metric: 'seconds' })).toBe('draft · image to GLB')
    expect(deltaName({ ...d, preset: undefined, stage: undefined, metric: 'core_overhead' })).toBe('Core overhead per job')
    expect(deltaValue({ ...d, metric: 'core_overhead' }, 0.042)).toBe('42 ms')
    expect(pct(12.5)).toBe('+12.5%')
    expect(pct(-3)).toBe('-3.0%')
    expect(pct(null)).toBe('—')
  })
})
