// What the Models screen and the guided setup say about models: the facts of a row, the state it
// is in, the order of a stage's list, and which models a new computer should install.
import { taskDone, type Fit, type InstallTask, type License, type Model, type Setup, type SetupStage } from './api'
import { money, taskNoun } from './format'

/** Good news, a caveat, or something that rules the model out on this machine. */
export type Tone = 'ok' | 'warn' | 'dim'

export interface Fact {
  value: string
  detail?: string
  tone?: Tone
}

export const fitText: Record<Fit, string> = {
  fits: 'Fits your GPU',
  offload: 'Slower here',
  'too-big': 'Needs more VRAM',
  unsupported: 'Cannot run here',
  hosted: 'Hosted',
}

/** How a model fits, said for the device it lands on: a model a Mac runs on its CPU fits no GPU. */
export const fitLabel = (fit: Fit, device?: string | null): string => (fit === 'fits' && device === 'cpu' ? 'Runs on the CPU' : fitText[fit])

const backends: Record<string, string> = { cuda: 'NVIDIA GPU', rocm: 'AMD GPU', mps: 'Apple GPU', cpu: 'CPU' }

/** A device id as people say it: "cuda:0" is the NVIDIA GPU, "cuda:1" the second one. */
export function deviceName(device: string): string {
  const [backend, index] = device.split(':')
  const name = backends[backend!]
  if (!name) return device
  return index && index !== '0' ? `${name} ${Number(index) + 1}` : name
}

/** Gigabytes without a pointless ".0". */
export const gb = (n: number): string => `${Number.isInteger(n) ? n : n.toFixed(1)} GB`

export const isInstalled = (m: Model): boolean => m.status === 'ready' || m.status === 'loaded'

export const isHosted = (m: Model): boolean => m.fit === 'hosted'

/** Whether the model can run on the computer that lists it, slower if need be. */
export const runsHere = (m: Model): boolean => m.fit === 'fits' || m.fit === 'offload'

/**
 * Whether this app installs, repairs and removes the model: only this computer's own real models.
 * Another computer manages its own, a hosted model has nothing to install, a stub is part of the engine.
 */
export const canManage = (m: Model): boolean => m.provider === 'local' && !isHosted(m) && !m.stub

/** Where a model runs, or would once installed, and how well it fits there. */
export function runsOn(m: Model): Fact {
  if (isHosted(m)) return { value: m.provider_label, detail: 'In the cloud' }
  const where = m.provider === 'local' ? 'This computer' : m.provider_label
  const needs = m.vram_gb ? `needs ${gb(m.vram_gb)}` : undefined
  if (isInstalled(m)) {
    const notes = [m.device && deviceName(m.device), m.fit === 'offload' && 'slower: part of it runs in RAM']
    return { value: where, detail: notes.filter(Boolean).join(' · ') || undefined, tone: m.fit === 'offload' ? 'warn' : undefined }
  }
  switch (m.fit) {
    case 'fits':
      return { value: where, detail: m.runs_on === 'cpu' ? 'Runs on the CPU' : ['Fits your GPU', needs].filter(Boolean).join(' · '), tone: 'ok' }
    case 'offload':
      return { value: where, detail: ['Slower here', needs && `${needs} of VRAM`].filter(Boolean).join(' · '), tone: 'warn' }
    case 'too-big':
      return { value: 'Another computer', detail: m.vram_gb ? `Needs ${gb(m.vram_gb)} of VRAM` : fitText['too-big'], tone: 'dim' }
    case 'unsupported':
      return { value: 'Another computer', detail: 'Cannot run on this one', tone: 'dim' }
    default:
      return { value: where }
  }
}

/** What a model takes: disk for a local one (running it is free), the provider's price when hosted. */
export function cost(m: Model): Fact {
  if (isHosted(m)) return { value: m.price ? money(m.price) : 'No price set', detail: `Billed by ${m.provider_label}`, tone: m.price ? undefined : 'warn' }
  if (m.stub) return { value: 'Built in' }
  return { value: m.disk_gb ? gb(m.disk_gb) : 'Size unknown', detail: 'Free to run' }
}

