import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { api, fetchModels, installModel, pinModel, taskDone, type Job, type Setup as SetupView } from '../lib/api'
import { keys } from '../lib/events'
import { bytes, listOf, stageLabel, taskNoun } from '../lib/format'
import { canRetry, taskPhase } from '../lib/installTasks'
import { isActive, summarize } from '../lib/jobs'
import { SKIP, downloadMinutes, fitLabel, gb, planInstalls, setupPlan, stageRule, type PlanRow } from '../lib/models'
import { transition, useNavigate } from '../lib/nav'
import { useToast } from '../lib/toast'
import { ErrorBlock, Pill } from './Controls'
import { ChecksSummary, FitBadge, LicenseLine, TaskProgress, taskFor, useChecks, useInstalls } from './Install'
import { AlertIcon } from './icons'
import { StageTrack } from './StageTrack'

function machine(setup: SetupView): string {
  const gpu = setup.system?.devices?.find((d) => d.backend !== 'cpu')
  const free = setup.system?.disk_free_bytes
  return [gpu ? `${gpu.name} · ${bytes(gpu.vram_bytes)}` : 'No GPU found', free !== undefined && `${bytes(free)} of disk free`].filter(Boolean).join(' · ')
}

/**
 * The guided path from a fresh install to a first asset: which model each step gets on this
 * computer and what that takes, what the computer still needs, the licenses, the installs,
 * then a test run of the whole pipeline. `onDone` is the way out at every point: "Not now",
 * "Skip" and "Done" all call it.
 */
