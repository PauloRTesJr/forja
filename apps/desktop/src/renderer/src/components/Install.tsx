import { useMutation, useQuery } from '@tanstack/react-query'
import { useState, type CSSProperties } from 'react'
import { api, cancelTask, runFix, taskDone, type Check, type Checks, type Fit, type InstallTask, type License } from '../lib/api'
import { keys } from '../lib/events'
import { bytes } from '../lib/format'
import { fitText, licenseName } from '../lib/models'
import { useToast } from '../lib/toast'
import { Alert, Chevron, Star } from './icons'

export function useInstalls() {
  return useQuery({ queryKey: keys.installs, queryFn: () => api<InstallTask[]>('GET', '/v1/installs') })
}

/** The task for a model or check: the running one if any, else the latest. */
export function taskFor(list: InstallTask[] | undefined, kind: InstallTask['kind'], target: string): InstallTask | undefined {
  const mine = (list ?? []).filter((t) => t.kind === kind && t.target === target)
  return mine.find((t) => !taskDone(t)) ?? mine[0]
}

/**
 * System checks change outside the app (an installer finishes, a driver updates), so they are
 * read again when the window regains focus: the one exception to push-only data.
 */
export function useChecks(models: string[], enabled = true) {
  return useQuery({
    queryKey: keys.checks(models),
    queryFn: () => api<Checks>('GET', `/v1/checks?models=${encodeURIComponent(models.join(','))}`),
    enabled: enabled && models.length > 0,
    staleTime: 0,
    refetchOnWindowFocus: 'always',
  })
}

export function FitBadge({ fit }: { fit?: Fit }) {
  if (!fit) return null
  return <span className={`chip fit-${fit}`}>{fitText[fit]}</span>
}

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
      {!license.commercial && <span className="chip warn">non-commercial</span>}
      {license.restrictions.length > 0 && <span className="restrictions">{license.restrictions.join('; ')}</span>}
    </span>
  )
}

const stepGlyph: Record<string, string> = { done: '✓', failed: '✕', skipped: '–' }

/** Steps, download progress and the log of an install, removal or fix. */
export function TaskProgress({ task, compact }: { task: InstallTask; compact?: boolean }) {
  const toast = useToast()
  const [open, setOpen] = useState(false)
  const cancel = useMutation({ mutationFn: () => cancelTask(task), onError: (e: Error) => toast(e.message, true) })
  const p = task.progress
  const fraction = p && p.total > 0 ? Math.min(1, p.done / p.total) : undefined
  const visible = task.steps.filter((s) => s.status !== 'skipped')
  return (
    <div className={`task ${task.status}`}>
      <ol className="task-steps">
        {visible.map((s) => (
          <li key={s.id} className={s.status}>
            <span className="task-glyph" aria-hidden>
              {stepGlyph[s.status] ?? <Star size={11} />}
            </span>
            {s.label}
            {s.status === 'running' && p?.step === s.id && p.total > 0 && (
              <span className="task-bytes">
                {bytes(p.done)} of {bytes(p.total)}
              </span>
            )}
          </li>
        ))}
      </ol>
      {task.status === 'queued' && <p className="muted">Waiting for the install before it to finish.</p>}
      {!taskDone(task) && (
        <div className="bar" aria-hidden>
          <i className={fraction === undefined ? 'indeterminate' : ''} style={{ '--p': fraction ?? 0 } as CSSProperties} />
        </div>
      )}
      {task.error && (
        <div className="error">
          <Alert size={16} />
          {task.error}
        </div>
      )}
      <div className="row task-actions">
        {!compact && task.log.length > 0 && (
          <button className="disclosure" aria-expanded={open} onClick={() => setOpen(!open)}>
            <Chevron size={14} />
            {open ? 'Hide details' : 'Show details'}
          </button>
        )}
        {!taskDone(task) && (
          <button className="btn ghost" onClick={() => cancel.mutate()} disabled={cancel.isPending || task.status === 'cancelled'}>
            Cancel
          </button>
        )}
      </div>
      {open && <pre className="code task-log">{task.log.join('\n')}</pre>}
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

function CheckRow({ check, i }: { check: Check; i: number }) {
  const toast = useToast()
  const installs = useInstalls()
  const running = taskFor(installs.data, 'fix', check.id)
  const [how, setHow] = useState(false)
  const fix = useMutation({ mutationFn: () => runFix(check.id), onError: (e: Error) => toast(e.message, true) })
  const busy = fix.isPending || (running !== undefined && !taskDone(running))
  return (
    <li className={`check enter ${check.ok ? 'ok' : 'bad'}`} style={{ '--i': i } as CSSProperties}>
      <span className="check-mark" aria-label={check.ok ? 'passes' : 'needs attention'}>
        {check.ok ? '✓' : '!'}
      </span>
      <div className="check-body">
        <div className="check-head">
          <strong>{check.label}</strong>
          <span className="mono muted">{check.found}</span>
        </div>
        {!check.ok && (
          <>
            <p className="check-why">
              Needs {check.need}. {check.why}
            </p>
            {check.fix && (
              <div className="row">
                {check.fix.runnable && (
                  <button className="btn primary" onClick={() => fix.mutate()} disabled={busy}>
                    {busy ? 'Installing…' : check.fix.label}
                  </button>
                )}
                {(check.fix.steps.length > 0 || check.fix.url) && (
                  <button className="btn ghost" onClick={() => setHow(!how)} aria-expanded={how}>
                    {check.fix.runnable ? 'Show me how' : check.fix.label}
                  </button>
                )}
                {check.fix.admin && check.fix.runnable && !busy && (
                  <span className="muted">Windows will ask for permission.</span>
                )}
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
            {running && (!taskDone(running) || running.status === 'failed') && <TaskProgress task={running} compact />}
          </>
        )}
      </div>
    </li>
  )
}
