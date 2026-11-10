import { beforeEach, describe, expect, it, vi } from 'vitest'
import { libraryView, pruned } from './libraryView'

describe('libraryView', () => {
  beforeEach(() => libraryView.reset())

  it('keeps the search, order and selection after the screen that set them is gone', () => {
    libraryView.set({ q: 'mobile', sort: 'name', selected: new Set(['a', 'b']), anchor: 'a' })
    // Nothing subscribes while another screen shows; the next mount reads the same view.
    expect(libraryView.get()).toMatchObject({ q: 'mobile', sort: 'name', anchor: 'a' })
    expect([...libraryView.get().selected]).toEqual(['a', 'b'])
  })

  it('tells its subscribers of a change, with a new snapshot each time', () => {
    const changed = vi.fn()
    const before = libraryView.get()
    const off = libraryView.subscribe(changed)
    libraryView.set({ q: 'gem' })
    expect(changed).toHaveBeenCalledOnce()
    expect(libraryView.get()).not.toBe(before)
    off()
    libraryView.set({ q: '' })
    expect(changed).toHaveBeenCalledOnce()
  })

  it('remembers the scroll position without re-rendering anything', () => {
    const changed = vi.fn()
    libraryView.subscribe(changed)
    libraryView.rememberScroll(480)
    expect(libraryView.scrollTop()).toBe(480)
    expect(changed).not.toHaveBeenCalled()
  })

  it('hands back the opened asset once, so only the return from its page focuses its card', () => {
    libraryView.open('asset-7')
    expect(libraryView.get().active).toBe('asset-7')
    expect(libraryView.returnedFrom()).toBe('asset-7')
    expect(libraryView.returnedFrom()).toBeNull()
  })
})

describe('pruned', () => {
  it('drops what left the library and keeps the same set when nothing did', () => {
    const selected = new Set(['a', 'b', 'c'])
    expect([...pruned(selected, ['a', 'c', 'd'])]).toEqual(['a', 'c'])
    expect(pruned(selected, ['c', 'b', 'a'])).toBe(selected)
  })
})
