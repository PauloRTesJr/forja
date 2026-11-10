// Pure helpers over job state, shared by the event reducer and the screens.
import type { Job, Stage } from './api'
import { seconds, stageLabel, stageName } from './format'
import { duration } from './time'

const isDone = (j: Job): boolean => j.status === 'succeeded' || j.status === 'failed' || j.status === 'cancelled'

/** When a job was created, in milliseconds; NaN when it does not say. */
const createdAt = (j: Job): number => Date.parse(j.created_at)

/**
 * Insert or replace a job. The list is newest first, and a job it does not hold yet goes where
 * its creation time puts it: a retried job takes the place of the one it retries, and an old
 * job heard of late does not jump to the top. A finished job is never taken back to running by
 * an older snapshot of itself. Keeps array identity when nothing changed.
 */
export function upsertJob(list: Job[] | undefined, job: Job): Job[] {
  const jobs = list ?? []
  const i = jobs.findIndex((j) => j.id === job.id)
  if (i >= 0) {
    if (isDone(jobs[i]!) && !isDone(job)) return jobs
    const next = jobs.slice()
    next[i] = job
    return next
  }
  const made = createdAt(job)
  const at = Number.isNaN(made) ? 0 : jobs.findIndex((j) => !(createdAt(j) > made))
  return at < 0 ? [...jobs, job] : [...jobs.slice(0, at), job, ...jobs.slice(at)]
}

/** The list without the jobs taken off it. Keeps array identity when none of them was there. */
export function withoutJobs(list: Job[] | undefined, ids: string[]): Job[] | undefined {
  if (!list || !list.some((j) => ids.includes(j.id))) return list
  return list.filter((j) => !ids.includes(j.id))
}

/** The list with a page of older jobs after it; a job both hold keeps the list's (newer) state. */
export function withOlderJobs(list: Job[] | undefined, page: Job[]): Job[] {
  const jobs = list ?? []
  const known = new Set(jobs.map((j) => j.id))
  const more = page.filter((j) => !known.has(j.id))
  return more.length ? [...jobs, ...more] : jobs
}

/** Where the next page of older jobs starts: just after the oldest job the list holds. */
export function olderThan(list: Job[]): number | undefined {
  let oldest: number | undefined
  for (const j of list) {
    const t = createdAt(j)
    if (!Number.isNaN(t) && (oldest === undefined || t < oldest)) oldest = t
  }
  // The core lists what was created before this moment: one more keeps a job made in the same
  // millisecond as the oldest from being skipped.
  return oldest === undefined ? undefined : oldest + 1
}

/** Apply a stage.progress event without waiting for the next full job snapshot. */
export function applyProgress(list: Job[] | undefined, jobId: string, stageId: string, step: number, total: number): Job[] | undefined {
  if (!list) return list
  const i = list.findIndex((j) => j.id === jobId)
  if (i < 0) return list
  const job = list[i]!
  const stages = job.stages.map((s) => (s.id === stageId ? { ...s, step, total } : s))
  const next = list.slice()
  next[i] = { ...job, stages }
  return next
}

/** 0..1 completion for one stage. Running stages without step counts sit at a third. */
export function stageFraction(s: Stage): number {
  if (s.status === 'succeeded') return 1
  if (s.status !== 'running') return 0
  if (s.total > 0) return Math.min(1, s.step / s.total)
  return 0.33
}

/** 0..1 completion for a job, each stage weighted equally. */
export function jobFraction(job: Job): number {
  if (job.stages.length === 0) return 0
  return job.stages.reduce((sum, s) => sum + stageFraction(s), 0) / job.stages.length
}

export function previewOf(job: Job): string | undefined {
  for (let i = job.stages.length - 1; i >= 0; i--) {
    const s = job.stages[i]!
    const glb = s.outputs.find((o) => o.kind === 'mesh/glb')
    if (s.status === 'succeeded' && glb) return glb.hash
  }
  return undefined
}

/** Not finished: running, queued, or waiting for the user. */
export const isActive = (j: Job): boolean => j.status === 'queued' || j.status === 'running' || j.status === 'waiting'

/** Waiting for the user to check the views found in its image. */
export const needsAnswer = (j: Job): boolean => j.status === 'waiting' && !!j.sheet?.found

export type PhaseId = 'prepare' | 'shape' | 'paint' | 'rig' | 'finish'
export interface Phase {
  id: PhaseId
  label: string
  stages: Stage[]
}

