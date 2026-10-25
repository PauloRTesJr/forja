import { describe, expect, it } from 'vitest'
import type { Job, Stage } from './api'
import { applyProgress, jobFraction, phaseState, phasesOf, previewOf, stageTime, summarize, upsertJob } from './jobs'
import { count, roman, seconds, skeletonName, stageLabel, taskNoun } from './format'

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

describe('phases', () => {
  const uses = ['background-removal', 'framing', 'shape', 'mesh.cleanup', 'mesh.decimate', 'mesh.uv', 'texture', 'texture.match', 'bake', 'rig', 'mesh.lods', 'export']

  it('groups a full pipeline into its phases in order', () => {
    const phases = phasesOf(uses.map((u) => stage(u, 'queued')))
    expect(phases.map((p) => [p.label, p.stages.length])).toEqual([
      ['Prepare', 2],
      ['Shape', 4],
      ['Paint', 3],
      ['Rig', 1],
      ['Finish', 2],
    ])
  })

  it('gives rigging its own phase between painting and finishing', () => {
    const phases = phasesOf(['texture', 'bake', 'rig', 'mesh.lods', 'export'].map((u) => stage(u, 'queued')))
    expect(phases.map((p) => [p.label, p.stages.map((s) => s.id)])).toEqual([
      ['Paint', ['texture', 'bake']],
      ['Rig', ['rig']],
      ['Finish', ['mesh.lods', 'export']],
    ])
  })

  it('puts an unknown stage type in the phase before it', () => {
    const phases = phasesOf([stage('shape', 'queued'), stage('mesh.remesh', 'queued'), stage('export', 'queued')])
    expect(phases.map((p) => p.stages.map((s) => s.id))).toEqual([['shape', 'mesh.remesh'], ['export']])
  })

  it('reads a phase as failed, running, done or waiting', () => {
    const [prepare, shape] = phasesOf([
      stage('background-removal', 'succeeded'),
      stage('framing', 'succeeded'),
      stage('shape', 'running'),
      stage('mesh.cleanup', 'queued'),
    ])
    expect(phaseState(prepare!)).toBe('done')
    expect(phaseState(shape!)).toBe('running')
    expect(phaseState(phasesOf([stage('texture', 'failed')])[0]!)).toBe('failed')
    expect(phaseState(phasesOf([stage('export', 'queued')])[0]!)).toBe('waiting')
  })
})

describe('summarize', () => {
  it('names the running stage and its place', () => {
    const j = job('a', [stage('shape', 'succeeded'), stage('texture', 'running', { step: 1, total: 2 }), stage('export', 'queued')])
    expect(summarize(j)).toEqual({ headline: 'Running Texture', note: 'stage 2 of 3 · 50% overall' })
  })

  it('says where a stage runs off this computer, and what its provider says', () => {
    const j = job('a', [
      stage('shape', 'running', { provider: 'fal', provider_label: 'fal.ai', note: "Waiting in fal.ai's queue, position 2" }),
      stage('export', 'queued'),
    ])
    expect(summarize(j)).toEqual({ headline: 'Running Shape on fal.ai', note: "Waiting in fal.ai's queue, position 2 · 17% overall" })
  })

  it('names where a failed job stopped', () => {
    const j = { ...job('a', [stage('framing', 'succeeded'), stage('shape', 'failed')]), status: 'failed' as const }
    expect(summarize(j)).toEqual({ headline: 'Stopped at Shape', note: 'stage 2 of 2' })
  })

  it('counts time spent and stages reused from cache', () => {
    const j = {
      ...job('a', [stage('shape', 'succeeded', { cached: true, seconds: 30 }), stage('texture', 'succeeded', { seconds: 95 })]),
      status: 'succeeded' as const,
    }
    expect(summarize(j)).toEqual({ headline: 'Finished in 1 min 35 s', note: '1 of 2 stages reused from cache' })
    expect(stageTime(j.stages[0]!)).toBe('cached')
    expect(stageTime(stage('x', 'running', { step: 1, total: 4 }))).toBe('25%')
  })
})

describe('format', () => {
  it('formats durations and counts', () => {
    expect(seconds(0.25)).toBe('250 ms')
    expect(seconds(4.2)).toBe('4.2 s')
    expect(seconds(95)).toBe('1 min 35 s')
    expect(count(4998)).toBe('5.0k')
  })

  it('names stage types as nouns in a sentence', () => {
    expect(taskNoun('background-removal')).toBe('background removal')
    expect(taskNoun('rig')).toBe('rigging')
    expect(stageLabel('rig')).toBe('Rig')
    expect([skeletonName({ skeleton: 'humanoid' }), skeletonName({ skeleton: 'generic' })]).toEqual(['Humanoid', 'Generic'])
  })

  it('numbers cards in roman numerals', () => {
    expect([1, 4, 9, 14, 40].map(roman)).toEqual(['I', 'IV', 'IX', 'XIV', 'XL'])
  })
})
