// Pure helpers over job state, shared by the event reducer and the screens.
import type { Job, Stage } from './api'
import { seconds, stageLabel } from './format'

/** Insert or replace a job, newest first. Keeps array identity when nothing changed. */
export function upsertJob(list: Job[] | undefined, job: Job): Job[] {
  const jobs = list ?? []
  const i = jobs.findIndex((j) => j.id === job.id)
  if (i < 0) return [job, ...jobs]
  const next = jobs.slice()
  next[i] = job
  return next
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
  const cached = job.stages.filter((s) => s.cached).length
  const ran = job.stages.reduce((t, s) => t + (s.cached ? 0 : s.seconds), 0)
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
      return {
        headline: ran > 0 ? `Finished in ${seconds(ran)}` : 'Finished from cache',
        note: cached ? `${cached} of ${n} stages reused from cache` : `${n} stages`,
      }
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
      return s.cached ? 'cached' : seconds(s.seconds)
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