const phaseLabels: Record<PhaseId, string> = { prepare: 'Prepare', shape: 'Shape', paint: 'Paint', rig: 'Rig', finish: 'Finish' }

function phaseOfUse(use: string): PhaseId | undefined {
  if (use === 'background-removal' || use === 'framing') return 'prepare'
  if (use === 'shape' || use === 'mesh.cleanup' || use === 'mesh.decimate' || use === 'mesh.uv') return 'shape'
  if (use === 'texture' || use.startsWith('texture.') || use === 'bake') return 'paint'
  if (use === 'rig') return 'rig'
  if (use === 'mesh.lods' || use === 'export') return 'finish'
  return undefined
}

/**
 * Groups stages into the pipeline's phases, keeping their order. Ten stage names in a row are
 * too many to read at a glance; a handful of phases are not. A stage type the UI does not know joins
 * the phase before it, so a new adapter never breaks the track.
 */
export function phasesOf(stages: Stage[]): Phase[] {
  const phases: Phase[] = []
  for (const s of stages) {
    const last = phases.at(-1)
    const id = phaseOfUse(s.use) ?? last?.id ?? 'prepare'
    if (last?.id === id) last.stages.push(s)
    else phases.push({ id, label: phaseLabels[id], stages: [s] })
  }
  return phases
}

export type PhaseState = 'done' | 'running' | 'failed' | 'waiting'

export function phaseState(p: Phase): PhaseState {
  if (p.stages.some((s) => s.status === 'failed')) return 'failed'
  if (p.stages.some((s) => s.status === 'running')) return 'running'
  if (p.stages.every((s) => s.status === 'succeeded')) return 'done'
  return 'waiting'
}

export interface Summary {
  headline: string
  note: string
}

/** One plain sentence for a job row: what is happening and where. */
export function summarize(job: Job): Summary {
  const n = job.stages.length
  const done = job.stages.filter((s) => s.status === 'succeeded').length
  const ran = job.stages.reduce((t, s) => t + s.seconds, 0)
  const at = (s: Stage): string => `stage ${job.stages.indexOf(s) + 1} of ${n}`
  switch (job.status) {
    case 'running': {
      const s = job.stages.find((x) => x.status === 'running')
      const overall = `${Math.round(jobFraction(job) * 100)}% overall`
      if (!s) return { headline: 'Running', note: `${done} of ${n} stages done · ${overall}` }
      // Another computer or a hosted model says where; its own word (a queue place) says more than a count.
      const where = s.provider && s.provider !== 'local' ? ` on ${s.provider_label || s.provider}` : ''
      return { headline: `Running ${stageLabel(s.use)}${where}`, note: `${s.note || at(s)} · ${overall}` }
    }
    case 'queued':
      return { headline: 'Waiting to start', note: done ? `${n - done} of ${n} stages left to run` : `${n} stages to run` }
    case 'waiting': {
      const views = job.sheet?.parts?.filter((p) => p.view).length ?? 0
      return { headline: 'Several views found', note: `${views} views of one subject · check them to go on` }
    }
    case 'succeeded':
      // From start to end when the core says so: stage times leave out the waits between them.
      return { headline: `Finished in ${seconds(job.duration_seconds ?? ran)}`, note: `${n} stages` }
    case 'failed': {
      const s = job.stages.find((x) => x.status === 'failed')
      return s ? { headline: `Stopped at ${stageLabel(s.use)}`, note: at(s) } : { headline: 'Failed', note: `${done} of ${n} stages done` }
    }
    case 'cancelled':
      return { headline: 'Cancelled', note: `${done} of ${n} stages done` }
  }
}

/** The time column of the stage list: a duration, or what the stage is doing instead. */
export function stageTime(s: Stage): string {
  switch (s.status) {
    case 'succeeded':
      return seconds(s.seconds)
    case 'running':
      return s.total > 0 ? `${Math.round((s.step / s.total) * 100)}%` : 'running'
    case 'failed':
      return 'failed'
    case 'cancelled':
      return 'cancelled'
    default:
      return '—'
  }
}

// ---- The Queue's view of the jobs ----

/** Work on it has begun: one of its stages runs or has finished. */
export const hasBegun = (j: Job): boolean => j.stages.some((s) => s.status === 'running' || s.status === 'succeeded')

