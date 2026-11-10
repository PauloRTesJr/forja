import { describe, expect, it } from 'vitest'
import { activity, isPreparing, queuedMessage, rowProgress, rowState, rowStatus, runningCaption, stillTracked } from './activity'
import type { Job, Stage } from './api'

let n = 0
const job = (status: Job['status'], extra: Partial<Job> = {}): Job => ({
  id: `j${++n}`,
  name: 'knight',
  preset: 'mobile-prop',
  input: 'blake3:in',
  priority: 'interactive',
  status,
  created_at: '',
  stages: [],
  ...extra,
})
const sheet = (s: Partial<NonNullable<Job['sheet']>> = {}): Job['sheet'] => ({ mode: 'ask', checked: false, found: false, size: [1, 1], ...s })
const stage = (use: string, status: Stage['status'], extra: Partial<Stage> = {}): Stage => ({
  id: use,
  use,
  status,
  outputs: [],
  seconds: 0,
  step: 0,
  total: 0,
  ...extra,
})

describe('isPreparing', () => {
  it('holds a job from its submit until it waits or goes on', () => {
    expect(isPreparing(job('queued', { sheet: sheet() }))).toBe(true)
    expect(isPreparing(job('running', { sheet: sheet({ checked: true, found: true }) }))).toBe(true) // about to wait
    expect(isPreparing(job('waiting', { sheet: sheet({ checked: true, found: true }) }))).toBe(false)
    expect(isPreparing(job('running', { sheet: sheet({ checked: true }) }))).toBe(false)
    expect(isPreparing(job('queued', { sheet: sheet({ checked: true, found: true, answer: 'split' }) }))).toBe(false)
  })

  it('leaves out jobs that bring their views or keep sheets whole', () => {
    expect(isPreparing(job('queued'))).toBe(false)
    expect(isPreparing(job('queued', { sheet: sheet({ mode: 'keep' }) }))).toBe(false)
  })
})

describe('rowState', () => {
  it('never calls a cancelled or failed job queued', () => {
    // Cancelled while its image was looked at: the sheet was never checked.
    expect(rowState(job('cancelled', { sheet: sheet() }))).toBeUndefined()
    // Cancelled while it waited for an answer.
    expect(rowState(job('cancelled', { sheet: sheet({ checked: true, found: true }) }))).toBeUndefined()
    expect(rowState(job('failed', { sheet: sheet() }))).toBe('failed')
  })

  it('follows a job from preparation to its end', () => {
    expect(rowState(job('queued', { sheet: sheet() }))).toBe('preparing')
    expect(rowState(job('waiting', { sheet: sheet({ checked: true, found: true }) }))).toBe('asking')
    expect(rowState(job('queued', { sheet: sheet({ checked: true }) }))).toBe('queued')
    expect(rowState(job('running', { sheet: sheet({ checked: true }) }))).toBe('running')
    expect(rowState(job('succeeded'))).toBe('done')
  })
})

describe('activity', () => {
  it('lists jobs started here, and any job that prepares or asks, whoever started it', () => {
    const mine = job('running')
    const other = job('running')
    const asks = job('waiting', { sheet: sheet({ checked: true, found: true }) })
    const prepares = job('queued', { sheet: sheet() })
    const { rows, more } = activity([mine, other, asks, prepares], [mine.id])
    expect(rows.map((r) => [r.job.id, r.state])).toEqual([
      [asks.id, 'asking'],
      [prepares.id, 'preparing'],
      [mine.id, 'running'],
    ])
    expect(more).toBe(0)
  })

  it('drops a cancelled job and keeps a failed one', () => {
    const cancelled = job('cancelled')
    const failed = job('failed', { error: 'No subject was found in the image.' })
    const { rows } = activity([cancelled, failed], [cancelled.id, failed.id])
    expect(rows.map((r) => r.state)).toEqual(['failed'])
    expect(rowStatus(rows[0]!)).toBe('No subject was found in the image.')
  })

  it('caps the rows and counts the rest, but never hides a job that asks', () => {
    const running = Array.from({ length: 6 }, () => job('running'))
    const capped = activity(running, running.map((j) => j.id), 3)
    expect(capped.rows).toHaveLength(3)
    expect(capped.more).toBe(3)
    // The list's order (newest first) is kept within a state.
    expect(capped.rows.map((r) => r.job.id)).toEqual(running.slice(0, 3).map((j) => j.id))

    const asking = Array.from({ length: 5 }, () => job('waiting', { sheet: sheet({ checked: true, found: true }) }))
    const all = activity([...running, ...asking], running.map((j) => j.id), 3)
    expect(all.rows.filter((r) => r.state === 'asking')).toHaveLength(5)
    expect(all.more).toBe(6)
  })

  it('has nothing to show before the jobs arrive', () => {
    expect(activity(undefined, ['x'])).toEqual({ rows: [], more: 0 })
  })
})

