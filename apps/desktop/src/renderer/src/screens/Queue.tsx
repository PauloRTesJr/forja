import { useQuery, useQueryClient } from '@tanstack/react-query'
import { memo, useCallback, useEffect, useId, useMemo, useRef, useState, type CSSProperties, type ReactNode } from 'react'
import { ErrorBlock, Pill, StatusPill } from '../components/Controls'
import { Dialog, DialogActions } from '../components/Dialog'
import { Empty } from '../components/Empty'
import { AlertIcon, ChevronIcon, CloseIcon, ImageIcon, NoJobsIcon } from '../components/icons'
import { ScreenHead, SectionHead } from '../components/ScreenHead'
import { SheetCard } from '../components/SheetCard'
import { StageList, StageTrack } from '../components/StageTrack'
import { artifactURL, cancelJob, clearJobs, fetchJob, fetchJobs, fetchPresets, JOBS_PAGE, removeJob, retryJob, type Job, type Status } from '../lib/api'
import { connectionLabel } from '../lib/connection'
import { keys } from '../lib/events'
import { describeMoves, describeNotes } from '../lib/estimate'
import { ApiError } from '../lib/http'
import {
  announcement,
  failureOf,
  hasBegun,
  olderThan,
  outcomeLine,
  placeLine,
  presetName,
  queueSummary,
  rowState,
  runningLine,
  secondsLeft,
  sections,
  upsertJob,
  withOlderJobs,
  withoutJobs,
  type RowState,
} from '../lib/jobs'
import { transition, useNavigate } from '../lib/nav'
import { about, ago, secondsSince, stopwatch, useNow } from '../lib/time'
import { useToast } from '../lib/toast'
import { useCoreState } from '../lib/useCore'

const plural = (n: number, one: string, many = `${one}s`): string => `${n} ${n === 1 ? one : many}`
const added = (set: ReadonlySet<string>, ids: string[]): ReadonlySet<string> => new Set([...set, ...ids])
const dropped = (set: ReadonlySet<string>, id: string): ReadonlySet<string> => new Set([...set].filter((x) => x !== id))

