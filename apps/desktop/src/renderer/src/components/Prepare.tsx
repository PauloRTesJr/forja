import { useMutation } from '@tanstack/react-query'
import type { CSSProperties } from 'react'
import { artifactURL, cancelJob, type Job } from '../lib/api'
import { useToast } from '../lib/toast'
import { SheetCard } from './SheetCard'

/**
 * A job still looking for several views in its image, which may need the user in a moment.
 * One that found them is still preparing until it waits: the two arrive as separate updates.
 */
export function isPreparing(j: Job): boolean {
  const sh = j.sheet
  if (sh?.mode !== 'ask' || (j.status !== 'queued' && j.status !== 'running')) return false
  return !sh.checked || (sh.found && !sh.answer)
}

function step(job: Job): string {
  const bg = job.stages.find((s) => s.use === 'background-removal')
  if (job.status === 'queued' || !bg || bg.status === 'queued') return 'Waiting for the engine'
  if (bg.status === 'running') return 'Removing the background'
  return 'Looking for several views of the subject'
}

function CancelJob({ job }: { job: Job }) {
  const toast = useToast()
  const cancel = useMutation({ mutationFn: () => cancelJob(job.id), onError: (e: Error) => toast(e.message, true) })
  return (
    <button className="btn ghost" onClick={() => cancel.mutate()} disabled={cancel.isPending}>
      Cancel
    </button>
  )
}

/**
 * What happens to a dropped image before it goes to the queue: its background comes off, then
 * the core looks for several views of one subject, which the user confirms right here.
 */
export function Preparing({ job, index }: { job: Job; index: number }) {
  const bg = job.stages.find((s) => s.use === 'background-removal')
  const fraction = bg?.status === 'running' && bg.total > 0 ? bg.step / bg.total : undefined
  return (
    <article className="prep enter" style={{ '--i': index } as CSSProperties} aria-busy="true">
      <div className="prep-thumb">
        <img src={artifactURL(job.input)} alt="" decoding="async" />
      </div>
      <div className="prep-body">
        <strong className="prep-title">Preparing {job.name}</strong>
        <span className="prep-step">{step(job)}…</span>
        <div className="bar" aria-hidden>
          <i className={fraction === undefined ? 'indeterminate' : ''} style={{ '--p': fraction } as CSSProperties} />
        </div>
        <span className="prep-note">If the image shows several views of one subject, you confirm them here.</span>
      </div>
      <CancelJob job={job} />
    </article>
  )
}

/** A job whose image shows several views of one subject, waiting for the user to confirm them. */
export function Confirm({ job, index }: { job: Job; index: number }) {
  return (
    <article className="prep confirm enter" style={{ '--i': index } as CSSProperties}>
      <div className="prep-head">
        <div className="prep-body">
          <strong className="prep-title">{job.name} shows several views of one subject</strong>
          <span className="prep-step">Confirm which figure is which view, then build from them.</span>
        </div>
        <CancelJob job={job} />
      </div>
      <SheetCard job={job} />
    </article>
  )
}
