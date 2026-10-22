import { useMutation, useQuery } from '@tanstack/react-query'
import { Empty } from '../components/Empty'
import { StageTrack } from '../components/StageTrack'
import { api, artifactURL, cancelJob, type Job } from '../lib/api'
import { keys } from '../lib/events'
import { relative, seconds } from '../lib/format'
import { isActive, jobFraction } from '../lib/jobs'
import { transition, useNavigate } from '../lib/nav'
import { useToast } from '../lib/toast'

export function Queue() {
  const jobs = useQuery({ queryKey: keys.jobs, queryFn: () => api<Job[]>('GET', '/v1/jobs?limit=100') })
  const active = jobs.data?.filter(isActive).length ?? 0
  return (
    <div className="screen">
      <div className="screen-head">
        <div>
          <h1>Queue</h1>
          <p className="sub">{active ? `${active} running or waiting` : 'Nothing running'}</p>
        </div>
      </div>
      {jobs.isPending && (
        <div className="jobs">
          {[0, 1, 2].map((i) => (
            <div key={i} className="skeleton" style={{ height: 102 }} />
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
  const done = !isActive(job)
  const fraction = jobFraction(job)
  const ran = job.stages.reduce((t, s) => t + (s.cached ? 0 : s.seconds), 0)
  const cached = job.stages.filter((s) => s.cached).length
  const failed = job.stages.find((s) => s.status === 'failed')
  return (
    <article className={`job enter ${done ? 'done' : ''}`} style={{ '--i': Math.min(index, 8) } as React.CSSProperties}>
      <div className="job-thumb">
        <img src={artifactURL(job.input)} alt="" loading="lazy" decoding="async" />
      </div>
      <div>
        <div className="job-title">
          <strong>{job.name}</strong>
          <span className="muted">{job.preset}</span>
          <span className={`status ${job.status}`}>{job.status}</span>
          <span className="muted" style={{ marginLeft: 'auto' }}>
            {relative(job.created_at)}
            {ran > 0 && ` · ${seconds(ran)}`}
            {cached > 0 && ` · ${cached} cached`}
          </span>
        </div>
        <div className={`bar ${job.status === 'succeeded' ? 'ok' : job.status === 'failed' ? 'bad' : ''}`}>
          <i style={{ '--p': job.status === 'failed' ? 1 : fraction } as React.CSSProperties} />
        </div>
        <StageTrack stages={job.stages} />
        {failed?.error && <div className="error">{failed.error}</div>}
        {!failed && job.error && <div className="error">{job.error}</div>}
      </div>
      <div className="row" style={{ flexDirection: 'column', alignItems: 'stretch' }}>
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
