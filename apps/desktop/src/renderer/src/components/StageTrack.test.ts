import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import type { Stage } from '../lib/api'
import { StageList } from './StageTrack'

const stage = (id: string, use: string, status: Stage['status'], metrics?: Record<string, number>, note?: string): Stage => ({
  id, use, status, seconds: 1, step: 0, total: 0,
  outputs: [{ hash: `blake3:${id}`, kind: 'mesh/glb', metrics, note }],
})

describe('StageList', () => {
  const html = (stages: Stage[]) => renderToStaticMarkup(createElement(StageList, { stages }))

  it('words what a finished stage measured under its name, beside its note', () => {
    const out = html([
      stage('match', 'texture.match', 'succeeded', { match_iou: 0.93, match_left_iou: 0.62 }, 'made a stand-in'),
      stage('lods', 'mesh.lods', 'succeeded', { lod_open_edges: 0 }),
    ])
    expect(out).toContain('<span class="stage-note warn">made a stand-in</span>')
    expect(out).toContain('<span class="stage-note measure">reference fit 93% · left view fit 62%</span>')
    expect(out).toContain('<span class="stage-note measure">LOD cracks: 0 open edges</span>')
  })

  it('shows nothing for a stage that is still running or measured nothing', () => {
    const out = html([stage('match', 'texture.match', 'running', { match_iou: 0.9 }), stage('uv', 'mesh.uv', 'succeeded')])
    expect(out).not.toContain('measure')
  })
})
