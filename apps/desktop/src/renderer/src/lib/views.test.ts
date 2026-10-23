import { describe, expect, it } from 'vitest'
import type { Job, Pick, SheetPart } from './api'
import { needsAnswer, summarize } from './jobs'
import { assignView, boxStyle, chosenParts, viewReader } from './views'

const pick = (stage: string, use: string, extra: Partial<Pick> = {}): Pick => ({ stage, use, ...extra })

describe('viewReader', () => {
  it('finds the shape step whose model reads side views', () => {
    const plan = [
      pick('bg', 'background-removal'),
      pick('paint', 'texture', { model: 'paint', views: ['front', 'left', 'back', 'right'] }),
      pick('shape', 'shape', { model: 'mv', views: ['front', 'left', 'back', 'right'] }),
    ]
    expect(viewReader(plan)?.model).toBe('mv')
  })

  it('ignores models that read the front alone, and skipped steps', () => {
    expect(viewReader([pick('shape', 'shape', { model: 'triposg' })])).toBeUndefined()
    expect(viewReader([pick('shape', 'shape', { views: ['front'] })])).toBeUndefined()
    expect(viewReader([pick('paint', 'texture', { skipped: true, views: ['front', 'back'] })])).toBeUndefined()
  })
})

describe('assignView', () => {
  it('swaps a view with the part that had it', () => {
    expect(assignView(['front', 'left', 'back'], 2, 'front')).toEqual(['back', 'left', 'front'])
  })

  it('leaves a part out, or gives it a view nobody has', () => {
    expect(assignView(['front', 'left', 'back'], 1, '')).toEqual(['front', '', 'back'])
    expect(assignView(['front', '', 'back'], 1, 'right')).toEqual(['front', 'right', 'back'])
  })

  it('moves a view from another part to one that was left out', () => {
    expect(assignView(['front', '', 'back'], 1, 'front')).toEqual(['', 'front', 'back'])
  })
})

describe('chosenParts', () => {
  const parts: SheetPart[] = [
    { box: [0, 0, 10, 10], kind: 'figure', view: '' },
    { box: [20, 0, 30, 10], kind: 'detail', view: '' },
    { box: [40, 0, 50, 10], kind: 'figure', view: '' },
  ]

  it('sends the labelled parts in view order', () => {
    expect(chosenParts(parts, ['back', '', 'front'])).toEqual([
      { box: [40, 0, 50, 10], kind: 'figure', view: 'front' },
      { box: [0, 0, 10, 10], kind: 'figure', view: 'back' },
    ])
  })
})

describe('boxStyle', () => {
  it('places a box in percentages of the image', () => {
    expect(boxStyle([100, 50, 300, 250], [400, 500])).toEqual({ left: '25.000%', top: '10.000%', width: '50.000%', height: '40.000%' })
  })
})

describe('a job waiting on a sheet', () => {
  const waiting: Job = {
    id: 'j', name: 'creature', preset: 'p', input: 'i', priority: 'interactive', status: 'waiting', created_at: '',
    stages: [],
    sheet: {
      mode: 'ask', checked: true, found: true, size: [100, 100],
      parts: [{ box: [0, 0, 1, 1], view: 'front' }, { box: [0, 0, 1, 1], view: '' }, { box: [0, 0, 1, 1], view: 'back' }],
    },
  }

  it('needs an answer and says how many views were found', () => {
    expect(needsAnswer(waiting)).toBe(true)
    expect(summarize(waiting)).toEqual({ headline: 'Several views found', note: '2 views of one subject · check them to go on' })
    expect(needsAnswer({ ...waiting, status: 'running' })).toBe(false)
  })
})
