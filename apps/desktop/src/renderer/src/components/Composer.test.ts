import { describe, expect, it } from 'vitest'
import type { Pick } from '../lib/api'
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
    const plan = [pick({ model: 'x', model_name: 'X', provider: 'fal', paid: true, price: { amount: 0.01, unit: 'second', currency: 'USD' } })]
    expect(estimate(plan).unknown).toEqual(['X'])
  })
})
