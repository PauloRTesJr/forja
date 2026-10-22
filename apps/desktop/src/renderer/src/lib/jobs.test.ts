import { describe, expect, it } from 'vitest'
import type { Job, Stage } from './api'
import { applyProgress, jobFraction, previewOf, upsertJob } from './jobs'
import { count, seconds } from './format'

const stage = (id: string, status: Stage['status'], extra: Partial<Stage> = {}): Stage => ({
  id, use: id, status, key: '', cached: false, outputs: [], seconds: 0, step: 0, total: 0, ...extra,
})
const job = (id: string, stages: Stage[]): Job => ({
  id, name: id, preset: 'p', input: 'i', priority: 'batch', status: 'running', created_at: '', stages,
})

describe('upsertJob', () => {
  it('prepends new jobs and replaces known ones in place', () => {
    const a = job('a', [])
    const b = job('b', [])
    const list = upsertJob(upsertJob([], a), b)
    expect(list.map((j) => j.id)).toEqual(['b', 'a'])
    const a2 = { ...a, status: 'succeeded' as const }
    expect(upsertJob(list, a2)[1]).toBe(a2)
  })
})

describe('progress', () => {
  it('weights stages equally and reads step counts', () => {
    const j = job('a', [stage('x', 'succeeded'), stage('y', 'running', { step: 1, total: 4 })])
    expect(jobFraction(j)).toBeCloseTo((1 + 0.25) / 2)
    const [updated] = applyProgress([j], 'a', 'y', 3, 4)!
    expect(jobFraction(updated!)).toBeCloseTo((1 + 0.75) / 2)
  })

  it('picks the latest finished mesh as the preview', () => {
    const j = job('a', [
      stage('shape', 'succeeded', { outputs: [{ hash: 'h1', kind: 'mesh/glb' }] }),
      stage('paint', 'running'),
    ])
    expect(previewOf(j)).toBe('h1')
  })
})

describe('format', () => {
  it('formats durations and counts', () => {
    expect(seconds(0.25)).toBe('250 ms')
    expect(seconds(4.2)).toBe('4.2 s')
    expect(seconds(95)).toBe('1 min 35 s')
    expect(count(4998)).toBe('5.0k')
  })
})
