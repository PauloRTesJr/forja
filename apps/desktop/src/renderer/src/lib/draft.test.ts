import { describe, expect, it } from 'vitest'
import {
  addImages,
  clearStaged,
  draftStore,
  emptyDraft,
  generateLabel,
  planSubmit,
  removeImage,
  replaceImage,
  setSide,
  sideViews,
  startingPreset,
  type Draft,
  type Staged,
} from './draft'

const file = (name: string, dir = 'C:\\art\\'): Staged => ({ id: name, name, path: dir + name, ref: `blake3:${name}` })
const pasted = (name: string, ref = `blake3:${name}`): Staged => ({ id: name, name, blob: new Blob(['x']), ref })
const draft = (images: Staged[], sides: Draft['sides'] = {}): Draft => ({ ...emptyDraft, images, sides })

describe('addImages', () => {
  it('adds to what is staged and leaves out what is already there', () => {
    const start = draft([file('knight.png')])
    const { draft: next, duplicates } = addImages(start, [file('KNIGHT.png'), file('mage.png')])
    expect(next.images.map((s) => s.name)).toEqual(['knight.png', 'mage.png'])
    expect(duplicates).toBe(1)
  })

  it('tells the same pasted picture by its content, and never confuses it with a file', () => {
    const start = draft([pasted('pasted-1.png', 'blake3:aa')])
    expect(addImages(start, [pasted('pasted-2.png', 'blake3:aa')]).duplicates).toBe(1)
    expect(addImages(start, [{ ...file('a.png'), ref: 'blake3:aa' }]).duplicates).toBe(0)
  })

  it('keeps the draft itself when nothing was new', () => {
    const start = draft([file('knight.png')])
    expect(addImages(start, [file('knight.png')]).draft).toBe(start)
  })
})

describe('removing and replacing', () => {
  it('drops the side views with the last image, not before', () => {
    const start = draft([file('front.png'), file('other.png')], { back: file('back.png') })
    const one = removeImage(start, 'other.png')
    expect(one.sides.back?.name).toBe('back.png')
    expect(removeImage(one, 'front.png').sides).toEqual({})
  })

  it('replaces an image in place and keeps its side views', () => {
    const start = draft([file('a.png'), file('b.png')], { left: file('l.png') })
    const next = replaceImage(start, 'a.png', file('c.png'))
    expect(next.images.map((s) => s.name)).toEqual(['c.png', 'b.png'])
    expect(next.sides.left?.name).toBe('l.png')
  })

  it('sets, replaces and clears a side view', () => {
    let d = setSide(draft([file('front.png')]), 'back', file('back.png'))
    d = setSide(d, 'back', file('back2.png'))
    expect(d.sides.back?.name).toBe('back2.png')
    expect(setSide(d, 'back').sides).toEqual({})
  })

  it('empties the target but keeps following the jobs and the sheet labels', () => {
    const start: Draft = { images: [file('a.png')], sides: { left: file('l.png') }, tracked: ['j1'], labels: { j1: ['front'] } }
    expect(clearStaged(start)).toEqual({ images: [], sides: {}, tracked: ['j1'], labels: { j1: ['front'] } })
  })
})

describe('sideViews', () => {
  it('counts side views only for a single image, in view order', () => {
    const sides = { right: file('r.png'), left: file('l.png') }
    expect(sideViews(draft([file('front.png')], sides))).toEqual(['left', 'right'])
    expect(sideViews(draft([file('front.png'), file('other.png')], sides))).toEqual([])
  })
})

