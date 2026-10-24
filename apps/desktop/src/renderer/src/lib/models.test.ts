import { describe, expect, it } from 'vitest'
import type { Model } from './api'
import { cost, licenseFact, licenseName, limits, runsOn } from './models'

const model = (over: Partial<Model> = {}): Model => ({
  id: 'triposg',
  name: 'TripoSG',
  family: 'triposg',
  tasks: ['shape'],
  license: { spdx: 'MIT', commercial: true, restrictions: [] },
  status: 'not-installed',
  device: null,
  provider: 'local',
  needs_acceptance: false,
  uses: [],
  provider_label: 'This computer',
  paid: false,
  ...over,
})

const hosted = (over: Partial<Model> = {}): Model =>
  model({ id: 'fal-ai/trellis-2', provider: 'fal', provider_label: 'fal.ai', fit: 'hosted', status: 'ready', paid: true, ...over })

describe('runsOn', () => {
  it('names the device of an installed model, and says when it sits in VRAM', () => {
    expect(runsOn(model({ status: 'ready', device: 'cuda:0', fit: 'fits' }))).toEqual({ value: 'This computer', detail: 'cuda:0', tone: undefined })
    expect(runsOn(model({ status: 'loaded', device: 'cuda:0', fit: 'fits' })).detail).toBe('cuda:0 · in VRAM')
  })

  it('warns when an installed model only runs with offload', () => {
    expect(runsOn(model({ status: 'ready', device: 'cuda:0', fit: 'offload' }))).toMatchObject({ detail: 'cuda:0 · with offload, slower', tone: 'warn' })
  })

  it('says how a model not installed yet would fit', () => {
    expect(runsOn(model({ fit: 'fits' }))).toEqual({ value: 'This computer', detail: 'Fits your GPU', tone: 'ok' })
    expect(runsOn(model({ fit: 'offload' }))).toMatchObject({ tone: 'warn' })
    expect(runsOn(model({ fit: 'too-big' }))).toEqual({ value: 'Another computer', detail: 'Needs more VRAM', tone: 'dim' })
  })

  it('names a computer on the network by its label', () => {
    expect(runsOn(model({ provider: 'lan-1', provider_label: 'Desktop', status: 'ready', device: 'cuda:0' })).value).toBe('Desktop')
  })

  it('puts a hosted model in its provider’s cloud', () => {
    expect(runsOn(hosted())).toEqual({ value: 'fal.ai', detail: 'In the cloud' })
  })
})

describe('cost', () => {
  it('is free locally, with the disk it takes', () => {
    expect(cost(model({ disk_gb: 12.5 }))).toEqual({ value: 'Free', detail: '12.5 GB on disk' })
    expect(cost(model()).detail).toBeUndefined()
  })

  it('is the price of a hosted model and who bills it', () => {
    expect(cost(hosted({ price: { amount: 0.53, unit: 'generation', currency: 'USD' } }))).toEqual({
      value: '$0.53 per image',
      detail: 'Billed by fal.ai',
    })
  })
})

describe('licenses', () => {
  it('reads a custom license id as words and keeps SPDX ids', () => {
    expect(licenseName('LicenseRef-Tencent-Hunyuan-3D-2.0-Community')).toBe('Tencent Hunyuan 3D 2.0 Community')
    expect(licenseName('Apache-2.0')).toBe('Apache-2.0')
  })

  it('says whether commercial use is allowed, and when it has limits', () => {
    expect(licenseFact({ spdx: 'MIT', commercial: true, restrictions: [] })).toEqual({ value: 'MIT', detail: 'Commercial use', tone: 'ok' })
    expect(licenseFact({ spdx: 'CC-BY-NC-4.0', commercial: false, restrictions: [] })).toMatchObject({ detail: 'Non-commercial', tone: 'warn' })
    expect(licenseFact({ spdx: 'X', commercial: true, restrictions: ['not in the EU'] })).toMatchObject({ detail: 'Commercial, with limits', tone: 'warn' })
  })

  it('joins restrictions into one sentence', () => {
    expect(limits({ spdx: 'X', commercial: true, restrictions: ['not licensed in the EU', 'separate license above 1M users'] })).toBe(
      'Not licensed in the EU; separate license above 1M users.',
    )
    expect(limits({ spdx: 'MIT', commercial: true, restrictions: [] })).toBeUndefined()
  })
})