export function Setup({ onDone }: { onDone?: () => void }) {
  const toast = useToast()
  const navigate = useNavigate()
  const qc = useQueryClient()
  const setup = useQuery({ queryKey: keys.setup, queryFn: () => api<SetupView>('GET', '/v1/setup') })
  const models = useQuery({ queryKey: keys.models, queryFn: fetchModels })
  const installs = useInstalls()
  const jobs = useQuery({ queryKey: keys.jobs, queryFn: () => api<Job[]>('GET', '/v1/jobs?limit=100') })
  const [picks, setPicks] = useState<Record<string, string>>({})
  const [accepted, setAccepted] = useState<Record<string, boolean>>({})
  const [testJob, setTestJob] = useState<string>()

  const plan = setup.data ? setupPlan(setup.data, models.data ?? [], picks) : []
  const wanted = planInstalls(plan)
  const ids = wanted.map((r) => r.id!)
  const checks = useChecks(ids, setup.data !== undefined && !setup.data.ready)
  const licenses = wanted.filter((r) => r.model?.needs_acceptance && !r.model.accepted_at)
  const tasks = wanted.map((r) => ({ row: r, task: taskFor(installs.data, 'install', r.id!, 'local') })).filter((t) => t.task !== undefined)
  const installing = tasks.some((t) => !taskDone(t.task!))
  const checksOk = checks.data?.ok ?? false
  const licensesOk = licenses.every((r) => accepted[r.id!])

  const install = useMutation({
    // One after the other: the engine installs them in that order, and the first refusal stops the rest.
    mutationFn: async (rows: PlanRow[]) => {
      for (const r of rows) await installModel(r.id!, accepted[r.id!] ?? false)
    },
    onError: (e: Error) => toast(e.message, true),
  })
  const unpin = useMutation({
    mutationFn: (use: string) => pinModel(use, ''),
    onSuccess: () => void qc.invalidateQueries({ queryKey: keys.models }),
    onError: (e: Error) => toast(e.message, true),
  })
  const test = useMutation({
    mutationFn: () => api<Job>('POST', '/v1/setup/test'),
    onSuccess: (job) => setTestJob(job.id),
    onError: (e: Error) => toast(e.message, true),
  })
  const job = jobs.data?.find((j) => j.id === testJob)

  if (setup.isPending) {
    return (
      <div className="setup enter" aria-busy>
        <div className="setup-head">
          <strong>Looking at this computer…</strong>
          <span className="muted">Starting the engine to see the GPU and the installed models.</span>
        </div>
        <div className="skeleton" style={{ height: 120 }} />
      </div>
    )
  }
  if (setup.error) {
    return (
      <ErrorBlock>
        <strong className="block">This computer could not be looked at.</strong>
        <span className="block">{(setup.error as Error).message}</span>
        <button className="btn sm" onClick={() => void setup.refetch()} disabled={setup.isFetching}>
          {setup.isFetching ? 'Looking…' : 'Try again'}
        </button>
      </ErrorBlock>
    )
  }
  const s = setup.data

  if (testJob) {
    const failed = job?.status === 'failed'
    const ended = job !== undefined && !isActive(job)
    return (
      <div className="setup enter">
        <div className="setup-head">
          <strong>Test run</strong>
          <span className="muted">The bundled sample image, through every step of the default preset.</span>
        </div>
        {job ? (
          <>
            <StageTrack stages={job.stages} />
            {failed ? (
              <ErrorBlock>{job.error ?? 'The test run failed.'}</ErrorBlock>
            ) : (
              <p className="muted" role="status">
                {job.status === 'cancelled' ? 'The test run was cancelled.' : summarize(job).headline}
              </p>
            )}
            {ended && (
              <div className="setup-actions">
                {job.asset_id ? (
                  <button className="btn" onClick={() => transition(() => navigate({ screen: 'asset', id: job.asset_id! }))}>
                    Open the sample
                  </button>
                ) : (
                  <button className="btn" onClick={() => test.mutate()} disabled={test.isPending}>
                    Try again
                  </button>
                )}
                <button className="btn ghost" onClick={() => (onDone ? onDone() : setTestJob(undefined))}>
                  {job.asset_id ? 'Done' : 'Close'}
                </button>
              </div>
            )}
          </>
        ) : (
          <div className="skeleton" style={{ height: 60 }} />
        )}
      </div>
    )
  }

  if (s.ready) {
    return (
      <div className="setup done enter">
        <div className="setup-head">
          <strong>This computer is ready</strong>
          <span className="muted">Every step has a model. Run the bundled sample once to test them together.</span>
        </div>
        <div className="row">
          <button className="btn" onClick={() => test.mutate()} disabled={test.isPending}>
            Run the sample
          </button>
          {onDone && (
            <button className="btn ghost" onClick={onDone}>
              Skip
            </button>
          )}
        </div>
      </div>
    )
  }

  const remote = plan.filter((r) => r.status === 'remote')
  const total = wanted.reduce((sum, r) => sum + (r.disk_gb ?? 0), 0)
  const free = s.system?.disk_free_bytes
  const slower = wanted.some((r) => r.fit === 'offload')
  const blocked =
    wanted.length === 0
      ? 'Choose at least one model to install.'
      : !checksOk
        ? checks.data
          ? 'Fix what is listed above to install.'
          : undefined
        : !licensesOk
          ? `Accept the ${licenses.length === 1 ? 'license' : 'licenses'} above to install.`
          : undefined

  return (
    <div className="setup enter">
      <div className="setup-head">
        <strong>Set up this computer</strong>
        <span className="muted">{machine(s)}</span>
      </div>

      <p className="setup-lead">
        {wanted.length > 0 ? (
          <>
            {wanted.length === 1 ? 'One model' : `${wanted.length} models`} to install: about {gb(Math.round(total * 10) / 10)} to download and keep on disk, roughly{' '}
            {downloadMinutes(total)} min at 100 Mbit/s, plus a few minutes to set each one up. An install can be cancelled and continued later. Until every step has a model, presets that need one cannot run.
          </>
        ) : remote.length === plan.filter((r) => r.status !== 'ready').length ? (
          'No model that is missing can run on this computer.'
        ) : (
          'Nothing is chosen to install.'
        )}
      </p>

      <ul className="setup-plan">
        {plan.map((row) => (
          <PlanLine
            key={row.use}
            row={row}
            rule={stageRule(models.data ?? [], row.use)}
            locked={installing}
            onPick={(id) => setPicks({ ...picks, [row.use]: id })}
            onAutomatic={() => unpin.mutate(row.use)}
          />
        ))}
      </ul>

      {slower && (
        <p className="caution">
          <AlertIcon size={14} />
          "Slower here" means the model needs more GPU memory than this card has, so part of it runs in ordinary memory. It works, and takes longer.
        </p>
      )}
      {free !== undefined && total * 1024 ** 3 > free && (
        <p className="caution">
          <AlertIcon size={14} />
          That is more than the {bytes(free)} free on this disk. Choose smaller models, or free up space first.
        </p>
      )}

      {remote.length > 0 && (
        <div className="notice">
          <div>
            <strong>No {listOf(remote.map((r) => taskNoun(r.use)))} model runs on this computer</strong>
            <span>Its GPU is too small for them. Use another computer that runs Forja, a rented GPU, or a hosted model.</span>
          </div>
          <button className="btn sm" onClick={() => transition(() => navigate({ screen: 'settings' }))}>
            Where models run
          </button>
        </div>
      )}

      {wanted.length > 0 && !installing && <ChecksSummary checks={checks} />}

      {!installing &&
        licenses.map((r) => (
          <label key={r.id} className="accept">
            <input type="checkbox" checked={accepted[r.id!] ?? false} onChange={(e) => setAccepted({ ...accepted, [r.id!]: e.target.checked })} />
            <span>
              I accept the license of <strong>{r.name}</strong>: <LicenseLine license={r.model!.license} />
            </span>
          </label>
        ))}

      {tasks
        .filter((t) => !taskDone(t.task!) || canRetry(taskPhase(t.task)))
        .map(({ row, task }) => (
          <div key={task!.id} className="setup-install">
            <strong>{row.name}</strong>
            <TaskProgress task={task!} onRetry={checksOk && licensesOk ? () => install.mutate([row]) : undefined} />
          </div>
        ))}

      <div className="setup-actions">
        {blocked && !installing && <span className="muted small grow">{blocked}</span>}
        {installing && <span className="muted small grow">Installing. You can leave this screen: the install goes on.</span>}
        {onDone && (
          <button className="btn ghost" onClick={onDone}>
            {installing ? 'Hide' : 'Not now'}
          </button>
        )}
        {!installing && (
          <button className="btn primary" onClick={() => install.mutate(wanted)} disabled={wanted.length === 0 || !checksOk || !licensesOk || install.isPending}>
            {wanted.length === 1 ? `Install ${wanted[0]!.name}` : `Install ${wanted.length} models`}
          </button>
        )}
      </div>
    </div>
  )
}

