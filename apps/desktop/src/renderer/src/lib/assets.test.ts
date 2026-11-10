import { describe, expect, it } from 'vitest'
import type { Asset } from './api'
import { assetListKey, countLabel, flatten, isNameSortedList, lodCount, nameProblem, nextOffset, totalOf, withAsset, withExtension, withoutAssets, type AssetList } from './assets'

const asset = (id: string, over: Partial<Asset> = {}): Asset => ({ id, name: id, ...over }) as Asset
const paged = (pages: { ids: string[]; total?: number }[]): AssetList => ({
  pages: pages.map((p) => ({ items: p.ids.map((id) => asset(id)), total: p.total })),
  pageParams: pages.map((_, i) => i * 2),
})

describe('paging', () => {
  it('asks for the next page while the core says more assets match', () => {
    expect(nextOffset(paged([{ ids: ['a', 'b'], total: 5 }]).pages)).toBe(2)
    expect(nextOffset(paged([{ ids: ['a', 'b'], total: 5 }, { ids: ['c', 'd'], total: 5 }]).pages)).toBe(4)
    expect(nextOffset(paged([{ ids: ['a', 'b'], total: 2 }]).pages)).toBeUndefined()
  })
  it('stops when the core gives no total, or a page comes back empty', () => {
    expect(nextOffset(paged([{ ids: ['a', 'b'] }]).pages)).toBeUndefined()
    expect(nextOffset(paged([{ ids: ['a'], total: 9 }, { ids: [], total: 9 }]).pages)).toBeUndefined()
    expect(nextOffset([])).toBeUndefined()
  })
  it('lists an asset once when a later page repeats it', () => {
    // A new asset arrived between the two requests and pushed "b" onto the second page.
    const list = paged([{ ids: ['a', 'b'], total: 4 }, { ids: ['b', 'c'], total: 4 }])
    expect(flatten(list).map((a) => a.id)).toEqual(['a', 'b', 'c'])
    expect(flatten(undefined)).toEqual([])
  })
  it('knows how many match in all', () => {
    expect(totalOf(paged([{ ids: ['a', 'b'], total: 40 }]))).toBe(40)
    expect(totalOf(paged([{ ids: ['a', 'b'] }]))).toBe(2)
    expect(totalOf(undefined)).toBeUndefined()
  })
})

describe('isNameSortedList', () => {
  it('picks the lists a rename reorders, and no other query', () => {
    expect(isNameSortedList(assetListKey('', 'name'))).toBe(true)
    expect(isNameSortedList(assetListKey('knight', 'name'))).toBe(true)
    expect(isNameSortedList(assetListKey('', 'newest'))).toBe(false)
    expect(isNameSortedList(['assets', 'a1'])).toBe(false)
  })
})

describe('cached lists', () => {
  it('drops deleted assets from every page and from the total', () => {
    const list = paged([{ ids: ['a', 'b'], total: 4 }, { ids: ['c', 'd'], total: 4 }])
    const next = withoutAssets(list, new Set(['b', 'c']))
    expect(next.pages.map((p) => p.items.map((a) => a.id))).toEqual([['a'], ['d']])
    expect(next.pages.map((p) => p.total)).toEqual([2, 2])
    expect(next.pageParams).toEqual(list.pageParams)
  })
  it('leaves a list that does not hold the asset untouched, so nothing re-renders', () => {
    const list = paged([{ ids: ['a'], total: 1 }])
    expect(withoutAssets(list, new Set(['zzz']))).toBe(list)
    expect(withAsset(list, asset('zzz'))).toBe(list)
  })
  it('replaces an asset in place when it changes', () => {
    const list = paged([{ ids: ['a', 'b'], total: 2 }])
    const next = withAsset(list, asset('b', { name: 'Red barrel', thumbnail: 'blake3:t' }))
    expect(next.pages[0]!.items.map((a) => a.name)).toEqual(['a', 'Red barrel'])
    expect(next.pages[0]!.total).toBe(2)
  })
  it('works on a plain array, and passes anything else through', () => {
    const plain = [asset('a'), asset('b')]
    expect(withoutAssets(plain, new Set(['a'])).map((a) => a.id)).toEqual(['b'])
    expect(withAsset(plain, asset('a', { name: 'New' }))[0]!.name).toBe('New')
    expect(withoutAssets(undefined, new Set(['a']))).toBeUndefined()
    const single = asset('a')
    expect(withoutAssets(single, new Set(['a']))).toBe(single)
  })
})

describe('names and files', () => {
  it('says why a name cannot be used', () => {
    expect(nameProblem('  ')).toBe('Type a name.')
    expect(nameProblem('x'.repeat(121))).toMatch(/120/)
    expect(nameProblem('two\nlines')).toMatch(/line break/)
    expect(nameProblem(' 宝箱 ')).toBeUndefined()
  })
  it('adds the extension a Save dialog left out, once', () => {
    expect(withExtension('/home/me/sword', '.glb')).toBe('/home/me/sword.glb')
    expect(withExtension('C:\\out\\Sword.GLB', '.glb')).toBe('C:\\out\\Sword.GLB')
    expect(withExtension('C:\\out\\sword.fbx', '.glb')).toBe('C:\\out\\sword.fbx.glb')
  })
})

describe('labels', () => {
  it('counts assets in plain words', () => {
    expect(countLabel(1)).toBe('1 asset')
    expect(countLabel(14)).toBe('14 assets')
    expect(countLabel(0)).toBe('0 assets')
    expect(countLabel(8, 14)).toBe('8 of 14 assets')
    expect(countLabel(1, 1)).toBe('1 asset')
    expect(countLabel(0, 1)).toBe('0 of 1 asset')
  })
  it('counts the full level among the levels of detail', () => {
    expect(lodCount({ stats: { lods: 2 } })).toBe(3)
    expect(lodCount({ stats: { lods: 0 } })).toBe(1)
    expect(lodCount({ stats: {} })).toBeUndefined()
    expect(lodCount({})).toBeUndefined()
  })
})
