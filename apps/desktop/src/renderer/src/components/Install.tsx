import { useMutation, useQuery, type UseQueryResult } from '@tanstack/react-query'
import { useState, type CSSProperties } from 'react'
import { api, cancelTask, installModel, runFix, taskDone, type Check, type Checks, type Fit, type InstallTask, type License, type Model, type Setup } from '../lib/api'
import { keys } from '../lib/events'
import { bytes, stageLabel } from '../lib/format'
import { canRetry, stepProgress, stopped, taskFor, taskPhase } from '../lib/installTasks'
import { cost, fitLabel, isInstalled, licenseName, runsOn, stageOf } from '../lib/models'
import { useToast } from '../lib/toast'
import { ErrorBlock, Pill, Progress, type Tone } from './Controls'
import { Dialog, DialogActions } from './Dialog'
import { AlertIcon, CheckIcon, ChevronIcon, CloseIcon, SkippedIcon } from './icons'

export { taskFor }

export function useInstalls() {
  return useQuery({ queryKey: keys.installs, queryFn: () => api<InstallTask[]>('GET', '/v1/installs') })
}

/**
 * System checks change outside the app (an installer finishes, a driver updates), so they are
 * read again when the window regains focus: the one exception to push-only data.
 */
export function useChecks(models: string[], enabled = true, stages: string[] = []) {
  const query = new URLSearchParams({ models: models.join(',') })
  if (stages.length > 0) query.set('stages', stages.join(','))
  return useQuery({
    queryKey: keys.checks(models, stages),
    queryFn: () => api<Checks>('GET', `/v1/checks?${query}`),
    enabled: enabled && (models.length > 0 || stages.length > 0),
    staleTime: 0,
    refetchOnWindowFocus: 'always',
  })
}

const fitTone: Partial<Record<Fit, Tone>> = { fits: 'ok', offload: 'warn' }

export function FitBadge({ fit, on }: { fit?: Fit; on?: string | null }) {
  if (!fit) return null
  return <Pill tone={fitTone[fit]}>{fitLabel(fit, on)}</Pill>
}

/** A license in a sentence: its name (a link to its text), then what it does not allow on a line of its own. */
export function LicenseLine({ license }: { license: License }) {
  return (
    <span className="license-line">
      {license.url ? (
        <a href={license.url} target="_blank" rel="noreferrer">
          {licenseName(license.spdx)}
        </a>
      ) : (
        licenseName(license.spdx)
      )}
      {!license.commercial && <Pill tone="warn">Non-commercial</Pill>}
      {license.restrictions.length > 0 && <span className="restrictions">Limits: {license.restrictions.join('; ')}.</span>}
    </span>
  )
}

function StepGlyph({ status }: { status: string }) {
  if (status === 'done') return <CheckIcon size={12} strokeWidth={2.5} />
  if (status === 'failed') return <CloseIcon size={12} strokeWidth={2.5} />
  if (status === 'skipped') return <SkippedIcon size={12} />
  return <i />
}

/**
 * An install, removal or fix: its steps, where it is now, and when it stopped, why and what to
 * do next. `onRetry` adds the button that starts it again after a failure or a cancel.
 */
export function TaskProgress({ task, compact, onRetry }: { task: InstallTask; compact?: boolean; onRetry?: () => void }) {
  const toast = useToast()
  const [open, setOpen] = useState(false)
  // The task a cancel was asked for: it keeps running until the engine reaches a point it can stop at.
  const [asked, setAsked] = useState<string>()
  const cancel = useMutation({
    mutationFn: () => cancelTask(task),
    onSuccess: () => setAsked(task.id),
    onError: (e: Error) => toast(e.message, true),
  })
  const phase = taskPhase(task, asked === task.id)
  const active = !taskDone(task)
  const step = stepProgress(task)
  const stop = stopped(task, phase)
  const visible = task.steps.filter((s) => s.status !== 'skipped')
  const log = !compact && task.log.length > 0
  const last = task.log.at(-1)?.trim()
  return (
    <div className={`task ${phase}`}>
      {visible.length > 1 && (
        <ol className="task-steps">
          {visible.map((s) => (
            <li key={s.id} className={s.status}>
              <span className="task-glyph" aria-hidden>
                <StepGlyph status={s.status} />
              </span>
              {s.label}
            </li>
          ))}
        </ol>
      )}
      {active && (
        <>
          <p className="task-now">
            {phase === 'queued'
              ? 'Waiting for another install to finish.'
              : phase === 'cancelling'
                ? 'Cancelling: it stops once the file it is on is done.'
                : (step?.text ?? 'Starting…')}
          </p>
          <Progress value={phase === 'running' ? step?.fraction : undefined} label={step?.label ?? 'Progress'} />
          {!compact && last && phase === 'running' && <p className="task-last mono dim truncate">{last}</p>}
        </>
      )}
      {stop &&
        (phase === 'cancelled' ? (
          <p className="muted">
            {stop.title} {stop.next}
          </p>
        ) : (
          <ErrorBlock>
            <strong>{stop.title}</strong>
            {phase === 'failed' && task.error && <span className="task-error mono">{task.error}</span>}
            {stop.next && <span>{stop.next}</span>}
          </ErrorBlock>
        ))}
      {(log || active || (onRetry && canRetry(phase))) && (
        <div className="row task-actions">
          {log ? (
            <button className="disclosure" aria-expanded={open} onClick={() => setOpen(!open)}>
              <ChevronIcon size={14} />
              {open ? 'Hide details' : 'Show details'}
            </button>
          ) : (
            <span />
          )}
          {active && (
            <button className="btn ghost sm" onClick={() => cancel.mutate()} disabled={cancel.isPending || phase === 'cancelling'}>
              {phase === 'cancelling' ? 'Cancelling…' : 'Cancel'}
            </button>
          )}
          {onRetry && canRetry(phase) && (
            <button className="btn sm" onClick={onRetry}>
              {phase === 'failed' ? 'Try again' : 'Continue'}
            </button>
          )}
        </div>
      )}
      {open && log && <pre className="code task-log">{task.log.join('\n')}</pre>}
    </div>
  )
}