/** One step of the plan: the model it gets (a choice when several run here), what that takes and how it fits. */
function PlanLine({
  row,
  rule,
  locked,
  onPick,
  onAutomatic,
}: {
  row: PlanRow
  rule?: ReturnType<typeof stageRule>
  locked: boolean
  onPick: (id: string) => void
  onAutomatic: () => void
}) {
  const stage = stageLabel(row.use)
  // The step is set to a model that is not installed: only that model, or automatic, unblocks it.
  const stuck = rule?.missing ? rule.model : undefined
  return (
    <li className={`plan-row ${row.status}`}>
      <span className="plan-stage">{stage}</span>
      <div className="plan-model">
        {row.status === 'ready' ? (
          <span>{row.name}</span>
        ) : row.status === 'remote' ? (
          <span className="dim">Nothing that runs here</span>
        ) : row.options.length > 0 ? (
          <select className="input sm" aria-label={`Model for ${taskNoun(row.use)}`} value={row.skipped ? SKIP : row.id} disabled={locked} onChange={(e) => onPick(e.target.value)}>
            {row.options.map((m) => (
              <option key={m.id} value={m.id}>
                {m.name}
                {m.disk_gb ? ` · ${gb(m.disk_gb)}` : ''}
                {m.fit ? ` · ${fitLabel(m.fit, m.runs_on)}` : ''}
              </option>
            ))}
            <option value={SKIP}>Skip for now</option>
          </select>
        ) : (
          <span>{row.name}</span>
        )}
        {stuck && (
          <span className="caution">
            <AlertIcon size={14} />
            <span>
              {stage} is set to {stuck.name}, which is not installed{row.id === stuck.id ? '' : ' and cannot be installed here'}.{' '}
              <button className="text-btn" onClick={onAutomatic}>
                Back to automatic
              </button>
            </span>
          </span>
        )}
      </div>
      <span className="plan-size num">{row.status === 'install' && !row.skipped && row.disk_gb ? gb(row.disk_gb) : ''}</span>
      <span className="plan-fit">
        {row.status === 'ready' ? (
          <Pill tone="ok" dot>
            Ready
          </Pill>
        ) : row.skipped ? (
          <span className="dim small">Presets that need it wait</span>
        ) : row.status === 'install' ? (
          <FitBadge fit={row.fit} on={row.model?.runs_on} />
        ) : null}
      </span>
    </li>
  )
}
