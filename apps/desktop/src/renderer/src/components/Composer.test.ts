import { describe, expect, it } from 'vitest'
import { money } from '../lib/format'

describe('money', () => {
  it('says per image only for a price per generation', () => {
    expect(money({ amount: 0.4, unit: 'generation', currency: 'USD' })).toBe('$0.40 per image')
    expect(money({ amount: 0.015, unit: 'units', currency: 'USD' })).toBe('$0.015 per unit')
    expect(money(undefined)).toBe('price unknown')
  })
})
