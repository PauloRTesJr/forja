import { describe, expect, it } from 'vitest'
import type { Stage } from './api'
import { describeMetric, describeMetrics, measureLines, metricName, metricValue, stageMetrics } from './quality'

const stage = (status: Stage['status'], ...metrics: (Record<string, number> | undefined)[]): Stage => ({
  id: 'match', use: 'texture.match', status, seconds: 0, step: 0, total: 0,
  outputs: metrics.map((m, i) => ({ hash: `blake3:${i}`, kind: 'mesh/glb', metrics: m })),
})

// The same cases as core/internal/quality/quality_test.go: the CLI and the app word metrics alike.
describe('describeMetric', () => {
  it.each([
    ['match_iou', 0.93, 'reference fit 93%'],
    ['match_reprojected', 0.41, 'reference projected onto 41% of the texture'],
    ['match_left_iou', 0.62, 'left view fit 62%'],
    ['match_back_iou', 0.5, 'back view fit 50%'],
    ['bake_hit_ratio', 0.99, 'bake reached the high-poly on 99% of texels'],
    ['bake_cage', 0.05, 'bake cage 5.0% of the size'],
    ['uv_coverage', 0.71, 'UV space used 71%'],
    ['lod_open_edges', 0, 'LOD cracks: 0 open edges'],
    ['lod_open_edges', 1, 'LOD cracks: 1 open edge'],
    ['lod_open_edges', 137, 'LOD cracks: 137 open edges'],
    ['something_new', 3, 'something_new 3'],
    ['something_new', 0.25, 'something_new 0.25'],
  ])('%s %s reads %s', (key, value, words) => {
    expect(describeMetric(key, value)).toBe(words)
  })
})

describe('describeMetrics', () => {
  it('puts the side views after the front fit and the unknown keys last', () => {
    expect(
      describeMetrics({ zeta: 1, alpha: 2, uv_coverage: 0.7, match_right_iou: 0.8, match_iou: 0.9, match_left_iou: 0.6, match_reprojected: 0.4 }),
    ).toEqual([
      'reference fit 90%',
      'left view fit 60%',
      'right view fit 80%',
      'reference projected onto 40% of the texture',
      'UV space used 70%',
      'alpha 2',
      'zeta 1',
    ])
  })

  it('says nothing for a stage that measured nothing', () => {
    expect(describeMetrics(undefined)).toEqual([])
    expect(describeMetrics({})).toEqual([])
  })
})

describe('a stage', () => {
  it('merges the metrics of its outputs and words them once it has succeeded', () => {
    const s = stage('succeeded', { match_iou: 0.9, uv_coverage: 0.5 }, undefined, { uv_coverage: 0.7 })
    expect(stageMetrics(s)).toEqual({ match_iou: 0.9, uv_coverage: 0.7 })
    expect(measureLines(s)).toEqual(['reference fit 90%', 'UV space used 70%'])
  })

  it('shows nothing while it runs, or when it measured nothing', () => {
    expect(measureLines(stage('running', { match_iou: 0.9 }))).toEqual([])
    expect(measureLines(stage('succeeded', undefined))).toEqual([])
  })
})

describe('comparison rows', () => {
  it('names a measure and gives its numbers in its own unit', () => {
    expect(metricName('match_iou')).toBe('reference fit')
    expect(metricName('match_left_iou')).toBe('left view fit')
    expect(metricName('lod_open_edges')).toBe('LOD open edges')
    expect(metricValue('match_iou', 0.93)).toBe('93%')
    expect(metricValue('lod_open_edges', 4)).toBe('4')
    expect(metricValue('bake_cage', 0.05)).toBe('5.0%')
  })
})