/** What a machine needs, each row with its fix: run it for the user, or steps and a link. */
export function CheckList({ checks }: { checks: Check[] }) {
  return (
    <ul className="checks">
      {checks.map((c, i) => (
        <CheckRow key={c.id} check={c} i={i} />
      ))}
    </ul>
  )
}

/**
 * The checks as one line while they all pass (their detail on request), and as the list of
 * what to fix when they do not. Nothing while there is nothing to check.
 */
export function ChecksSummary({ checks }: { checks: UseQueryResult<Checks> }) {
  const [open, setOpen] = useState(false)
  if (checks.isPending) return checks.fetchStatus === 'idle' ? null : <div className="skeleton" style={{ height: 28 }} />
  if (checks.error) {
    return (
      <ErrorBlock>
        <strong>This computer could not be checked.</strong>
        <span>{(checks.error as Error).message}</span>
        <button className="text-btn" onClick={() => void checks.refetch()} disabled={checks.isFetching}>
          {checks.isFetching ? 'Checking…' : 'Try again'}
        </button>
      </ErrorBlock>
    )
  }
  const list = checks.data.checks
  const failing = list.filter((c) => !c.ok).length
  if (failing === 0) {
    return (
      <div className="checks-summary">
        <div className="checks-ok">
          <CheckIcon size={14} strokeWidth={2.5} />
          <span>This computer has what the install needs.</span>
          <button className="disclosure" aria-expanded={open} onClick={() => setOpen(!open)}>
            <ChevronIcon size={14} />
            {open ? 'Hide' : 'Details'}
          </button>
        </div>
        {open && <CheckList checks={list} />}
      </div>
    )
  }
  return (
    <div className="checks-summary">
      <div className="checks-need">
        <strong>{failing === 1 ? 'One thing to fix before installing' : `${failing} things to fix before installing`}</strong>
        <button className="btn ghost sm" onClick={() => void checks.refetch()} disabled={checks.isFetching}>
          {checks.isFetching ? 'Checking…' : 'Check again'}
        </button>
      </div>
      <CheckList checks={list} />
    </div>
  )
}

function CheckRow({ check, i }: { check: Check; i: number }) {
  const toast = useToast()
  const installs = useInstalls()
  const running = taskFor(installs.data, 'fix', check.id)
  const [how, setHow] = useState(false)
  const fix = useMutation({ mutationFn: () => runFix(check.id), onError: (e: Error) => toast(e.message, true) })
  const busy = fix.isPending || (running !== undefined && !taskDone(running))
  return (
    <li className={`check enter ${check.ok ? 'ok' : 'bad'}`} style={{ '--i': i } as CSSProperties}>
      <span className="check-mark" role="img" aria-label={check.ok ? 'passes' : 'needs attention'}>
        {check.ok ? <CheckIcon size={14} strokeWidth={2.5} /> : <AlertIcon size={14} />}
      </span>
      <div className="check-body">
        <div className="check-head">
          <strong>{check.label}</strong>
          <span className="mono dim">{check.found}</span>
        </div>
        {!check.ok && (
          <>
            <p className="check-why">
              Needs {check.need}. {check.why}
            </p>
            {check.fix && (
              <div className="row">
                {check.fix.runnable && (
                  <button className="btn sm" onClick={() => fix.mutate()} disabled={busy}>
                    {busy ? 'Installing…' : check.fix.label}
                  </button>
                )}
                {(check.fix.steps.length > 0 || check.fix.url) && (
                  <button className="btn ghost sm" onClick={() => setHow(!how)} aria-expanded={how}>
                    {check.fix.runnable ? 'Show me how' : check.fix.label}
                  </button>
                )}
                {check.fix.admin && check.fix.runnable && !busy && <span className="muted small">Windows will ask for permission.</span>}
              </div>
            )}
            {how && check.fix && (
              <div className="how">
                <ol>
                  {check.fix.steps.map((s) => (
                    <li key={s}>{s}</li>
                  ))}
                </ol>
                {check.fix.url && (
                  <a href={check.fix.url} target="_blank" rel="noreferrer">
                    {check.fix.url}
                  </a>
                )}
                {check.fix.command && <code className="mono">{check.fix.command}</code>}
              </div>
            )}
            {running && (!taskDone(running) || running.status === 'failed') && <TaskProgress task={running} compact onRetry={() => fix.mutate()} />}
          </>
        )}
      </div>
    </li>
  )
}

