// The Queue's view of the jobs: which one runs, the order of the rest, and what each row says.
import { describe, expect, it } from 'vitest'
import type { Job, Stage, Status } from './api'
import {
  announcement,
  failureOf,
  leadJob,
  olderThan,
  outcomeLine,
  placeLine,
  presetName,
  progressText,
  queueSummary,
  rowState,
  runningLine,
  secondsLeft,
  sections,
  summarize,
  upsertJob,
  withOlderJobs,
  withoutJobs,
} from './jobs'

const stage = (use: string, status: Status, extra: Partial<Stage> = {}): Stage => ({
  id: use, use, status, outputs: [], seconds: 0, step: 0, total: 0, ...extra,
})
const at = (minute: number): string => new Date(Date.UTC(2026, 10, 9, 12, minute)).toISOString()
const job = (id: string, status: Status, minute: number, stages: Stage[] = [], extra: Partial<Job> = {}): Job => ({
  id, name: id, preset: 'mobile-prop', input: 'i', priority: 'batch', status, created_at: at(minute), stages, ...extra,
})
const fresh = (): Stage[] => [stage('background-removal', 'queued'), stage('shape', 'queued'), stage('texture', 'queued')]
const ids = (jobs: Job[]): string[] => jobs.map((j) => j.id)

describe('the job list', () => {
  it('puts a job it did not hold where its creation time says, newest first', () => {
    const list = [job('c', 'queued', 3), job('a', 'succeeded', 1)]
    expect(ids(upsertJob(list, job('d', 'queued', 4)))).toEqual(['d', 'c', 'a'])
    // A retried job keeps the time of the one it retries; an old job heard of late does not jump up.
    expect(ids(upsertJob(list, job('b', 'queued', 2)))).toEqual(['c', 'b', 'a'])
    expect(ids(upsertJob(list, job('z', 'running', 0)))).toEqual(['c', 'a', 'z'])
  })

  it('never takes a finished job back to running from an older snapshot', () => {
    const done = job('a', 'succeeded', 1)
    const list = [done]
    expect(upsertJob(list, job('a', 'running', 1))).toBe(list)
    const failed = job('a', 'failed', 1)
    expect(upsertJob(list, failed)[0]).toBe(failed)
  })

  it('drops removed jobs and keeps its identity when none was there', () => {
    const list = [job('b', 'failed', 2), job('a', 'succeeded', 1)]
    expect(ids(withoutJobs(list, ['b'])!)).toEqual(['a'])
    expect(withoutJobs(list, ['x'])).toBe(list)
    expect(withoutJobs(undefined, ['x'])).toBeUndefined()
  })

  it('adds a page of older jobs after what it holds, without doubles', () => {
    const list = [job('c', 'running', 3), job('b', 'succeeded', 2)]
    const stale = job('b', 'running', 2)
    const merged = withOlderJobs(list, [stale, job('a', 'succeeded', 1)])
    expect(ids(merged)).toEqual(['c', 'b', 'a'])
    expect(merged[1]!.status).toBe('succeeded')
    expect(withOlderJobs(list, [stale])).toBe(list)
  })

  it('asks for the page before its oldest job, that millisecond included', () => {
    expect(olderThan([job('b', 'succeeded', 2), job('a', 'succeeded', 1)])).toBe(Date.parse(at(1)) + 1)
    expect(olderThan([])).toBeUndefined()
  })
})