describe('planSubmit', () => {
  it('sends one file by its path, asking about a sheet', () => {
    expect(planSubmit(draft([file('knight.png')]))).toEqual([{ body: { path: 'C:\\art\\knight.png', priority: 'interactive', sheet: 'ask' } }])
  })

  it('sends a single image with its side views by path', () => {
    const d = draft([file('front.png')], { back: file('back.png'), left: file('left.png') })
    expect(planSubmit(d)).toEqual([
      { body: { path: 'C:\\art\\front.png', priority: 'interactive', sheet: 'ask', views: { left: 'C:\\art\\left.png', back: 'C:\\art\\back.png' } } },
    ])
  })

  it('sends several files as one request, so one bad file refuses them all', () => {
    const d = draft([file('a.png'), file('b.png'), file('c.png')], { back: file('back.png') })
    expect(planSubmit(d)).toEqual([{ body: { paths: ['C:\\art\\a.png', 'C:\\art\\b.png', 'C:\\art\\c.png'], priority: 'batch', sheet: 'ask' } }])
  })

  it('uploads a pasted picture again and sends it by ref, under its own name', () => {
    const p = pasted('pasted-101500.png')
    expect(planSubmit(draft([p]))).toEqual([{ body: { name: 'pasted-101500.png', priority: 'interactive', sheet: 'ask' }, upload: p }])
  })

  it('sends files together and each pasted picture after them', () => {
    const p = pasted('pasted-101500.png')
    const steps = planSubmit(draft([file('a.png'), p, file('b.png')]))
    expect(steps).toEqual([
      { body: { paths: ['C:\\art\\a.png', 'C:\\art\\b.png'], priority: 'batch', sheet: 'ask' } },
      { body: { name: 'pasted-101500.png', priority: 'batch', sheet: 'ask' }, upload: p },
    ])
  })

  it('has nothing to send for an empty target', () => {
    expect(planSubmit(draft([]))).toEqual([])
  })
})

describe('generateLabel', () => {
  it('counts a folder beside the images, truthfully', () => {
    const folder: Staged = { id: 'f', name: 'set', path: '/art/set', ref: '', folder: true }
    expect(generateLabel(draft([folder]))).toBe('Forge 1 folder')
    expect(generateLabel(draft([file('a.png'), file('b.png'), folder]))).toBe('Forge 2 images and 1 folder')
    expect(generateLabel(draft([file('a.png'), folder]))).toBe('Forge 1 image and 1 folder')
  })

  it('says what pressing it will start', () => {
    expect(generateLabel(draft([]))).toBe('Forge')
    expect(generateLabel(draft([file('a.png')]))).toBe('Forge')
    expect(generateLabel(draft([file('a.png')], { back: file('b.png'), left: file('l.png') }))).toBe('Forge from 3 views')
    expect(generateLabel(draft([file('a.png'), file('b.png')], { back: file('k.png') }))).toBe('Forge 2 images')
  })
})

describe('startingPreset', () => {
  const presets = [{ id: 'draft' }, { id: 'mobile-prop' }, { id: 'hero-character' }]

  it('prefers what was chosen here, then the Default preset setting, then Mobile prop', () => {
    expect(startingPreset(presets, 'draft', 'hero-character')).toBe('draft')
    expect(startingPreset(presets, null, 'hero-character')).toBe('hero-character')
    expect(startingPreset(presets, null, undefined)).toBe('mobile-prop')
  })

  it('passes over a preset that no longer exists', () => {
    expect(startingPreset(presets, 'my-deleted-preset', 'hero-character')).toBe('hero-character')
    expect(startingPreset([{ id: 'only' }], 'gone', 'also-gone')).toBe('only')
  })

  it('stands by the first candidate until the presets arrive', () => {
    expect(startingPreset(undefined, 'draft', 'hero-character')).toBe('draft')
    expect(startingPreset([], null, 'hero-character')).toBe('hero-character')
  })
})

describe('draftStore', () => {
  it('keeps the draft between readers and tells them of changes', () => {
    let calls = 0
    const off = draftStore.subscribe(() => calls++)
    draftStore.set((d) => ({ ...d, tracked: ['j1'] }))
    draftStore.set((d) => d) // nothing changed: nobody is told
    expect(draftStore.get().tracked).toEqual(['j1'])
    expect(calls).toBe(1)
    off()
    draftStore.set(emptyDraft)
    expect(calls).toBe(1)
  })
})
