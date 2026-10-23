import { describe, expect, it } from 'vitest'
import type { Pick } from '../lib/api'
import { money } from '../lib/format'
import { estimate } from './Composer'

const pick = (p: Partial<Pick>): Pick => ({ stage: 's', use: 'shape', ...p })

describe('estimate', () => {
  it('adds up paid steps per image and ignores free and skipped ones', () => {
    const plan = [
      pick({ stage: 'bg', use: 'background-removal', model: 'birefnet' }),
      pick({ stage: 'shape', model: 'fal-ai/trellis-2', provider: 'fal', paid: true, price: { amount: 0.3, unit: 'generation', currency: 'USD' } }),
      pick({ stage: 'paint', use: 'texture', skipped: true, by: 'shape', paid: true, price: { amount: 9, unit: 'generation', currency: 'USD' } }),
    ]
    expect(estimate(plan)).toEqual({ usd: 0.3, unknown: [], hosts: ['fal'] })
  })

  it('names models without a fixed price per generation', () => {
    const plan = [
      pick({ model: 'x', model_name: 'X', provider: 'fal', paid: true, price: { amount: 0.01, unit: 'second', currency: 'USD' } }),
      pick({ stage: 'paint', model: 'y', model_name: 'Y', provider: 'fal', paid: true, price: { amount: 0.015, unit: 'units', currency: 'USD' } }),
      pick({ stage: 'lods', model: 'z', model_name: 'Z', provider: 'fal', paid: true }),
    ]
    expect(estimate(plan)).toEqual({ usd: 0, unknown: ['X', 'Y', 'Z'], hosts: ['fal'] })
  })
})

describe('money', () => {
  it('says per image only for a price per generation', () => {
    expect(money({ amount: 0.4, unit: 'generation', currency: 'USD' })).toBe('$0.40 per image')
    expect(money({ amount: 0.015, unit: 'units', currency: 'USD' })).toBe('$0.015 per unit')
    expect(money(undefined)).toBe('price unknown')
  })
})