describe('which job runs', () => {
  const batch = (): Job[] => [
    job('d', 'queued', 4, fresh()),
    job('c', 'running', 3, fresh()),
    job('b', 'running', 2, [stage('background-removal', 'succeeded'), stage('shape', 'running'), stage('texture', 'queued')]),
    job('a', 'running', 1, [stage('background-removal', 'succeeded'), stage('shape', 'succeeded'), stage('texture', 'running')]),
    job('z', 'succeeded', 0, [], { finished_at: at(5) }),
  ]

  it('shows one job as running in a batch: the one furthest along', () => {
    expect(leadJob(batch())?.id).toBe('a')
    expect(leadJob([job('q', 'queued', 1)])).toBeUndefined()
  })

  it('keeps the running row with the same job while that job runs', () => {
    expect(leadJob(batch(), 'b')?.id).toBe('b')
    // It ended: the row passes to the next one.
    expect(leadJob(batch(), 'z')?.id).toBe('a')
  })

  it('starts with the oldest job when none has begun', () => {
    expect(leadJob([job('n', 'running', 2, fresh()), job('o', 'running', 1, fresh())])?.id).toBe('o')
  })

  it('orders the rest as they will run, and the finished by when they ended', () => {
    const jobs = [
      ...batch(),
      job('w', 'waiting', 6, fresh()),
      job('e', 'queued', 5, fresh()),
      job('y', 'failed', 0, [], { finished_at: at(7) }),
      job('x', 'cancelled', 0, [], { finished_at: at(6) }),
    ]
    const s = sections(jobs)
    expect(ids(s.asking)).toEqual(['w'])
    expect(s.running?.id).toBe('a')
    expect(ids(s.next)).toEqual(['b', 'c', 'd', 'e'])
    expect(ids(s.finished)).toEqual(['y', 'x', 'z'])
    expect(queueSummary(s)).toBe('1 forging · 4 queued · 1 needs you')
    expect(queueSummary(sections([job('z', 'succeeded', 0)]))).toBe('Nothing forging')
    expect(queueSummary(sections([job('w', 'waiting', 1), job('v', 'waiting', 2)]))).toBe('2 need you')
  })
})

describe('what a row says', () => {
  it('reads a job in a slot that is not the running one as queued', () => {
    const flying = job('b', 'running', 2, fresh())
    expect(rowState(flying)).toBe('queued')
    expect(rowState(flying, { lead: true })).toBe('starting')
    expect(rowState(job('a', 'running', 1, [stage('shape', 'running')]), { lead: true })).toBe('running')
    expect(rowState(job('w', 'waiting', 1))).toBe('asking')
    expect(rowState(job('q', 'queued', 1))).toBe('queued')
  })

  it('says cancelling from the click until the job has ended', () => {
    expect(rowState(job('a', 'running', 1, [stage('shape', 'running')]), { lead: true, cancelling: true })).toBe('cancelling')
    expect(rowState(job('q', 'queued', 1), { cancelling: true })).toBe('cancelling')
    expect(rowState(job('a', 'cancelled', 1), { cancelling: true })).toBe('cancelled')
    expect(rowState(job('a', 'succeeded', 1), { cancelling: true })).toBe('done')
  })

  it('names the stage, its step and its place, and the next stage between two', () => {
    const j = job('a', 'running', 1, [stage('shape', 'succeeded'), stage('texture', 'running', { step: 12, total: 30 }), stage('export', 'queued')])
    expect(runningLine(j)).toBe('Texture, step 12 of 30 · stage 2 of 3')
    const between = job('a', 'running', 1, [stage('shape', 'succeeded'), stage('texture', 'queued'), stage('export', 'queued')])
    expect(runningLine(between)).toBe('Texture · stage 2 of 3')
    const hosted = job('a', 'running', 1, [stage('shape', 'running', { provider: 'fal', provider_label: 'fal.ai', note: "Waiting in fal.ai's queue, position 2" })])
    expect(runningLine(hosted)).toBe("Shape on fal.ai: Waiting in fal.ai's queue, position 2 · stage 1 of 1")
  })

  it('gives a queued job its place in line', () => {
    expect([1, 2, 3, 4, 11, 12, 13, 21, 22, 103].map(placeLine)).toEqual([
      'Next', '2nd in line', '3rd in line', '4th in line', '11th in line', '12th in line', '13th in line', '21st in line', '22nd in line', '103rd in line',
    ])
  })

  it('reports the time from start to end, not the sum of the stages', () => {
    const stages = [stage('shape', 'succeeded', { seconds: 2 }), stage('texture', 'succeeded', { seconds: 2.9 })]
    expect(outcomeLine(job('a', 'succeeded', 1, stages, { duration_seconds: 13.4 }))).toBe('Took 13 s')
    expect(summarize(job('a', 'succeeded', 1, stages, { duration_seconds: 13.4 })).headline).toBe('Finished in 13 s')
    // An older core does not say: the stages' own times are the best there is.
    expect(outcomeLine(job('a', 'succeeded', 1, stages))).toBe('Took 4.9 s')
  })

  it('says where a failed or cancelled job stopped', () => {
    expect(outcomeLine(job('a', 'failed', 1, [stage('shape', 'succeeded'), stage('texture', 'failed')]))).toBe('Stopped at Texture')
    expect(outcomeLine(job('a', 'failed', 1, [stage('shape', 'cancelled')]))).toBe('Did not start')
    expect(outcomeLine(job('a', 'failed', 1, [stage('shape', 'succeeded'), stage('texture', 'cancelled')]))).toBe('Stopped after 1 of 2 stages')
    expect(outcomeLine(job('a', 'cancelled', 1, [stage('shape', 'succeeded'), stage('texture', 'cancelled')]))).toBe('Cancelled at Texture · 1 of 2 stages done')
    expect(outcomeLine(job('a', 'cancelled', 1, [stage('shape', 'cancelled')]))).toBe('Cancelled before it started')
  })

  it('knows the time left only from an estimate that has not passed', () => {
    const j = job('a', 'running', 1, [], { estimate_seconds: 90 })
    expect(secondsLeft(j, 30)).toBe(60)
    expect(secondsLeft(j, 95)).toBeUndefined()
    expect(secondsLeft(j, undefined)).toBeUndefined()
    expect(secondsLeft(job('a', 'running', 1), 30)).toBeUndefined()
  })

  it('shows a preset by its name', () => {
    expect(presetName('mobile-prop', [{ id: 'mobile-prop', name: 'Mobile prop' }])).toBe('Mobile prop')
    expect(presetName('hero_character', undefined)).toBe('Hero character')
  })

  it('describes progress and changes for a screen reader', () => {
    const stages = [stage('shape', 'succeeded'), stage('texture', 'running'), stage('export', 'queued')]
    expect(progressText(stages)).toBe('Texture, 1 of 3 stages done')
    expect(progressText([stage('shape', 'queued')])).toBe('0 of 1 stages done')
    expect(announcement('running', job('chest', 'succeeded', 1))).toBe('chest finished.')
    expect(announcement('running', job('chest', 'failed', 1))).toBe('chest failed.')
    expect(announcement('queued', job('chest', 'waiting', 1))).toBe('chest needs your answer.')
    expect(announcement('queued', job('chest', 'running', 1))).toBeUndefined()
    expect(announcement(undefined, job('chest', 'succeeded', 1))).toBeUndefined()
  })
})