describe('stillTracked', () => {
  it('keeps only the jobs that have not ended', () => {
    const running = job('running')
    const waiting = job('waiting')
    const done = job('succeeded')
    const failed = job('failed')
    expect(stillTracked([running.id, waiting.id, done.id, failed.id, 'gone'], [running, waiting, done, failed])).toEqual([running.id, waiting.id])
  })
})

describe('rowStatus and rowProgress', () => {
  it('say what a job being prepared is doing', () => {
    const waiting = { job: job('queued', { sheet: sheet() }), state: 'preparing' as const }
    expect(rowStatus(waiting)).toBe('Waiting for the engine')
    expect(rowProgress(waiting)).toBeUndefined()

    const removing = { job: job('running', { sheet: sheet(), stages: [stage('background-removal', 'running', { step: 1, total: 4 })] }), state: 'preparing' as const }
    expect(rowStatus(removing)).toBe('Removing the background')
    expect(rowProgress(removing)).toBe(0.25)

    const looking = { job: job('running', { sheet: sheet(), stages: [stage('background-removal', 'succeeded')] }), state: 'preparing' as const }
    expect(rowStatus(looking)).toBe('Looking for several views of the subject')
  })

  it('prefer the failed stage’s own words', () => {
    const j = job('failed', { error: 'job error', stages: [stage('shape', 'failed', { error: 'The shape model ran out of memory.' })] })
    expect(rowStatus({ job: j, state: 'failed' })).toBe('The shape model ran out of memory.')
  })

  it('count the views a sheet proposes', () => {
    const j = job('waiting', {
      sheet: sheet({ checked: true, found: true, parts: [{ box: [0, 0, 1, 1], view: 'front' }, { box: [0, 0, 1, 1], view: 'back' }, { box: [0, 0, 1, 1], view: '' }] }),
    })
    expect(rowStatus({ job: j, state: 'asking' })).toBe('Shows 2 views of one subject. Check them to go on.')
  })
})

describe('queuedMessage', () => {
  it('names one job and counts several', () => {
    expect(queuedMessage([{ name: 'knight' }])).toBe('knight added to the queue')
    expect(queuedMessage([{ name: 'a' }, { name: 'b' }, { name: 'c' }])).toBe('3 images added to the queue')
  })
})

describe('rowStatus for jobs that wait or ended', () => {
  it('adds to the pill instead of repeating it', () => {
    expect(rowStatus({ job: job('queued'), state: 'queued' })).toBe('Waiting for its turn')
    expect(rowStatus({ job: job('succeeded', { duration_seconds: 3.2 }), state: 'done' })).toBe('Took 3.2 s')
    expect(rowStatus({ job: job('succeeded'), state: 'done' })).toBe('Finished')
  })
})

describe('runningCaption', () => {
  const running = (extra: Partial<Job> = {}): Job => job('running', { stages: [stage('background-removal', 'succeeded'), stage('shape', 'running'), stage('texture', 'queued')], ...extra })

  it('names the stage, the time it has run and the time that is left', () => {
    expect(runningCaption(running({ estimate_seconds: 300 }), 72)).toBe('Shape · 1:12 · about 4 min left')
  })

  it('leaves out what it does not know', () => {
    expect(runningCaption(running(), undefined)).toBe('Shape')
    expect(runningCaption(running(), 5)).toBe('Shape · 0:05')
    // An estimate that has passed says nothing about what is left.
    expect(runningCaption(running({ estimate_seconds: 60 }), 90)).toBe('Shape · 1:30')
    expect(runningCaption(job('running'), 3)).toBe('Forging · 0:03')
  })
})