const oldestFirst = (a: Job, b: Job): number => (createdAt(a) || 0) - (createdAt(b) || 0) || (a.id < b.id ? -1 : a.id > b.id ? 1 : 0)

/**
 * The one job to show as running. The core calls every job that holds one of its slots
 * "running", and the engine takes them in turn, so a batch has several at once; the one shown is
 * the one furthest along, and it stays the one (`current`) for as long as it runs, so the row
 * does not change hands between two jobs at the same stage.
 */
export function leadJob(jobs: Job[], current?: string): Job | undefined {
  const flying = jobs.filter((j) => j.status === 'running')
  const kept = flying.find((j) => j.id === current)
  if (kept) return kept
  return flying.sort((a, b) => jobFraction(b) - jobFraction(a) || oldestFirst(a, b))[0]
}

export interface Sections {
  /** Waiting for the user's answer, oldest first. */
  asking: Job[]
  running?: Job
  /** In the order they will run: the ones already handed to the engine, then the rest, oldest first. */
  next: Job[]
  /** Newest first, by when they ended. */
  finished: Job[]
}

export function sections(jobs: Job[], lead?: string): Sections {
  const running = leadJob(jobs, lead)
  const asking = jobs.filter((j) => j.status === 'waiting').sort(oldestFirst)
  const flying = jobs.filter((j) => j.status === 'running' && j !== running).sort(oldestFirst)
  const queued = jobs.filter((j) => j.status === 'queued').sort(oldestFirst)
  const ended = (j: Job): number => Date.parse(j.finished_at ?? j.created_at) || 0
  const finished = jobs.filter(isDone).sort((a, b) => ended(b) - ended(a) || oldestFirst(b, a))
  return { asking, running, next: [...flying, ...queued], finished }
}

/** The line under the screen's title: "1 forging · 28 queued · 1 needs you". */
export function queueSummary(s: Sections): string {
  const parts = [
    s.running && '1 forging',
    s.next.length > 0 && `${s.next.length} queued`,
    s.asking.length > 0 && `${s.asking.length} ${s.asking.length === 1 ? 'needs' : 'need'} you`,
  ]
  return parts.filter(Boolean).join(' · ') || 'Nothing forging'
}

/** How a row presents its job. The core's status says less: see `leadJob`. */
export type RowState = 'asking' | 'running' | 'starting' | 'queued' | 'cancelling' | 'done' | 'failed' | 'cancelled'

export function rowState(job: Job, o: { lead?: boolean; cancelling?: boolean } = {}): RowState {
  switch (job.status) {
    case 'succeeded':
      return 'done'
    case 'failed':
      return 'failed'
    case 'cancelled':
      return 'cancelled'
  }
  if (o.cancelling) return 'cancelling'
  if (job.status === 'waiting') return 'asking'
  if (job.status === 'running' && o.lead) return hasBegun(job) ? 'running' : 'starting'
  return 'queued'
}

/** The stage a job is at: the one running, else the first it has not finished. */
export function currentStage(job: Job): Stage | undefined {
  return job.stages.find((s) => s.status === 'running') ?? job.stages.find((s) => s.status !== 'succeeded')
}

const doneCount = (job: Job): number => job.stages.filter((s) => s.status === 'succeeded').length

/**
 * What the running job is doing: "Texture, step 12 of 30 · stage 6 of 9". Between two stages it
 * names the next one, so the line does not blink.
 */
export function runningLine(job: Job): string {
  const s = currentStage(job)
  if (!s) return 'Finishing'
  const where = s.provider && s.provider !== 'local' ? ` on ${s.provider_label || s.provider}` : ''
  const live = s.status === 'running'
  // A provider's own word (a place in a hosted model's queue) says more than a step count.
  const detail = live && s.note ? `: ${s.note}` : live && s.total > 0 ? `, step ${s.step} of ${s.total}` : ''
  return `${stageName(s)}${where}${detail} · stage ${job.stages.indexOf(s) + 1} of ${job.stages.length}`
}

function ordinal(n: number): string {
  const tens = n % 100
  const suffix = tens >= 11 && tens <= 13 ? 'th' : (['th', 'st', 'nd', 'rd'][n % 10] ?? 'th')
  return `${n}${suffix}`
}

/** A queued job's place, counted from 1: "Next", "3rd in line". */
export function placeLine(place: number): string {
  return place === 1 ? 'Next' : `${ordinal(place)} in line`
}

