import { describe, expect, it } from 'vitest'
import { editsToSets, materialName, percent, shownValues, valueText, withEdit, withoutSent, type Edits, type MaterialRow } from './materials'

const row = (index: number, over: Partial<MaterialRow> = {}): MaterialRow => ({
  index,
  texture: 0,
  color: '#8b5a2b',
  share: 0.5,
  roughness: 0.8,
  metallic: 0,
  ...over,
})

describe('edits', () => {
  it('keeps what was moved, per material and field, clamped to 0..1', () => {
    let e: Edits = new Map()
    e = withEdit(e, 2, 'roughness', 0.3)
    e = withEdit(e, 2, 'metallic', 1.4)
    e = withEdit(e, 0, 'metallic', -1)
    expect(editsToSets(e)).toEqual([
      { index: 0, metallic: 0 },
      { index: 2, roughness: 0.3, metallic: 1 },
    ])
  })

  it('does not change the edits it is given', () => {
    const before: Edits = new Map()
    withEdit(before, 1, 'roughness', 0.5)
    expect(before.size).toBe(0)
  })

  it('shows the moved value over the core one, field by field', () => {
    const e = withEdit(new Map(), 1, 'roughness', 0.25)
    expect(shownValues(row(1), e)).toEqual({ roughness: 0.25, metallic: 0 })
    expect(shownValues(row(2), e)).toEqual({ roughness: 0.8, metallic: 0 })
  })

  it('drops what the core took and keeps what moved again meanwhile', () => {
    let e: Edits = new Map()
    e = withEdit(e, 1, 'roughness', 0.2)
    e = withEdit(e, 1, 'metallic', 1)
    const sent = editsToSets(e)
    // The slider moves again while the request is in flight.
    e = withEdit(e, 1, 'roughness', 0.4)
    const left = withoutSent(e, sent)
    expect(editsToSets(left)).toEqual([{ index: 1, roughness: 0.4 }])
    expect(withoutSent(left, [{ index: 1, roughness: 0.4 }]).size).toBe(0)
  })
})

describe('words', () => {
  it('writes shares as whole percents, never 0%', () => {
    expect(percent(0.5)).toBe('50%')
    expect(percent(0.204)).toBe('20%')
    expect(percent(0.004)).toBe('1%')
  })

  it('writes values with two decimals', () => {
    expect(valueText(0.8)).toBe('0.80')
    expect(valueText(1)).toBe('1.00')
  })

  it('names a material for a screen reader by its place, color and share', () => {
    expect(materialName(row(3, { color: '#d4af37', share: 0.2 }), 1)).toBe('Material 2, #d4af37, 20%')
  })
})