/**
 * Installs one of this computer's models: what it takes and what the computer needs for it, its
 * license, then the install itself. Closing it never stops an install: the model's row and the
 * sidebar keep showing it.
 */
export function InstallDialog({ model: m, onClose }: { model: Model; onClose: () => void }) {
  const toast = useToast()
  const installs = useInstalls()
  const setup = useQuery({ queryKey: keys.setup, queryFn: () => api<Setup>('GET', '/v1/setup') })
  const task = taskFor(installs.data, 'install', m.id, m.provider)
  const phase = taskPhase(task)
  const active = task !== undefined && !taskDone(task)
  const installed = isInstalled(m)
  const checks = useChecks([m.id], !active && !installed)
  const [accepted, setAccepted] = useState(!m.needs_acceptance || !!m.accepted_at)
  const install = useMutation({
    mutationFn: () => installModel(m.id, m.needs_acceptance && accepted, m.provider),
    onError: (e: Error) => toast(e.message, true),
  })
  const use = stageOf(m)
  const free = setup.data?.system?.disk_free_bytes
  const where = runsOn(m)

  if (installed) {
    return (
      <Dialog title={`${m.name} is installed`} onClose={onClose}>
        <p className="muted">
          {use && m.uses.some((u) => u.use === use)
            ? `${stageLabel(use)} uses it from now on.`
            : `It is ready. To run it for ${use ? stageLabel(use).toLowerCase() : 'its step'}, press Use on its row.`}
        </p>
        <DialogActions>
          <button className="btn primary" onClick={onClose} autoFocus>
            Done
          </button>
        </DialogActions>
      </Dialog>
    )
  }

  if (active) {
    return (
      <Dialog title={`Installing ${m.name}`} onClose={onClose}>
        <TaskProgress task={task} />
        <p className="muted small">You can close this: the install goes on, and the Models screen shows it.</p>
        <DialogActions>
          <button className="btn" onClick={onClose}>
            Hide
          </button>
        </DialogActions>
      </Dialog>
    )
  }

  const again = task !== undefined && canRetry(phase)
  const blocked = !accepted ? 'Accept the license to install.' : checks.data && !checks.data.ok ? 'Fix what is listed above to install.' : undefined
  return (
    <Dialog title={`Install ${m.name}`} onClose={onClose}>
      {m.description && <p>{m.description}</p>}
      <dl className="kv install-facts">
        <div className="kv-row">
          <dt>Download</dt>
          <dd>
            {cost(m).value}
            {free !== undefined && <span className="dim"> · {bytes(free)} free on this disk</span>}
          </dd>
        </div>
        <div className="kv-row">
          <dt>Runs on</dt>
          <dd>
            {where.value}
            {where.detail && <span className={where.tone === 'warn' ? 'warn-text' : 'dim'}> · {where.detail}</span>}
          </dd>
        </div>
      </dl>
      {again && <TaskProgress task={task} />}
      <ChecksSummary checks={checks} />
      {m.needs_acceptance && !m.accepted_at && (
        <label className="accept">
          <input type="checkbox" checked={accepted} onChange={(e) => setAccepted(e.target.checked)} />
          <span>
            I accept the license: <LicenseLine license={m.license} />
          </span>
        </label>
      )}
      {blocked && <p className="muted small">{blocked}</p>}
      <DialogActions>
        <button className="btn ghost" onClick={onClose}>
          Cancel
        </button>
        <button className="btn primary" onClick={() => install.mutate()} disabled={!accepted || !checks.data?.ok || install.isPending}>
          {again ? (phase === 'failed' ? 'Try again' : 'Continue install') : 'Install'}
        </button>
      </DialogActions>
    </Dialog>
  )
}
