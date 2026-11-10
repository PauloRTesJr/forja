// What Generate shows of the jobs it started, between "Generate" and the queue: each one being
// prepared, asking about its views, running, done or failed. Pure, so the rules have tests.
import type { Job } from './api'
import { jobFraction, needsAnswer } from './jobs'

/**
 * A job still looking for several views in its image, which may need the user in a moment.
 * One that found them is still preparing until it waits: the two arrive as separate updates.
 */
export function isPreparing(j: Job): boolean {
  const sh = j.sheet
  if (sh?.mode !== 'ask' || (j.status !== 'queued' && j.status !== 'running')) return false
  return !sh.checked || (sh.found && !sh.answer)
}

export type RowState = 'asking' | 'failed' | 'preparing' | 'running' | 'queued' | 'done'

/**
 * Where a job stands for the person who started it; undefined for one that has nothing left to
 * say. A cancelled job is one of those: its owner cancelled it, so it is never reported as
 * queued, and a failed one is reported as failed.
 */
export function rowState(j: Job): RowState | undefined {
  if (needsAnswer(j)) return 'asking'
  if (isPreparing(j)) return 'preparing'
  switch (j.status) {
    case 'failed':
      return 'failed'
    case 'running':
      return 'running'
    case 'queued':
    case 'waiting':
      return 'queued'
    case 'succeeded':
      return 'done'
    case 'cancelled':
      return undefined
  }
}

export interface ActivityRow {
  job: Job
  state: RowState
}

// What needs the user comes first, then what went wrong, then work in order of how far it is.
const ORDER: RowState[] = ['asking', 'failed', 'preparing', 'running', 'queued', 'done']

/**
 * The rows of the activity panel: every job that asks about its views or is being prepared
 * (also one from before a restart), and the jobs started here (`tracked`) for as long as they
 * have something to say. At most `limit` rows, except that a job that asks is never hidden;
 * `more` counts the rest, which the queue lists.
 */
export function activity(jobs: Job[] | undefined, tracked: readonly string[], limit = 4): { rows: ActivityRow[]; more: number } {
  const mine = new Set(tracked)
  const all: ActivityRow[] = []
  for (const job of jobs ?? []) {
    const state = rowState(job)
    if (!state) continue
    if (state === 'asking' || state === 'preparing' || mine.has(job.id)) all.push({ job, state })
  }
  // Array.sort is stable: within a state the list's own order (newest first) stays.
  all.sort((a, b) => ORDER.indexOf(a.state) - ORDER.indexOf(b.state))
  const asking = all.filter((r) => r.state === 'asking').length
  const rows = all.slice(0, Math.max(limit, asking))
  return { rows, more: all.length - rows.length }
}

/**
 * The jobs still worth following when new ones are started: those that have not ended. What
 * ended (done, failed, cancelled, or gone from the list) makes room for the new batch.
 */
export function stillTracked(tracked: readonly string[], jobs: Job[] | undefined): string[] {
  const active = new Set((jobs ?? []).filter((j) => j.status === 'queued' || j.status === 'running' || j.status === 'waiting').map((j) => j.id))
  return tracked.filter((id) => active.has(id))
}

/** One line under a row's name: what is happening to the job now. */
export function rowStatus({ job, state }: ActivityRow): string {
  switch (state) {
    case 'asking': {
      const views = job.sheet?.parts?.filter((p) => p.view).length ?? 0
      return `Shows ${views} views of one subject. Check them to go on.`
    }
    case 'preparing': {
      const bg = job.stages.find((s) => s.use === 'background-removal')
      if (job.status === 'queued' || !bg || bg.status === 'queued') return 'Waiting for the engine'
      if (bg.status === 'running') return 'Removing the background'
      return 'Looking for several views of the subject'
    }
    case 'failed':
      return job.stages.find((s) => s.status === 'failed')?.error ?? job.error ?? 'It failed without saying why.'
    case 'running':
      return `Generating · ${Math.round(jobFraction(job) * 100)}%`
    case 'queued':
      return 'Queued'
    case 'done':
      return 'Done'
  }
}

/** 0..1 for a row's progress bar; undefined when there is no measure of it yet. */
export function rowProgress({ job, state }: ActivityRow): number | undefined {
  if (state === 'running') return jobFraction(job)
  if (state !== 'preparing') return undefined
  const bg = job.stages.find((s) => s.use === 'background-removal')
  return bg?.status === 'running' && bg.total > 0 ? bg.step / bg.total : undefined
}

/** What the toast says once the core has taken the jobs. */
export function queuedMessage(created: Pick<Job, 'name'>[]): string {
  return created.length === 1 ? `${created[0]!.name} added to the queue` : `${created.length} images added to the queue`
}
