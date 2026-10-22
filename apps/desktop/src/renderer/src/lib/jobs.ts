// Pure helpers over job state, shared by the event reducer and the screens.
import type { Job, Stage } from './api'

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

export const isActive = (j: Job): boolean => j.status === 'queued' || j.status === 'running'