/** What became of a finished job, without the time: "Took 2 min 5 s", "Stopped at Texture". */
export function outcomeLine(job: Job): string {
  const n = job.stages.length
  const done = doneCount(job)
  const at = currentStage(job)
  switch (job.status) {
    case 'succeeded': {
      const took = job.duration_seconds ?? job.stages.reduce((t, s) => t + s.seconds, 0)
      return took > 0 ? `Took ${duration(took)}` : 'Finished'
    }
    case 'failed': {
      const failed = job.stages.find((s) => s.status === 'failed')
      if (failed) return `Stopped at ${stageName(failed)}`
      return done === 0 ? 'Did not start' : `Stopped after ${done} of ${n} stages`
    }
    case 'cancelled':
      return done === 0 || !at ? 'Cancelled before it started' : `Cancelled at ${stageName(at)} · ${done} of ${n} stages done`
    default:
      return ''
  }
}

/** Seconds the running job still needs by the core's estimate; undefined when there is none or it has passed. */
export function secondsLeft(job: Job, elapsed: number | undefined): number | undefined {
  if (!job.estimate_seconds || elapsed === undefined) return undefined
  const left = job.estimate_seconds - elapsed
  return left > 1 ? left : undefined
}

export interface Failure {
  /** One or two plain sentences. */
  summary: string
  /** The raw error, to show on request. */
  detail?: string
}

const known: [RegExp, string][] = [
  [/UnidentifiedImageError|cannot identify image/i, 'The image could not be read. It may be damaged, or not a PNG, JPEG or WebP file.'],
  [/out of memory|OutOfMemoryError|CUBLAS_STATUS_ALLOC_FAILED/i, 'The graphics card ran out of memory. Close other programs that use it or choose a lighter quality, then retry.'],
  [/event stream for run|no run [0-9a-f]{8,}/i, 'The engine stopped while this job ran. Retry it.'],
  [/^interrupted: the core stopped/i, 'Forja closed while this job was running, so it did not finish. Retry it.'],
]

// An exception's name, a traceback, or a file path: text written for a log, not for a person.
const raw = /Traceback|\b[A-Z]\w+(Error|Exception)\b|[A-Za-z]:\\|\/\w[\w.-]*\/\w[\w.-]*\//

/**
 * Why a job failed, for its row: the core's sentence, with the raw error kept apart. An older
 * core sends only the raw error: then a sentence is made here and the raw text is the detail.
 */
export function failureOf(job: Job): Failure | undefined {
  if (job.status !== 'failed') return undefined
  const stage = job.stages.find((s) => s.status === 'failed')
  const text = (stage?.error || job.error || '').trim()
  const detail = (stage?.error_detail || job.error_detail || '').trim()
  if (!text) return { summary: 'It failed without saying why. Retry it.', detail: detail || undefined }
  if (detail && detail !== text) return { summary: text, detail }
  const match = known.find(([pattern]) => pattern.test(text))
  if (match) return { summary: match[1], detail: text }
  if (raw.test(text) || text.length > 240) {
    return { summary: `${stage ? stageName(stage) : 'The job'} failed. The details say why.`, detail: text }
  }
  return { summary: text }
}

/** A preset by its display name; an id the list does not know is at least made readable. */
export function presetName(id: string, presets: { id: string; name: string }[] | undefined): string {
  const found = presets?.find((p) => p.id === id)?.name
  if (found) return found
  const words = id.replace(/[-_]+/g, ' ').trim()
  return words ? words[0]!.toUpperCase() + words.slice(1) : id
}

/** What to tell a screen reader when a job's status changes; undefined when it is not news. */
export function announcement(before: Job['status'] | undefined, job: Job): string | undefined {
  if (before === undefined || before === job.status) return undefined
  switch (job.status) {
    case 'succeeded':
      return `${job.name} finished.`
    case 'failed':
      return `${job.name} failed.`
    case 'cancelled':
      return `${job.name} was cancelled.`
    case 'waiting':
      return `${job.name} needs your answer.`
    default:
      return undefined
  }
}

/** The stage track for assistive technology: "Texture, 5 of 9 stages done". */
export function progressText(stages: Stage[]): string {
  const done = stages.filter((s) => s.status === 'succeeded').length
  const at = stages.find((s) => s.status === 'running') ?? stages.find((s) => s.status === 'failed')
  return `${at ? `${stageName(at)}, ` : ''}${done} of ${stages.length} stages done`
}
