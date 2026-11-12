import { describe, expect, it } from 'vitest'
import type { Model, Preset } from '../lib/api'
import { money } from '../lib/format'
import { automaticPick, optionalOverrides } from './Composer'

describe('optionalOverrides', () => {
  const preset: Preset = {
    id: 'hero-character',
    name: 'Hero character',
    description: '',
    source: 'builtin',
    builtin: true,
    ready: true,
    plan: [],
    stages: [
      { id: 'cleanup', use: 'mesh.cleanup' },
      { id: 'remesh', use: 'mesh.remesh', optional: { label: 'Quad remesh with Blender', default: false, replaces: 'lowpoly' } },
      { id: 'lowpoly', use: 'mesh.decimate' },
      { id: 'match', use: 'texture.match', optional: { label: 'Match', default: true } },
    ],
  }

  it('sends each switch as the user left it', () => {
    const on = new Set(['remesh'])
    expect(optionalOverrides(preset, (s, d) => on.has(s) || (s !== 'match' && d))).toEqual({
      'remesh.enabled': true,
      'match.enabled': false,
    })
  })

  it('offers the denser mesh as a switch that is off until it is turned on', () => {
    const mobile: Preset = {
      ...preset,
      id: 'mobile-prop',
      stages: [
        { id: 'lowpoly', use: 'mesh.decimate' },
        { id: 'dense', use: 'mesh.decimate', optional: { label: 'Paint a denser mesh', default: false } },
        { id: 'paint', use: 'texture' },
      ],
    }
    expect(optionalOverrides(mobile, (_s, d) => d)).toEqual({ 'dense.enabled': false })
    expect(optionalOverrides(mobile, (s, d) => s === 'dense' || d)).toEqual({ 'dense.enabled': true })
  })

  it('keeps a step off where no provider can run it, even if switched on before', () => {
    const blocked = { ...preset, unavailable: { remesh: 'Blender 4.2 or newer was not found' } }
    expect(optionalOverrides(blocked, () => true)).toEqual({ 'remesh.enabled': false, 'match.enabled': true })
  })
})

describe('money', () => {
  it('says per image only for a price per generation', () => {
    expect(money({ amount: 0.4, unit: 'generation', currency: 'USD' })).toBe('$0.40 per image')
    expect(money({ amount: 0.015, unit: 'units', currency: 'USD' })).toBe('$0.015 per unit')
    expect(money(undefined)).toBe('price unknown')
  })
})

describe('automaticPick', () => {
  const model = (id: string, extra: Partial<Model> = {}): Model => ({
    id,
    name: id.toUpperCase(),
    family: 'f',
    tasks: ['shape'],
    license: { spdx: 'MIT', commercial: true, restrictions: [] },
    status: 'ready',
    device: 'cuda:0',
    provider: 'local',
    provider_label: 'This computer',
    needs_acceptance: false,
    uses: [],
    paid: false,
    ...extra,
  })

  it('is the highest-ranked model ready on this computer, as the core picks it', () => {
    const models = [model('triposg', { rank: 50 }), model('pixal3d', { rank: 90 }), model('trellis2', { rank: 95, status: 'not-installed', device: null })]
    expect(automaticPick(models, 'shape', false)?.id).toBe('pixal3d')
  })

  it('passes over stubs unless they are switched on, other stage types and other computers', () => {
    const models = [
      model('stub-shape', { stub: true, rank: 1 }),
      model('paint', { tasks: ['texture'], rank: 99 }),
      model('lan-shape', { provider: 'studio', rank: 99 }),
    ]
    expect(automaticPick(models, 'shape', false)).toBeUndefined()
    expect(automaticPick(models, 'shape', true)?.id).toBe('stub-shape')
  })

  it('settles a tie by id, so the answer does not change between reads', () => {
    expect(automaticPick([model('b', { rank: 5 }), model('a', { rank: 5 })], 'shape', false)?.id).toBe('a')
  })
})