describe('why a job failed', () => {
  it('is nothing for a job the user cancelled, whatever an older core wrote', () => {
    expect(failureOf(job('a', 'cancelled', 1, [], { error: 'cancelled' }))).toBeUndefined()
    expect(failureOf(job('a', 'succeeded', 1))).toBeUndefined()
  })

  it("shows the core's sentence and keeps the raw error apart", () => {
    const j = job('a', 'failed', 1, [stage('shape', 'failed', { error: 'The shape model ran out of memory.', error_detail: 'torch.OutOfMemoryError: CUDA out of memory' })])
    expect(failureOf(j)).toEqual({ summary: 'The shape model ran out of memory.', detail: 'torch.OutOfMemoryError: CUDA out of memory' })
    expect(failureOf(job('a', 'failed', 1, [], { error: 'Forja closed while this job was running, so it did not finish. Retry it.' }))).toEqual({
      summary: 'Forja closed while this job was running, so it did not finish. Retry it.',
    })
  })

  it('turns a raw error from an older core into a sentence, with the raw text as the detail', () => {
    const unreadable = "UnidentifiedImageError: cannot identify image file 'C:\\\\data\\\\store\\\\objects\\\\d4\\\\f65384'"
    expect(failureOf(job('a', 'failed', 1, [stage('background-removal', 'failed', { error: unreadable })]))).toEqual({
      summary: 'The image could not be read. It may be damaged, or not a PNG, JPEG or WebP file.',
      detail: unreadable,
    })
    const lost = 'provider local: event stream for run bf3bd8d7555c47e8 kept failing: engine: Request failed (404): no run bf3bd8d7555c47e8'
    expect(failureOf(job('a', 'failed', 1, [], { error: lost }))?.summary).toBe('The engine stopped while this job ran. Retry it.')
    const other = 'ValueError: mesh has no faces'
    expect(failureOf(job('a', 'failed', 1, [stage('mesh.cleanup', 'failed', { error: other })]))).toEqual({
      summary: 'Cleanup failed. The details say why.',
      detail: other,
    })
    expect(failureOf(job('a', 'failed', 1))?.summary).toBe('It failed without saying why. Retry it.')
  })
})
