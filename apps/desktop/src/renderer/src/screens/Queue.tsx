import { useMutation, useQuery } from '@tanstack/react-query'
import { useState } from 'react'
import { Empty } from '../components/Empty'
import { Alert, Chevron } from '../components/icons'
import { ScreenHead } from '../components/ScreenHead'
import { StageList, StageTrack } from '../components/StageTrack'
import { api, artifactURL, cancelJob, type Job } from '../lib/api'
import { keys } from '../lib/events'
import { relative, seconds } from '../lib/format'
import { isActive, summarize } from '../lib/jobs'
import { transition, useNavigate } from '../lib/nav'
import { useToast } from '../lib/toast'

export function Queue() {
  const jobs = useQuery({ queryKey: keys.jobs, queryFn: () => api<Job[]>('GET', '/v1/jobs?limit=100') })
  const active = jobs.data?.filter(isActive).length ?? 0
  return (
    <div className="screen">
      <ScreenHead eyebrow={['Arcanum II', 'The Hourglass']} title="Queue" sub={active ? `${active} running or waiting` : 'Nothing running'} />
      {jobs.isPending && (
        <div className="jobs">
          {[0, 1, 2].map((i) => (
            <div key={i} className="skeleton" style={{ height: 180 }} />
          ))}
        </div>
      )}
      {jobs.data?.length === 0 && <Empty title="No jobs yet">Drop an image on Generate to start one.</Empty>}
      <div className="jobs">
        {jobs.data?.map((j, i) => <JobRow key={j.id} job={j} index={i} />)}
      </div>
    </div>
  )
}

function JobRow({ job, index }: { job: Job; index: number }) {
  const navigate = useNavigate()
  const toast = useToast()
  const cancel = useMutation({ mutationFn: () => cancelJob(job.id), onError: (e: Error) => toast(e.message, true) })
  // Null follows the job: the running job shows its stages until the user decides otherwise.
  const [open, setOpen] = useState<boolean | null>(null)
  const shown = open ?? job.status === 'running'
  const done = !isActive(job)
  const { headline, note } = summarize(job)
  const ran = job.stages.reduce((t, s) => t + (s.cached ? 0 : s.seconds), 0)
  const error = job.stages.find((s) => s.status === 'failed')?.error ?? job.error
  return (
    <article className={`job enter ${job.status}`} style={{ '--i': Math.min(index, 8) } as React.CSSProperties}>
      <div className="job-thumb">
        <img src={artifactURL(job.input)} alt="" loading="lazy" decoding="async" />
      </div>
      <div className="job-body">
        <div className="job-head">
          <div className="job-title">
            <strong>{job.name}</strong>
            <span className={`status ${job.status}`}>
              <i aria-hidden />
              {job.status}
            </span>
          </div>
          <p className="job-summary">
            <b>{headline}</b> · {note}
          </p>
          <span className="job-meta">
            {job.preset} · {when(job)}
            {job.status === 'running' && ran > 0 && ` · ${seconds(ran)} so far`}
          </span>
        </div>
        <StageTrack stages={job.stages} />
        {error && (
          <div className="error">
            <Alert />
            <span>{error}</span>
          </div>
        )}
        {job.stages.length > 0 && (
          <button className="disclosure" aria-expanded={shown} onClick={() => setOpen(!shown)}>
            <Chevron />
            {shown ? 'Hide stages' : `Show all ${job.stages.length} stages`}
          </button>
        )}
        {shown && <StageList stages={job.stages} />}
      </div>
      <div className="job-actions">
        {!done && (
          <button className="btn" onClick={() => cancel.mutate()} disabled={cancel.isPending}>
            Cancel
          </button>
        )}
        {job.asset_id && (
          <button className="btn primary" onClick={() => transition(() => navigate({ screen: 'asset', id: job.asset_id! }))}>
            Open
          </button>
        )}
      </div>
    </article>
  )
}

function when(job: Job): string {
  switch (job.status) {
    case 'queued':
      return `queued ${relative(job.created_at)}`
    case 'running':
      return `started ${relative(job.created_at)}`
    default:
      return `${job.status === 'succeeded' ? 'finished' : job.status} ${relative(job.finished_at ?? job.created_at)}`
  }
}
