import { describe, expect, it } from 'vitest'
import type { Asset } from './api'
import { baseName, clickSelect, dragIds, fileStem, rangeBetween, withoutAsset } from './selection'

const ids = ['a', 'b', 'c', 'd', 'e']

describe('rangeBetween', () => {
  it('spans the anchor and the target in either direction', () => {
    expect(rangeBetween(ids, 'b', 'd')).toEqual(['b', 'c', 'd'])
    expect(rangeBetween(ids, 'd', 'b')).toEqual(['b', 'c', 'd'])
  })
  it('is just the target when there is no anchor, or it left the list', () => {
    expect(rangeBetween(ids, null, 'c')).toEqual(['c'])
    expect(rangeBetween(ids, 'gone', 'c')).toEqual(['c'])
    expect(rangeBetween(ids, 'a', 'gone')).toEqual([])
  })
})

describe('clickSelect', () => {
  it('toggles one card, and Shift adds the range without dropping what was chosen', () => {
    let sel = clickSelect(new Set(), ids, null, 'b', false)
    expect([...sel]).toEqual(['b'])
    sel = clickSelect(sel, ids, 'b', 'e', false)
    sel = clickSelect(sel, ids, 'e', 'c', true)
    expect([...sel].sort()).toEqual(['b', 'c', 'd', 'e'])
    expect([...clickSelect(sel, ids, 'c', 'c', false)].sort()).toEqual(['b', 'd', 'e'])
  })
})

describe('dragIds', () => {
  it('carries the selection when the dragged card is in it, else that card alone', () => {
    const sel = new Set(['d', 'b'])
    expect(dragIds(ids, sel, 'd')).toEqual(['b', 'd'])
    expect(dragIds(ids, sel, 'a')).toEqual(['a'])
    expect(dragIds(ids, new Set(), 'c')).toEqual(['c'])
  })
})

describe('helpers', () => {
  it('drops a deleted asset from a list and leaves an unloaded one alone', () => {
    const list = [{ id: 'x' }, { id: 'y' }] as Asset[]
    expect(withoutAsset(list, 'x')?.map((a) => a.id)).toEqual(['y'])
    expect(withoutAsset(undefined, 'x')).toBeUndefined()
  })
  it('names files after assets on any OS', () => {
    expect(baseName('C:\\Users\\me/out\\chest.fbx')).toBe('chest.fbx')
    expect(baseName('/home/me/chest.obj')).toBe('chest.obj')
    expect(fileStem('C:\\art\\Owl: final.png')).toBe('Owl- final')
    expect(fileStem('chest')).toBe('chest')
    expect(fileStem('')).toBe('asset')
  })
})