export function Queue() {
  const navigate = useNavigate()
  const toast = useToast()
  const qc = useQueryClient()
  const core = useCoreState()
  // Without its core the app knows nothing new: no job may look as if it were moving.
  const offline = core !== undefined && core.phase !== 'connected'
  const jobs = useQuery({ queryKey: keys.jobs, queryFn: () => fetchJobs() })
  const presets = useQuery({ queryKey: keys.presets, queryFn: () => fetchPresets() })
  const list = jobs.data

  // The running row stays with one job for as long as that job runs.
  const [leadId, setLeadId] = useState<string>()
  const s = useMemo(() => sections(list ?? [], leadId), [list, leadId])
  if (s.running?.id !== leadId) setLeadId(s.running?.id)

  // Asked to stop, until the core says they have; and rows whose retry or removal is on its way.
  const [cancelling, setCancelling] = useState<ReadonlySet<string>>(new Set())
  const [busy, setBusy] = useState<ReadonlySet<string>>(new Set())
  const [open, setOpen] = useState<ReadonlySet<string>>(new Set())
  // Whose views are being checked: undefined shows the first job that asks, null none.
  const [review, setReview] = useState<string | null>()
  const reviewing = review === undefined ? s.asking[0]?.id : review
  // The jobs a "Cancel all" asks about are fixed when it is asked, so its count does not change under the reader.
  const [confirm, setConfirm] = useState<{ kind: 'cancel-all'; ids: string[] } | { kind: 'clear' }>()

  const setJobs = useCallback((change: (list: Job[] | undefined) => Job[] | undefined) => qc.setQueryData<Job[]>(keys.jobs, change), [qc])

  const cancel = useCallback(
    (id: string) => {
      setCancelling((set) => added(set, [id]))
      cancelJob(id).catch(async (e: unknown) => {
        setCancelling((set) => dropped(set, id))
        // It ended before the cancel arrived: show what became of it, which is no error.
        if (e instanceof ApiError && (e.status === 409 || e.status === 404)) {
          try {
            const now = await fetchJob(id)
            setJobs((l) => upsertJob(l, now))
          } catch (again) {
            if (again instanceof ApiError && again.status === 404) setJobs((l) => withoutJobs(l, [id]))
          }
          return
        }
        toast((e as Error).message, true)
      })
    },
    [setJobs, toast],
  )

  const retry = useCallback(
    (id: string) => {
      setBusy((set) => added(set, [id]))
      retryJob(id)
        // The new job takes the old one's place; the core's events say the same again.
        .then((next) => setJobs((l) => upsertJob(withoutJobs(l, [id]), next)))
        .catch((e: Error) => toast(e.message, true))
        .finally(() => setBusy((set) => dropped(set, id)))
    },
    [setJobs, toast],
  )

  const remove = useCallback(
    (id: string) => {
      setBusy((set) => added(set, [id]))
      removeJob(id)
        .then(() => setJobs((l) => withoutJobs(l, [id])))
        .catch((e: Error) => toast(e.message, true))
        .finally(() => setBusy((set) => dropped(set, id)))
    },
    [setJobs, toast],
  )

  const toggle = useCallback((id: string) => setOpen((set) => (set.has(id) ? dropped(set, id) : added(set, [id]))), [])
  const toggleReview = useCallback((id: string, on: boolean) => setReview(on ? id : null), [])
  const show = useCallback((asset: string) => transition(() => navigate({ screen: 'asset', id: asset })), [navigate])

  const active = [...s.asking, ...(s.running ? [s.running] : []), ...s.next]
  const cancelAll = (ids: string[]): void => {
    setConfirm(undefined)
    for (const j of active) if (ids.includes(j.id) && !cancelling.has(j.id)) cancel(j.id)
  }
  const clear = (): void => {
    setConfirm(undefined)
    clearJobs()
      .then((r) => {
        setJobs((l) => withoutJobs(l, r.removed))
        toast(r.removed.length === 0 ? 'Nothing to clear' : `${plural(r.removed.length, 'finished job')} cleared`)
      })
      .catch((e: Error) => toast(e.message, true))
  }

  // Older jobs come a page at a time, as the end of the list scrolls into view.
  const [older, setOlder] = useState<{ loading: boolean; endedAt?: number; error?: string }>({ loading: false })
  const before = list ? olderThan(list) : undefined
  const hasMore = list !== undefined && list.length >= JOBS_PAGE && before !== undefined && before !== older.endedAt
  const loadOlder = useCallback(async () => {
    const have = qc.getQueryData<Job[]>(keys.jobs)
    const from = have ? olderThan(have) : undefined
    if (from === undefined) return
    setOlder((o) => ({ ...o, loading: true, error: undefined }))
    try {
      const page = await fetchJobs(from)
      const now = qc.getQueryData<Job[]>(keys.jobs)
      const merged = withOlderJobs(now, page)
      qc.setQueryData<Job[]>(keys.jobs, merged)
      // A short page, or one that brought nothing new, was the last.
      const more = page.length >= JOBS_PAGE && merged.length > (now?.length ?? 0)
      setOlder({ loading: false, endedAt: more ? undefined : olderThan(merged) })
    } catch (e) {
      setOlder((o) => ({ ...o, loading: false, error: (e as Error).message }))
    }
  }, [qc])
  const sentinel = useRef<HTMLDivElement>(null)
  const wantsMore = hasMore && !older.loading && !older.error && !offline
  useEffect(() => {
    const el = sentinel.current
    if (!el || !wantsMore) return
    const seen = new IntersectionObserver((entries) => entries.some((e) => e.isIntersecting) && void loadOlder(), { rootMargin: '400px' })
    seen.observe(el)
    return () => seen.disconnect()
  }, [wantsMore, loadOlder])

  // What changed, said once for a screen reader: a row moving to another section says nothing by itself.
  const said = useRef<HTMLDivElement>(null)
  const statuses = useRef(new Map<string, Status>())
  useEffect(() => {
    const news: string[] = []
    for (const j of list ?? []) {
      const text = announcement(statuses.current.get(j.id), j)
      if (text) news.push(text)
      statuses.current.set(j.id, j.status)
    }
    if (news.length > 0 && said.current) said.current.textContent = news.length > 3 ? `${news.length} jobs changed.` : news.join(' ')
  }, [list])

  const row = (job: Job, index: number, extra: { lead?: boolean; place?: number } = {}): ReactNode => (
    <JobRow
      key={job.id}
      job={job}
      index={index}
      state={rowState(job, { lead: extra.lead, cancelling: cancelling.has(job.id) })}
      lead={extra.lead}
      place={extra.place}
      preset={presetName(job.preset, presets.data)}
      offline={offline}
      busy={busy.has(job.id)}
      expanded={open.has(job.id)}
      reviewing={reviewing === job.id}
      onCancel={cancel}
      onRetry={retry}
      onRemove={remove}
      onOpen={show}
      onToggle={toggle}
      onReview={toggleReview}
    />
  )

  return (
    <div className="screen capped">
      <ScreenHead title="Queue" sub={(offline && core && connectionLabel(core)) || queueSummary(s)}>
        {active.length > 1 && (
          <button className="btn ghost danger" disabled={offline} onClick={() => setConfirm({ kind: 'cancel-all', ids: active.map((j) => j.id) })}>
            Cancel all
          </button>
        )}
        {s.finished.length > 0 && (
          <button className="btn ghost" disabled={offline} onClick={() => setConfirm({ kind: 'clear' })}>
            Clear finished
          </button>
        )}
      </ScreenHead>
      <div ref={said} className="sr-only" role="status" aria-live="polite" />

      {jobs.isPending && (
        <div className="jobs group" aria-busy>
          {[0, 1, 2, 3].map((i) => (
            <div key={i} className="job">
              <div className="job-row">
                <div className="skeleton thumb" />
                <div className="skeleton job-skeleton" />
              </div>
            </div>
          ))}
        </div>
      )}
      {jobs.error && !list && (
        <>
          <ErrorBlock role="alert">{(jobs.error as Error).message}</ErrorBlock>
          <div className="row job-again">
            <button className="btn" onClick={() => void jobs.refetch()}>
              Try again
            </button>
          </div>
        </>
      )}
      {list?.length === 0 && (
        <Empty
          icon={NoJobsIcon}
          title="No jobs yet"
          action={
            <button className="btn" onClick={() => transition(() => navigate({ screen: 'generate' }))}>
              Go to Generate
            </button>
          }
        >
          Add images on Generate to start one.
        </Empty>
      )}

      {s.asking.length > 0 && (
        <section className="section" aria-label="Needs you">
          <SectionHead>Needs you · {s.asking.length}</SectionHead>
          <ul className="jobs group" role="list">
            {s.asking.map((j, i) => row(j, i))}
          </ul>
        </section>
      )}
      {s.running && (
        <section className="section" aria-label="Running">
          <SectionHead>Running</SectionHead>
          <ul className="jobs group" role="list">
            {row(s.running, 0, { lead: true })}
          </ul>
        </section>
      )}
      {s.next.length > 0 && (
        <section className="section" aria-label="Up next">
          <SectionHead>Up next · {s.next.length}</SectionHead>
          <ul className="jobs group" role="list">
            {s.next.map((j, i) => row(j, i, { place: i + 1 }))}
          </ul>
        </section>
      )}
      {s.finished.length > 0 && (
        <section className="section" aria-label="Finished">
          <SectionHead>
            Finished · {s.finished.length}
            {hasMore ? '+' : ''}
          </SectionHead>
          <ul className="jobs group" role="list">
            {s.finished.map((j, i) => row(j, i))}
          </ul>
        </section>
      )}
      {list !== undefined && list.length > 0 && (hasMore || older.loading || older.error) && (
        <div ref={sentinel} className="jobs-more">
          {older.error ? (
            <span className="dim">{older.error}</span>
          ) : (
            older.loading && <span className="dim">Loading older jobs…</span>
          )}
          {!older.loading && (
            <button className="btn ghost sm" disabled={offline} onClick={() => void loadOlder()}>
              {older.error ? 'Try again' : 'Show older jobs'}
            </button>
          )}
        </div>
      )}

      {confirm?.kind === 'cancel-all' && (
        <Dialog title={`Cancel ${plural(confirm.ids.length, 'job')}?`} size="narrow" alert onClose={() => setConfirm(undefined)}>
          <p className="muted">Each stops where it is. A cancelled job stays in the list and can be retried.</p>
          <DialogActions>
            <button className="btn" onClick={() => setConfirm(undefined)}>
              Keep them
            </button>
            <button className="btn danger" autoFocus onClick={() => cancelAll(confirm.ids)}>
              Cancel {plural(confirm.ids.length, 'job')}
            </button>
          </DialogActions>
        </Dialog>
      )}
      {confirm?.kind === 'clear' && (
        <Dialog title="Clear finished jobs?" size="narrow" alert onClose={() => setConfirm(undefined)}>
          <p className="muted">
            Every finished job leaves this list. Assets stay in the library; failed and cancelled jobs can no longer be retried.
          </p>
          <DialogActions>
            <button className="btn" onClick={() => setConfirm(undefined)}>
              Keep them
            </button>
            <button className="btn primary" autoFocus onClick={clear}>
              Clear finished
            </button>
          </DialogActions>
        </Dialog>
      )}
    </div>
  )
}

