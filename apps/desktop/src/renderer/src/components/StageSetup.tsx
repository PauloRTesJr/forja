import { useMutation, useQuery } from '@tanstack/react-query'
import { useEffect, useRef, useState } from 'react'
import { fetchModels, installModel, taskDone, type StageInstall } from '../lib/api'
import { keys } from '../lib/events'
import { useToast } from '../lib/toast'
import { ErrorBlock } from './Controls'
import { CheckList, FitBadge, LicenseLine, TaskProgress, taskFor, useChecks, useInstalls } from './Install'

/**
 * Sets up an optional step that cannot run on this computer yet, right where its switch is:
 * the same steps as the first setup (what the computer needs, the license, the install with
 * its progress), for the one model or program the step needs. Once it is installed the step
 * turns on.
 */
export function StageSetup({ install, onReady }: { install: StageInstall; onReady: () => void }) {
  const [open, setOpen] = useState(false)
  const models = useQuery({ queryKey: keys.models, queryFn: fetchModels })
  const model = install.model ? models.data?.find((m) => m.id === install.model && m.provider === 'local') : undefined
  // The program a step needs is named by its checks, so they are read before the button says it.
  const software = useChecks([], !!install.software, install.software ? [install.use] : [])
  const missing = software.data?.checks.find((c) => !c.ok)

  if (install.model && !model) return null
  const label = model
    ? `Install ${model.name}`
    : missing
      ? `Install ${missing.label}`
      : 'Set it up'
  return (
    <div className="stage-setup">
      {!open ? (
        <button className="btn sm" onClick={() => setOpen(true)}>
          {label}
          {model?.disk_gb ? <span className="muted"> · about {model.disk_gb.toFixed(1)} GB</span> : null}
        </button>
      ) : model ? (
        <ModelSetup modelId={model.id} onReady={onReady} onClose={() => setOpen(false)} />
      ) : (
        <SoftwareSetup use={install.use} onReady={onReady} onClose={() => setOpen(false)} />
      )}
    </div>
  )
}

/** Calls onReady once, when a task it watches has just succeeded. */
function useReadyWhen(succeeded: boolean, onReady: () => void) {
  const was = useRef(succeeded)
  useEffect(() => {
    if (succeeded && !was.current) onReady()
    was.current = succeeded
  }, [succeeded, onReady])
}

function ModelSetup({ modelId, onReady, onClose }: { modelId: string; onReady: () => void; onClose: () => void }) {
  const toast = useToast()
  const models = useQuery({ queryKey: keys.models, queryFn: fetchModels })
  const m = models.data?.find((x) => x.id === modelId && x.provider === 'local')
  const checks = useChecks([modelId])
  const installs = useInstalls()
  const task = taskFor(installs.data, 'install', modelId)
  const [accepted, setAccepted] = useState(false)
  const install = useMutation({
    mutationFn: () => installModel(modelId, !!m?.needs_acceptance && accepted),
    onError: (e: Error) => toast(e.message, true),
  })
  useReadyWhen(task?.status === 'succeeded', onReady)
  if (!m) return null

  const licensed = !m.needs_acceptance || !!m.accepted_at || accepted
  const started = install.isSuccess || (task !== undefined && !taskDone(task))
  return (
    <div className="stage-setup-panel enter" role="group" aria-label={`Install ${m.name}`}>
      <p className="muted">
        {m.description} About {m.disk_gb?.toFixed(1) ?? '?'} GB on disk. <FitBadge fit={m.fit} on={m.runs_on} />
      </p>
      {!started && (
        <>
          {checks.isPending && <div className="skeleton" style={{ height: 64 }} />}
          {checks.error && <ErrorBlock>{(checks.error as Error).message}</ErrorBlock>}
          {checks.data && <CheckList checks={checks.data.checks} />}
          {m.needs_acceptance && !m.accepted_at && (
            <label className="accept">
              <input type="checkbox" checked={accepted} onChange={(e) => setAccepted(e.target.checked)} />
              <span>
                I accept the license: <LicenseLine license={m.license} />
              </span>
            </label>
          )}
        </>
      )}
      {task && (started || task.status === 'failed') && <TaskProgress task={task} />}
      <div className="row">
        {!started && (
          <button className="btn primary sm" onClick={() => install.mutate()} disabled={!licensed || !checks.data?.ok || install.isPending}>
            Install {m.name}
          </button>
        )}
        <button className="btn ghost sm" onClick={onClose}>
          {started ? 'Hide' : 'Not now'}
        </button>
      </div>
      {!started && checks.data && !checks.data.ok && <p className="muted small">Install is available once every check passes.</p>}
    </div>
  )
}

function SoftwareSetup({ use, onReady, onClose }: { use: string; onReady: () => void; onClose: () => void }) {
  const checks = useChecks([], true, [use])
  const installs = useInstalls()
  // The step is ready when a fix for one of its checks has just gone through.
  const fixed = (checks.data?.checks ?? []).some((c) => taskFor(installs.data, 'fix', c.id)?.status === 'succeeded')
  useReadyWhen(fixed, onReady)
  return (
    <div className="stage-setup-panel enter" role="group" aria-label="What this step needs">
      {checks.isPending && <div className="skeleton" style={{ height: 64 }} />}
      {checks.data && <CheckList checks={checks.data.checks} />}
      <div className="row">
        <button className="btn sm" onClick={() => void checks.refetch()} disabled={checks.isFetching}>
          {checks.isFetching ? 'Checking…' : 'Check again'}
        </button>
        <button className="btn ghost sm" onClick={onClose}>
          Not now
        </button>
      </div>
    </div>
  )
}