/** A license's name for people: SPDX ids stay, a custom "LicenseRef-" id reads as words. */
export function licenseName(spdx: string): string {
  const custom = /^LicenseRef-(.+)$/.exec(spdx)
  return custom ? custom[1]!.replaceAll('-', ' ') : spdx
}

/** The license and whether a game made with the model can be sold. */
export function licenseFact(l: License): Fact {
  const value = licenseName(l.spdx)
  if (l.restrictions.length > 0) return { value, detail: l.commercial ? 'Commercial, with limits' : 'Non-commercial, with limits', tone: 'warn' }
  return l.commercial ? { value, detail: 'Commercial use', tone: 'ok' } : { value, detail: 'Non-commercial', tone: 'warn' }
}

/** A license's restrictions as one sentence. */
export function limits(l: License): string | undefined {
  if (l.restrictions.length === 0) return undefined
  const text = l.restrictions.join('; ')
  return `${text[0]!.toUpperCase()}${text.slice(1)}.`
}

/** The stage a model is listed under: its first, which is also the one a pin sets. */
export const stageOf = (m: Model): string | undefined => m.tasks[0]

/**
 * What a row says and offers:
 * - `chosen-missing`: the stage is set to this model, which is not installed, so nothing runs it.
 * - `failed`: its last install stopped; installing again continues it.
 * - `elsewhere`: not installed on a computer this app does not manage.
 * - `unavailable`: no device here runs it.
 */
export type ModelState = 'installing' | 'removing' | 'in-use' | 'chosen-missing' | 'ready' | 'failed' | 'not-installed' | 'elsewhere' | 'unavailable'

export function modelState(m: Model, task?: InstallTask): ModelState {
  if (task && !taskDone(task)) return task.kind === 'remove' ? 'removing' : 'installing'
  const use = stageOf(m)
  const used = use ? m.uses.find((u) => u.use === use) : undefined
  const ready = isHosted(m) || isInstalled(m)
  if (used) return used.pinned && !ready ? 'chosen-missing' : 'in-use'
  if (ready) return 'ready'
  if (m.stub || !runsHere(m)) return 'unavailable'
  if (!canManage(m)) return 'elsewhere'
  return task?.kind === 'install' && task.status === 'failed' ? 'failed' : 'not-installed'
}

const listOrder: Record<ModelState, number> = {
  'in-use': 0,
  'chosen-missing': 0,
  installing: 1,
  removing: 1,
  ready: 1,
  failed: 2,
  'not-installed': 2,
  elsewhere: 2,
  unavailable: 3,
}

/** A stage's models as the list shows them: the one in use, the other ready ones, those that can be installed, those that cannot run here; best first within each, stand-ins last. */
export function orderModels(models: Model[]): Model[] {
  const group = (m: Model): number => listOrder[modelState(m)] + (m.stub && m.uses.length === 0 ? 0.5 : 0)
  return [...models].sort((a, b) => group(a) - group(b) || (b.rank ?? 0) - (a.rank ?? 0) || a.name.localeCompare(b.name))
}

/** Which model a stage uses now and why: chosen by the user or picked automatically; `missing` when the chosen one is not installed. */
export interface StageRule {
  model: Model
  pinned: boolean
  missing: boolean
}

export function stageRule(models: Model[], use: string): StageRule | undefined {
  for (const m of models) {
    const used = m.uses.find((u) => u.use === use)
    if (used) return { model: m, pinned: used.pinned, missing: used.pinned && !isHosted(m) && !isInstalled(m) }
  }
  return undefined
}

/**
 * What removing a model does to the stage that uses it, as a sentence for the confirmation;
 * nothing when no stage uses it. Paid models never step in by themselves, and stand-ins only
 * when the settings allow them.
 */
export function removalEffect(m: Model, models: Model[], stubs = false): string | undefined {
  const use = stageOf(m)
  if (!use || !m.uses.some((u) => u.use === use)) return undefined
  const next = models
    .filter((x) => x !== m && x.tasks.includes(use) && isInstalled(x) && !isHosted(x) && !x.paid && (stubs || !x.stub))
    .sort((a, b) => (b.rank ?? 0) - (a.rank ?? 0))[0]
  const stage = taskNoun(use)
  if (!next) return `It is the only ${stage} model that is ready: presets that need one stop working until you install another.`
  return `${capital(stage)} will use ${next.name} instead.`
}