interface RowProps {
  job: Job
  index: number
  state: RowState
  /** The one job shown as running: it carries the labelled track. */
  lead?: boolean
  /** Its place in line, from 1, when it waits. */
  place?: number
  preset: string
  offline: boolean
  busy: boolean
  expanded: boolean
  reviewing: boolean
  onCancel: (id: string) => void
  onRetry: (id: string) => void
  onRemove: (id: string) => void
  onOpen: (asset: string) => void
  onToggle: (id: string) => void
  onReview: (id: string, on: boolean) => void
}

const JobRow = memo(function JobRow({ job, index, state, lead, place, preset, offline, busy, expanded, reviewing, onCancel, onRetry, onRemove, onOpen, onToggle, onReview }: RowProps) {
  const toast = useToast()
  const name = useId()
  const stages = useId()
  const [details, setDetails] = useState(false)
  const live = state === 'running' || state === 'starting' || state === 'queued' || state === 'asking' || state === 'cancelling'
  const failure = failureOf(job)
  // A stage that moved to another provider after a failure says so, even while collapsed.
  const moves = describeMoves(job.stages)
  // So does one that made a stand-in for what it was asked, such as a simplification for a quad remesh.
  const notes = describeNotes(job.stages)
  const views = job.sheet?.parts?.filter((p) => p.view).length ?? 0
  // The lead's track is under its row; a finished asset and an untouched job have nothing to show.
  const bar = !lead && job.stages.length > 0 && (state === 'failed' || ((state === 'queued' || state === 'cancelled' || state === 'cancelling') && hasBegun(job)))

  return (
    <li className={`job enter ${job.status}`} data-state={state} data-lead={lead || undefined} aria-labelledby={name} style={{ '--i': Math.min(index, 8) } as CSSProperties}>
      <div className="job-row">
        <Thumb hash={job.input} />
        <div className="job-text">
          <div className="job-title">
            <strong id={name} className="truncate" title={job.name}>
              {job.name}
            </strong>
            <StatePill state={state} offline={offline && live} />
          </div>
          <p className="job-meta truncate">
            {preset}
            {state === 'queued' && place !== undefined && ` · ${placeLine(place)}`}
            {state === 'starting' && ' · Starting'}
            {state === 'running' && (
              <>
                {' · '}
                <span className="job-now">{runningLine(job)}</span>
                {!offline && <Elapsed job={job} />}
              </>
            )}
            {state === 'cancelling' && ' · Stops as soon as the step it is on allows'}
            {state === 'asking' && ` · ${views > 0 ? `${plural(views, 'view')} of one subject found in this image` : 'Several views found in this image'}`}
            {(state === 'done' || state === 'failed' || state === 'cancelled') && (
              <>
                {' · '}
                <span className={state === 'failed' ? 'job-stopped' : undefined}>{outcomeLine(job)}</span>
                {' · '}
                <Ago at={job.finished_at ?? job.created_at} />
              </>
            )}
          </p>
        </div>
        {bar && (
          <div className="job-bar">
            <StageTrack stages={job.stages} compact label={`Progress of ${job.name}`} />
          </div>
        )}
        <div className="job-actions">
          {state === 'asking' && (
            <button className="btn sm" aria-expanded={reviewing} disabled={offline} onClick={() => onReview(job.id, !reviewing)}>
              {reviewing ? 'Hide views' : 'Check views'}
            </button>
          )}
          {live && (
            <button className="btn ghost sm" aria-label={`Cancel ${job.name}`} disabled={offline || state === 'cancelling'} onClick={() => onCancel(job.id)}>
              Cancel
            </button>
          )}
          {(state === 'failed' || state === 'cancelled') && (
            <button className="btn sm" aria-label={`Retry ${job.name}`} disabled={offline || busy} onClick={() => onRetry(job.id)}>
              Retry
            </button>
          )}
          {state === 'done' && job.asset_id && (
            <button className="btn sm" aria-label={`Open ${job.name}`} onClick={() => onOpen(job.asset_id!)}>
              Open
            </button>
          )}
          {!live && (
            <button className="btn ghost sm icon" aria-label={`Remove ${job.name} from the queue`} title="Remove from the queue" disabled={offline || busy} onClick={() => onRemove(job.id)}>
              <CloseIcon />
            </button>
          )}
          {job.stages.length > 0 && (
            <button
              className="btn ghost sm icon job-toggle"
              aria-label={`Stages of ${job.name}`}
              aria-expanded={expanded}
              aria-controls={expanded ? stages : undefined}
              title={expanded ? 'Hide stages' : 'Show stages'}
              onClick={() => onToggle(job.id)}
            >
              <ChevronIcon />
            </button>
          )}
        </div>
      </div>
      {lead && job.stages.length > 0 && (
        <div className="job-more job-track">
          <StageTrack stages={job.stages} label={`Progress of ${job.name}`} />
        </div>
      )}
      {failure && (
        <div className="job-more job-failure" role="group" aria-label="What went wrong">
          <AlertIcon />
          <div>
            <p className="selectable">{failure.summary}</p>
            {failure.detail && (
              <div className="row job-detail-actions">
                <button className="disclosure" aria-expanded={details} onClick={() => setDetails(!details)}>
                  <ChevronIcon size={14} />
                  Details
                </button>
                {details && (
                  <button className="text-btn" onClick={() => void window.forja.copyText(failure.detail!).then(() => toast('Details copied'))}>
                    Copy
                  </button>
                )}
              </div>
            )}
            {details && failure.detail && <pre className="job-detail selectable">{failure.detail}</pre>}
          </div>
        </div>
      )}
      {[...moves, ...notes].map((text) => (
        <p key={text} className="job-more caution" role="status">
          {text}
        </p>
      ))}
      {state === 'asking' && reviewing && !offline && (
        <div className="job-more">
          <SheetCard job={job} />
        </div>
      )}
      {expanded && job.stages.length > 0 && (
        <div className="job-more" id={stages}>
          <StageList stages={job.stages} />
        </div>
      )}
    </li>
  )
})

