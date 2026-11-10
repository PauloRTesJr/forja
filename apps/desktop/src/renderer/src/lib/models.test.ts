import { describe, expect, it } from 'vitest'
import type { InstallTask, Model, Setup } from './api'
import {
  SKIP,
  canManage,
  cost,
  deviceName,
  downloadMinutes,
  licenseFact,
  licenseName,
  limits,
  modelState,
  orderModels,
  planInstalls,
  removalEffect,
  removalPlan,
  runsOn,
  setupPlan,
  stageRule,
} from './models'

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
  fit: 'fits',
  ...over,
})

const hosted = (over: Partial<Model> = {}): Model =>
  model({ id: 'fal-ai/trellis-2', name: 'TRELLIS 2', provider: 'fal', provider_label: 'fal.ai', fit: 'hosted', status: 'ready', paid: true, ...over })

const ready = (over: Partial<Model> = {}): Model => model({ status: 'ready', device: 'cuda:0', ...over })

const task = (over: Partial<InstallTask> = {}): InstallTask => ({
  provider: 'local',
  id: 't',
  kind: 'install',
  target: 'triposg',
  status: 'running',
  steps: [],
  progress: null,
  log: [],
  error: null,
  created_at: 1,
  version: 1,
  ...over,
})

describe('runsOn', () => {
  it('names the device of an installed model as people say it', () => {
    expect(runsOn(ready())).toEqual({ value: 'This computer', detail: 'NVIDIA GPU', tone: undefined })
    // Whether it sits in VRAM right now is not said: nothing pushes that, so it would go stale.
    expect(runsOn(ready({ status: 'loaded' })).detail).toBe('NVIDIA GPU')
    expect(deviceName('cuda:1')).toBe('NVIDIA GPU 2')
    expect(deviceName('xpu:0')).toBe('xpu:0')
  })

  it('warns when an installed model only runs slower, without the word offload', () => {
    const fact = runsOn(ready({ fit: 'offload' }))
    expect(fact).toMatchObject({ detail: 'NVIDIA GPU · slower: part of it runs in RAM', tone: 'warn' })
  })

  it('says how a model not installed yet would fit, with the VRAM it needs', () => {
    expect(runsOn(model({ vram_gb: 8 }))).toEqual({ value: 'This computer', detail: 'Fits your GPU · needs 8 GB', tone: 'ok' })
    expect(runsOn(model())).toEqual({ value: 'This computer', detail: 'Fits your GPU', tone: 'ok' })
    expect(runsOn(model({ fit: 'offload', vram_gb: 12.5 }))).toEqual({ value: 'This computer', detail: 'Slower here · needs 12.5 GB of VRAM', tone: 'warn' })
    expect(runsOn(model({ fit: 'too-big', vram_gb: 24 }))).toEqual({ value: 'Another computer', detail: 'Needs 24 GB of VRAM', tone: 'dim' })
    expect(runsOn(model({ fit: 'too-big' })).detail).toBe('Needs more VRAM')
  })

  it('says Apple GPU and CPU for a Mac’s devices', () => {
    expect(runsOn(ready({ device: 'mps', runs_on: 'mps' })).detail).toBe('Apple GPU')
    expect(runsOn(ready({ device: 'cpu', runs_on: 'cpu' })).detail).toBe('CPU')
    // BiRefNet on a Mac lands on the CPU, which is no GPU to fit.
    expect(runsOn(model({ runs_on: 'cpu', vram_gb: 8 }))).toEqual({ value: 'This computer', detail: 'Runs on the CPU', tone: 'ok' })
  })

  it('sends a model no device here can run to another computer', () => {
    expect(runsOn(model({ fit: 'unsupported', runs_on: null }))).toEqual({ value: 'Another computer', detail: 'Cannot run on this one', tone: 'dim' })
  })

  it('names a computer on the network by its label', () => {
    expect(runsOn(ready({ provider: 'lan-1', provider_label: 'Desktop' })).value).toBe('Desktop')
  })

  it('puts a hosted model in its provider’s cloud', () => {
    expect(runsOn(hosted())).toEqual({ value: 'fal.ai', detail: 'In the cloud' })
  })
})

