import { describe, expect, it } from 'vitest'
import type { Estimate, Fallback, Stage, StageEstimate } from './api'
import { describeEstimate, describeMoves, priceText, usd, whereItRan } from './estimate'

const stage = (s: Partial<StageEstimate>): StageEstimate => ({ stage: 's', use: 'shape', priced: true, ...s })

const base: Estimate = {
  preset: 'mobile-prop',
  images: 1,
  stages: [],
  seconds: 0,
  cost_usd: 0,
  unmeasured: [],
  unpriced: [],
  paid: false,
  spend: { limits: { per_job: 0, per_day: 0, per_month: 0 }, today: 0, month: 0, queued: 0 },
}

describe('describeEstimate', () => {
  it('words measured time and leaves out the stages that never ran', () => {
    const e = {
      ...base,
      seconds: 72,
      unmeasured: ['paint'],
      stages: [stage({ stage: 'shape', seconds: 72 }), stage({ stage: 'paint', use: 'texture' })],
    }
    expect(describeEstimate(e)).toEqual({ time: 'About 1 min 12 s per image', cost: undefined, notes: ['Not counting Texture, which has not run yet.'], problem: undefined })
  })

  it('says when nothing ran yet', () => {
    expect(describeEstimate({ ...base, unmeasured: ['shape'], stages: [stage({})] }).time).toMatch(/after the first run/)
  })

  it('prices paid stages on their hosts, with spending against the daily limit', () => {
    const e: Estimate = {
      ...base,
      seconds: 40,
      paid: true,
      cost_usd: 0.4,
      unpriced: ['Rodin'],
      stages: [
        stage({ stage: 'shape', paid: true, provider: 'fal', provider_label: 'fal.ai', cost_usd: 0.4 }),
        stage({ stage: 'paint', use: 'texture', skipped: true, by: 'shape', paid: true, provider: 'other' }),
      ],
      spend: { limits: { per_job: 0, per_day: 2, per_month: 10 }, today: 0.8, month: 3, queued: 0 },
      problem: 'this image would bring spending to $2.40, over the daily limit of $2.00',
    }
    const text = describeEstimate(e)
    expect(text.cost).toBe('$0.40 per image on fal.ai')
    expect(text.notes).toEqual(['Rodin has no known price: give it one in Settings → Prices.', 'Spent today $0.80 of $2.00.'])
    expect(text.problem).toMatch(/daily limit/)
  })
})

describe('usd', () => {
  it('rounds to cents and says when a cost is below one', () => {
    expect(usd(0.4)).toBe('$0.40')
    expect(usd(0.000017)).toBe('under $0.01')
    expect(usd(0)).toBe('$0.00')
  })
})

describe('whereItRan', () => {
  it('names a provider other than this computer, and why a stage moved', () => {
    expect(whereItRan({ provider: 'local', provider_label: 'This computer' })).toBeUndefined()
    expect(whereItRan({ provider: 'lan', provider_label: 'Studio PC' })).toBe('Studio PC')
    expect(
      whereItRan({
        provider: 'lan',
        provider_label: 'Studio PC',
        fallbacks: [{ from: 'local', to: 'lan', reason: 'ran out of memory', from_label: 'This computer' }],
      }),
    ).toBe('Studio PC, after This computer ran out of memory')
  })
})

describe('describeMoves', () => {
  it('words the stages that moved together in one sentence', () => {
    const down = { from: 'lan', to: 'local', reason: 'could not be reached', from_label: 'Studio PC', to_label: 'This computer' }
    const s = (id: string, use: string, fallbacks?: Fallback[]): Stage => ({
      id, use, status: 'succeeded', key: '', cached: false, outputs: [], seconds: 0, step: 0, total: 0, fallbacks,
    })
    const stages = [
      s('bg', 'background-removal', [down]),
      s('shape', 'shape', [down]),
      s('paint', 'texture', [{ from: 'local', to: 'lan', reason: 'ran out of memory' }]),
      s('export', 'export'),
    ]
    expect(describeMoves(stages)).toEqual([
      'Background and Shape moved to This computer: Studio PC could not be reached.',
      'Texture moved to lan: local ran out of memory.',
    ])
  })
})

describe('priceText', () => {
  it('reads tiny rates whole and a generation as an image', () => {
    expect(priceText({ usd: 0.00031, per: 'second' })).toBe('$0.00031 per second')
    expect(priceText({ usd: 0.34, per: 'hour' })).toBe('$0.34 per hour')
    expect(priceText({ usd: 0.4, per: 'generation' })).toBe('$0.40 per image')
    expect(priceText({ usd: 0.015, per: 'units' })).toBe('$0.015 per unit')
  })
})
