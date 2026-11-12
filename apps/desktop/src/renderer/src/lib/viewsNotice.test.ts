import { describe, expect, it } from 'vitest'
import type { Pick, ReadBy } from './api'
import { theViews, viewsNote, viewsNotes } from './viewsNotice'

const shape = (extra: Partial<Pick> = {}): Pick => ({ stage: 'shape', use: 'shape', model: 'triposg', model_name: 'TripoSG', ...extra })
const mv: ReadBy = { model: 'hunyuan3d2-mv', model_name: 'Hunyuan3D-2mv', install_on: 'local', provider_label: 'This computer', reads: ['left', 'back', 'right'] }

describe('theViews', () => {
  it('lists one, two and three views the way people say them', () => {
    expect(theViews(['back'])).toBe('the back view')
    expect(theViews(['left', 'right'])).toBe('the left and right views')
    expect(theViews(['left', 'back', 'right'])).toBe('the left, back and right views')
  })
})

describe('viewsNote', () => {
  it('says a front-only model ignores the views, and which model reads them', () => {
    const note = viewsNote(shape({ unread_views: ['left', 'back', 'right'], read_by: mv }))
    expect(note?.lead).toBe('TripoSG shapes from the front alone: the left, back and right views will not change the shape.')
    expect(note?.reader).toBe('Hunyuan3D-2mv reads them.')
    expect(note?.installable).toBe(true)
  })

  it('has only the first half when no model reads more', () => {
    expect(viewsNote(shape({ unread_views: ['back'] }))).toEqual({
      stage: 'shape',
      lead: 'TripoSG shapes from the front alone: the back view will not change the shape.',
    })
  })

  it('reads naturally for a single view and for two', () => {
    expect(viewsNote(shape({ unread_views: ['left'], read_by: { ...mv, reads: ['left'] } }))?.reader).toBe('Hunyuan3D-2mv reads it.')
    expect(viewsNote(shape({ unread_views: ['left', 'back'], read_by: { ...mv, reads: ['left', 'back'] } }))?.reader).toBe('Hunyuan3D-2mv reads them.')
  })

  it('names the views a reader covers when it does not cover them all', () => {
    const note = viewsNote(shape({ unread_views: ['left', 'back'], read_by: { ...mv, reads: ['back'] } }))
    expect(note?.reader).toBe('Hunyuan3D-2mv reads the back view.')
  })

  it('says where an uninstalled model installs when that is not this computer', () => {
    const note = viewsNote(shape({ unread_views: ['back'], read_by: { ...mv, reads: ['back'], install_on: 'studio', provider_label: 'Studio PC' } }))
    expect(note?.reader).toBe('Hunyuan3D-2mv reads it. It installs on Studio PC.')
  })

  it('says a model that is on the provider but cannot run there yet is not an install', () => {
    const note = viewsNote(shape({ unread_views: ['back'], read_by: { ...mv, reads: ['back'], installed: true } }))
    expect(note?.reader).toBe('Hunyuan3D-2mv reads it, but cannot run on This computer yet.')
    expect(note?.installable).toBeUndefined()
  })

  it('does not call a model that reads some views front-only', () => {
    const note = viewsNote(shape({ model_name: 'Duo', views: ['front', 'back'], unread_views: ['left', 'right'] }))
    expect(note?.lead).toBe('Duo does not read the left and right views: they will not change the shape.')
  })

  it('speaks of the texture for a painter', () => {
    const note = viewsNote({ stage: 'paint', use: 'texture', model: 'paint', unread_views: ['back'] })
    expect(note?.lead).toBe('paint paints from the front alone: the back view will not change the texture.')
  })

  it('has nothing to say when every view is read or the step is skipped', () => {
    expect(viewsNote(shape())).toBeUndefined()
    expect(viewsNote(shape({ unread_views: [] }))).toBeUndefined()
    expect(viewsNote(shape({ unread_views: ['back'], skipped: true }))).toBeUndefined()
  })
})

describe('viewsNotes', () => {
  it('collects the steps that ignore views, in order', () => {
    const plan: Pick[] = [
      { stage: 'bg', use: 'background-removal', model: 'rmbg' },
      shape({ unread_views: ['back'] }),
      { stage: 'paint', use: 'texture', model: 'paint', unread_views: ['back'] },
    ]
    expect(viewsNotes(plan).map((n) => n.stage)).toEqual(['shape', 'paint'])
  })
})