describe('cost', () => {
  it('is the disk a local model takes, and free to run', () => {
    expect(cost(model({ disk_gb: 12.5 }))).toEqual({ value: '12.5 GB', detail: 'Free to run' })
    expect(cost(model({ disk_gb: 5 })).value).toBe('5 GB')
    expect(cost(model()).value).toBe('Size unknown')
    expect(cost(model({ stub: true }))).toEqual({ value: 'Built in' })
  })

  it('is the price of a hosted model and who bills it', () => {
    expect(cost(hosted({ price: { amount: 0.53, unit: 'generation', currency: 'USD' } }))).toEqual({
      value: '$0.53 per image',
      detail: 'Billed by fal.ai',
      tone: undefined,
    })
    expect(cost(hosted())).toMatchObject({ value: 'No price set', tone: 'warn' })
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

describe('modelState', () => {
  const used = [{ use: 'shape', pinned: false }]
  const chosen = [{ use: 'shape', pinned: true }]

  it('is in use when the stage runs it, chosen or picked', () => {
    expect(modelState(ready({ uses: used }))).toBe('in-use')
    expect(modelState(ready({ uses: chosen }))).toBe('in-use')
    expect(modelState(hosted({ uses: chosen }))).toBe('in-use')
  })

  it('never calls a chosen model that is not installed "in use"', () => {
    expect(modelState(model({ uses: chosen }))).toBe('chosen-missing')
    expect(modelState(model({ uses: chosen, fit: 'too-big' }))).toBe('chosen-missing')
  })

  it('tells ready, installable and out-of-reach models apart', () => {
    expect(modelState(ready())).toBe('ready')
    expect(modelState(model())).toBe('not-installed')
    expect(modelState(model({ fit: 'offload' }))).toBe('not-installed')
    expect(modelState(model({ fit: 'too-big' }))).toBe('unavailable')
    expect(modelState(model({ fit: 'unsupported' }))).toBe('unavailable')
  })

  it('offers no install for a model on a computer this app does not manage', () => {
    const remote = model({ provider: 'lan-1', provider_label: 'Studio PC' })
    expect(canManage(remote)).toBe(false)
    expect(modelState(remote)).toBe('elsewhere')
    expect(canManage(hosted())).toBe(false)
    expect(canManage(ready({ stub: true }))).toBe(false)
    expect(canManage(model())).toBe(true)
  })

  it('follows a running task first, and remembers a failed install', () => {
    expect(modelState(model(), task())).toBe('installing')
    expect(modelState(model(), task({ status: 'queued' }))).toBe('installing')
    expect(modelState(ready({ uses: used }), task({ kind: 'remove' }))).toBe('removing')
    expect(modelState(model(), task({ status: 'failed' }))).toBe('failed')
    expect(modelState(model(), task({ status: 'cancelled' }))).toBe('not-installed')
    // An old failure says nothing about a model that is installed now.
    expect(modelState(ready(), task({ status: 'failed' }))).toBe('ready')
  })
})

describe('orderModels', () => {
  it('lists the model in use, then ready ones, then installable ones, then the rest, best first', () => {
    const list = [
      model({ id: 'hunyuan', name: 'Hunyuan3D-2mv', rank: 5 }),
      model({ id: 'pixal', name: 'Pixal3D', rank: 20, fit: 'offload' }),
      ready({ id: 'stub', name: 'Stub shape', rank: 0, stub: true }),
      model({ id: 'trellis', name: 'TRELLIS.2', rank: 15, fit: 'too-big' }),
      ready({ id: 'triposg', name: 'TripoSG', rank: 10, uses: [{ use: 'shape', pinned: false }] }),
      ready({ id: 'mv', name: 'Other ready', rank: 12 }),
    ]
    expect(orderModels(list).map((m) => m.id)).toEqual(['triposg', 'mv', 'stub', 'pixal', 'hunyuan', 'trellis'])
  })

  it('keeps a stand-in first while it is the one in use', () => {
    const list = [model({ id: 'a', rank: 10 }), ready({ id: 'stub', stub: true, rank: 0, uses: [{ use: 'shape', pinned: false }] })]
    expect(orderModels(list)[0]!.id).toBe('stub')
  })

  it('puts a chosen model that is not installed on top, where its problem is seen', () => {
    const list = [ready({ id: 'a', rank: 10 }), model({ id: 'b', rank: 1, uses: [{ use: 'shape', pinned: true }] })]
    expect(orderModels(list)[0]!.id).toBe('b')
  })
})

describe('stageRule', () => {
  it('says which model a stage uses and whether the user chose it', () => {
    const list = [ready({ id: 'a' }), ready({ id: 'b', uses: [{ use: 'shape', pinned: false }] })]
    expect(stageRule(list, 'shape')).toMatchObject({ model: { id: 'b' }, pinned: false, missing: false })
    expect(stageRule(list, 'texture')).toBeUndefined()
  })

  it('flags a stage set to a model that is not installed', () => {
    const list = [ready({ id: 'a' }), model({ id: 'b', uses: [{ use: 'shape', pinned: true }] })]
    expect(stageRule(list, 'shape')).toMatchObject({ pinned: true, missing: true })
    expect(stageRule([hosted({ uses: [{ use: 'shape', pinned: true }] })], 'shape')).toMatchObject({ pinned: true, missing: false })
  })
})

describe('removalEffect', () => {
  const inUse = ready({ id: 'triposg', name: 'TripoSG', rank: 10, uses: [{ use: 'shape', pinned: false }] })

  it('has nothing to add for a model no stage uses', () => {
    expect(removalEffect(ready(), [ready(), inUse])).toBeUndefined()
  })

  it('names the model that takes over', () => {
    const others = [ready({ id: 'low', name: 'Low', rank: 1 }), ready({ id: 'mv', name: 'Hunyuan3D-2mv', rank: 5 }), model({ id: 'pixal', name: 'Pixal3D', rank: 20 })]
    expect(removalEffect(inUse, [inUse, ...others])).toBe('Shape will use Hunyuan3D-2mv instead.')
  })

  it('warns when nothing is left, since paid models and stand-ins do not step in by themselves', () => {
    const rest = [hosted(), ready({ id: 'stub', name: 'Stub shape', stub: true }), ready({ id: 'bg', tasks: ['background-removal'] })]
    expect(removalEffect(inUse, [inUse, ...rest])).toMatch(/only shape model that is ready/)
    expect(removalEffect(inUse, [inUse, ...rest], true)).toBe('Shape will use Stub shape instead.')
  })
})

describe('removalPlan', () => {
  it('hands a step set to the model back to automatic', () => {
    expect(removalPlan(ready({ uses: [{ use: 'shape', pinned: true }] }))).toEqual({ hosted: false, unpin: 'shape' })
  })

  it('leaves automatic steps and unused models alone', () => {
    expect(removalPlan(ready({ uses: [{ use: 'shape', pinned: false }] }))).toEqual({ hosted: false, unpin: undefined })
    expect(removalPlan(ready())).toEqual({ hosted: false, unpin: undefined })
  })

  it('takes a hosted model off its list without touching the stage pins', () => {
    expect(removalPlan(hosted({ uses: [{ use: 'shape', pinned: true }] }))).toEqual({ hosted: true })
  })
})

describe('setupPlan', () => {
  const setup = (stages: Setup['stages']): Setup => ({ system: null, stages, install: [], disk_gb: 0, ready: false })
  const triposg = model({ id: 'triposg', name: 'TripoSG', rank: 10, disk_gb: 12.5 })
  const pixal = model({ id: 'pixal3d', name: 'Pixal3D', rank: 20, fit: 'offload', disk_gb: 25.8 })
  const trellis = model({ id: 'trellis2', name: 'TRELLIS.2', rank: 15, fit: 'too-big' })
  const birefnet = model({ id: 'birefnet', name: 'BiRefNet', tasks: ['background-removal'], disk_gb: 3.8 })
  const all = [pixal, trellis, triposg, birefnet, model({ id: 'stub-shape', stub: true, status: 'ready' })]
  const shape = { use: 'shape', status: 'install' as const, model: 'pixal3d', model_name: 'Pixal3D', disk_gb: 25.8, fit: 'offload' as const }

  it('takes the core’s recommendation and offers what else runs here, the ones that fit first', () => {
    const [row] = setupPlan(setup([shape]), all)
    expect(row).toMatchObject({ status: 'install', id: 'pixal3d', fit: 'offload', disk_gb: 25.8, chosen: false })
    // Too big for this GPU and the stand-in are no choices.
    expect(row!.options.map((m) => m.id)).toEqual(['triposg', 'pixal3d'])
  })

  it('lets the user take a smaller model or leave a stage for later', () => {
    expect(setupPlan(setup([shape]), all, { shape: 'triposg' })[0]).toMatchObject({ id: 'triposg', disk_gb: 12.5, fit: 'fits' })
    expect(setupPlan(setup([shape]), all, { shape: SKIP })[0]).toMatchObject({ skipped: true })
    expect(setupPlan(setup([shape]), all, { shape: SKIP })[0]!.id).toBeUndefined()
    // A choice that is not on the list falls back to the recommendation.
    expect(setupPlan(setup([shape]), all, { shape: 'trellis2' })[0]!.id).toBe('pixal3d')
  })

  it('recommends the model a stage is set to, because installing another would not unblock it', () => {
    const chosen = { ...triposg, uses: [{ use: 'shape', pinned: true }] }
    const [row] = setupPlan(setup([shape]), [pixal, chosen])
    expect(row).toMatchObject({ id: 'triposg', chosen: true })
  })

  it('keeps the core’s words while the models list is not read yet', () => {
    expect(setupPlan(setup([shape]), [])[0]).toMatchObject({ id: 'pixal3d', name: 'Pixal3D', disk_gb: 25.8, options: [] })
  })

  it('passes covered and out-of-reach stages through', () => {
    const plan = setupPlan(
      setup([
        { use: 'background-removal', status: 'ready', model: 'birefnet', model_name: 'BiRefNet', provider: 'local' },
        { use: 'texture', status: 'remote' },
      ]),
      all,
    )
    expect(plan[0]).toMatchObject({ status: 'ready', name: 'BiRefNet' })
    expect(plan[1]).toMatchObject({ status: 'remote', options: [] })
  })

  it('installs each model once and nothing for skipped or covered stages', () => {
    const both = model({ id: 'both', name: 'Both', tasks: ['shape', 'texture'], disk_gb: 4 })
    const plan = setupPlan(
      setup([
        { use: 'background-removal', status: 'install', model: 'birefnet' },
        { use: 'shape', status: 'install', model: 'both' },
        { use: 'texture', status: 'install', model: 'both' },
        { use: 'rig', status: 'ready', model: 'unirig' },
      ]),
      [birefnet, both],
      { 'background-removal': SKIP },
    )
    expect(planInstalls(plan).map((r) => r.id)).toEqual(['both'])
  })
})

describe('downloadMinutes', () => {
  it('is the arithmetic of size over speed, never under a minute', () => {
    expect(downloadMinutes(12.5)).toBe(17)
    expect(downloadMinutes(12.5, 1000)).toBe(2)
    expect(downloadMinutes(0.1)).toBe(1)
  })
})
