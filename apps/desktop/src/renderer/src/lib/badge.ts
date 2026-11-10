// What the sidebar's queue badge counts and how it says it, in the Queue's own words.
import type { Job } from './api'
import { isActive, queueSummary, sections } from './jobs'

/** The jobs that are running, queued or waiting for an answer, and the same in words for a screen reader. */
export function queueBadge(jobs: Job[] | undefined): { count: number; label: string } {
  const active = (jobs ?? []).filter(isActive)
  return { count: active.length, label: queueSummary(sections(active)).replace(/ · /g, ', ') }
}