/**
 * The calls a confirmed removal makes: a hosted model is taken off its provider's list; a local
 * one is deleted, and a step set to it is handed back to automatic, because a pin to a model
 * that is gone would block every preset (a core that does this itself makes the second call a no-op).
 */
export function removalPlan(m: Model): { hosted: boolean; unpin?: string } {
  if (isHosted(m)) return { hosted: true }
  const use = stageOf(m)
  return { hosted: false, unpin: use && m.uses.some((u) => u.use === use && u.pinned) ? use : undefined }
}

const capital = (s: string): string => `${s[0]!.toUpperCase()}${s.slice(1)}`

/** One stage of the guided setup: covered already, to install (with the models to choose from), or out of this computer's reach. */
export interface PlanRow {
  use: string
  status: SetupStage['status']
  /** The model that covers it, or the one to install: the user's choice, else the recommendation. */
  id?: string
  name?: string
  /** The same model as the models list has it; missing while that list is not read yet. */
  model?: Model
  disk_gb?: number
  fit?: Fit
  /** What this computer could install for the stage, the ones that fit first, best first. */
  options: Model[]
  /** The stage is set to this model, which is not installed: installing anything else would not help. */
  chosen?: boolean
  /** The user left this stage for later. */
  skipped?: boolean
}

/** The choice that leaves a stage for later. */
export const SKIP = ''

/**
 * The setup as a plan per stage. The core recommends the best-ranked model that runs here; a
 * stage the user set to a model that is not installed gets that model instead, because nothing
 * else would unblock it. `picks` are the user's changes: a model id, or SKIP.
 */
export function setupPlan(setup: Setup, models: Model[], picks: Record<string, string> = {}): PlanRow[] {
  const local = models.filter((m) => m.provider === 'local')
  return setup.stages.map((st): PlanRow => {
    if (st.status === 'ready') {
      const model = models.find((m) => m.id === st.model && m.provider === (st.provider ?? 'local'))
      return { use: st.use, status: 'ready', id: st.model, name: model?.name ?? st.model_name, model, options: [] }
    }
    const options = local
      .filter((m) => canManage(m) && m.tasks.includes(st.use) && runsHere(m) && !isInstalled(m))
      .sort((a, b) => Number(b.fit === 'fits') - Number(a.fit === 'fits') || (b.rank ?? 0) - (a.rank ?? 0))
    if (st.status === 'remote' && options.length === 0) return { use: st.use, status: 'remote', options }
    if (picks[st.use] === SKIP) return { use: st.use, status: 'install', options, skipped: true }
    const pinned = options.find((m) => m.uses.some((u) => u.use === st.use && u.pinned))
    const model = options.find((m) => m.id === picks[st.use]) ?? pinned ?? options.find((m) => m.id === st.model) ?? options[0]
    if (model) return { use: st.use, status: 'install', id: model.id, name: model.name, model, disk_gb: model.disk_gb, fit: model.fit, options, chosen: model === pinned }
    // The models list is not here yet: the core's own words about its recommendation.
    return { use: st.use, status: 'install', id: st.model, name: st.model_name ?? st.model, disk_gb: st.disk_gb, fit: st.fit, options }
  })
}

/** The rows of a plan that install something, one per model (one model may cover two stages). */
export function planInstalls(plan: PlanRow[]): PlanRow[] {
  const seen = new Set<string>()
  return plan.filter((row) => {
    if (row.status !== 'install' || row.skipped || !row.id || seen.has(row.id)) return false
    seen.add(row.id)
    return true
  })
}

/** Minutes to download so many gigabytes at a connection speed, for an honest "about" line. */
export const downloadMinutes = (gigabytes: number, mbitPerSecond = 100): number => Math.max(1, Math.round((gigabytes * 8000) / mbitPerSecond / 60))
