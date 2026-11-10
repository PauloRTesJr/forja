import { useMutation } from '@tanstack/react-query'
import { useState } from 'react'
import { activity, rowProgress, rowStatus, type ActivityRow } from '../lib/activity'
import { artifactURL, cancelJob, type Job } from '../lib/api'
import { draftStore } from '../lib/draft'
import { transition, useNavigate } from '../lib/nav'
import { useToast } from '../lib/toast'
import { Progress } from './Controls'
import { AlertIcon, CloseIcon } from './icons'
import { SheetCard, useSheetAnswer } from './SheetCard'

const untrack = (id: string): void => draftStore.set((d) => ({ ...d, tracked: d.tracked.filter((x) => x !== id) }))

/**
 * What became of the images after "Generate", above the workspace: each job being prepared,
 * asking about the views in its image, running, done or failed. One compact row per job; the
 * queue has the rest and the detail.
 */
export function ActivityPanel({ jobs, tracked }: { jobs: Job[] | undefined; tracked: readonly string[] }) {
  const navigate = useNavigate()
  const { rows, more } = activity(jobs, tracked)
  const asking = rows.filter((r) => r.state === 'asking')
  // Undefined until the user opens or closes a check: a lone question then opens by itself.
  const [opened, setOpened] = useState<string | null>()
  const open = opened === undefined ? (asking.length === 1 ? asking[0]!.job.id : null) : opened
  if (rows.length === 0) return null
  return (
    <section className="activity group enter" aria-label="Images you started">
      {rows.map((row) => (
        <ActivityItem
          key={row.job.id}
          row={row}
          open={row.state === 'asking' && open === row.job.id}
          onToggle={() => setOpened(open === row.job.id ? null : row.job.id)}
        />
      ))}
      {more > 0 && (
        <div className="activity-more">
          <button className="text-btn" onClick={() => transition(() => navigate({ screen: 'queue' }))}>
            +{more} more in the queue
          </button>
        </div>
      )}
    </section>
  )
}

function ActivityItem({ row, open, onToggle }: { row: ActivityRow; open: boolean; onToggle: () => void }) {
  const { job, state } = row
  const navigate = useNavigate()
  const toast = useToast()
  const cancel = useMutation({ mutationFn: () => cancelJob(job.id), onError: (e: Error) => toast(e.message, true) })
  const answer = useSheetAnswer(job)
  const working = state === 'preparing' || state === 'running'
  const toQueue = (): void => transition(() => navigate({ screen: 'queue' }))
  return (
    <article className={`activity-item ${state}`} aria-busy={working}>
      <div className="activity-row">
        <div className="thumb">
          <img src={artifactURL(job.input)} alt="" decoding="async" />
        </div>
        <div className="activity-text">
          <strong className="truncate" title={job.name}>
            {job.name}
          </strong>
          <span className="activity-status">
            {state === 'failed' && <AlertIcon size={14} />}
            <span>{rowStatus(row)}</span>
          </span>
        </div>
        {working && <Progress value={rowProgress(row)} label={`${job.name}: progress`} />}
        <div className="activity-actions">
          {state === 'asking' && (
            <>
              <button className="btn sm" aria-expanded={open} onClick={onToggle}>
                {open ? 'Hide' : 'Review'}
              </button>
              {!open && (
                <button className="btn ghost sm" disabled={answer.isPending} onClick={() => answer.mutate({ keep: true })}>
                  Use whole image
                </button>
              )}
            </>
          )}
          {(state === 'asking' || state === 'preparing') && (
            <button className="btn ghost sm" disabled={cancel.isPending} onClick={() => cancel.mutate()}>
              Cancel
            </button>
          )}
          {(state === 'running' || state === 'queued' || state === 'failed') && (
            <button className="btn ghost sm" onClick={toQueue}>
              View in queue
            </button>
          )}
          {state === 'done' && job.asset_id && (
            <button className="btn sm" onClick={() => transition(() => navigate({ screen: 'asset', id: job.asset_id! }))}>
              Open
            </button>
          )}
          {(state === 'done' || state === 'failed') && (
            <button className="btn ghost sm icon" aria-label={`Dismiss ${job.name}`} onClick={() => untrack(job.id)}>
              <CloseIcon />
            </button>
          )}
        </div>
      </div>
      {open && <SheetCard job={job} maxHeight={320} />}
    </article>
  )
}