/** The row's state in a word. Without a connection, work is not shown as live. */
function StatePill({ state, offline }: { state: RowState; offline: boolean }) {
  if (offline) return <Pill dot>No connection</Pill>
  switch (state) {
    case 'asking':
      return <StatusPill status="waiting" />
    case 'running':
      return <StatusPill status="running" />
    case 'starting':
      return (
        <Pill tone="accent" live>
          Starting
        </Pill>
      )
    case 'cancelling':
      return <Pill dot>Cancelling…</Pill>
    case 'queued':
      return <StatusPill status="queued" />
    case 'done':
      return <StatusPill status="succeeded" />
    case 'failed':
      return <StatusPill status="failed" />
    case 'cancelled':
      return <StatusPill status="cancelled" />
  }
}

/** The job's image at thumbnail size; an image that cannot be drawn leaves an icon, not a broken picture. */
function Thumb({ hash }: { hash: string }) {
  const [state, setState] = useState<'loading' | 'shown' | 'broken'>('loading')
  return (
    <div className="thumb job-thumb">
      {state === 'broken' ? (
        <ImageIcon />
      ) : (
        <img src={artifactURL(hash)} alt="" loading="lazy" decoding="async" data-shown={state === 'shown' || undefined} onLoad={() => setState('shown')} onError={() => setState('broken')} />
      )}
    </div>
  )
}

/** How long the running job has run, and what is left of the core's estimate, on the shared clock. */
function Elapsed({ job }: { job: Job }) {
  const now = useNow(1000)
  const elapsed = secondsSince(job.started_at, now)
  if (elapsed === undefined) return null
  const left = secondsLeft(job, elapsed)
  return (
    <>
      {' · '}
      <span className="num">{stopwatch(elapsed)}</span>
      {left !== undefined && ` · ${about(left)} left`}
    </>
  )
}

/** "3 min ago", kept true by the shared clock. */
function Ago({ at }: { at: string }) {
  const now = useNow(30_000)
  return <time dateTime={at}>{ago(at, now)}</time>
}
