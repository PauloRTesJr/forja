import { describe, expect, it } from 'vitest'
import type { Preset } from '../lib/api'
import { money } from '../lib/format'
import { optionalOverrides } from './Composer'

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
