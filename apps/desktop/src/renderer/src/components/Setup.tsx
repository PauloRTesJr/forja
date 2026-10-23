import { useMutation, useQuery } from '@tanstack/react-query'
import { useState } from 'react'
import { api, fetchModels, installModel, taskDone, type Job, type Model, type Setup as SetupView } from '../lib/api'
import { keys } from '../lib/events'
import { bytes, listOf, taskNoun } from '../lib/format'
import { isActive, summarize } from '../lib/jobs'
import { transition, useNavigate } from '../lib/nav'
import { useToast } from '../lib/toast'
import { CheckList, LicenseLine, TaskProgress, taskFor, useChecks, useInstalls } from './Install'
import { Sparkle } from './icons'
import { StageTrack } from './StageTrack'

function gpuOf(setup: SetupView): string {
  const gpu = setup.system?.devices?.find((d) => d.backend !== 'cpu')
  return gpu ? `${gpu.name} · ${bytes(gpu.vram_bytes)}` : 'No GPU found'
}

/**
 * The guided path from a fresh install to a first asset: what this computer needs, the
 * licenses to accept, the installs, then a test run of the whole pipeline.
 */
export function Setup({ onDone }: { onDone?: () => void }) {
  const toast = useToast()
  const navigate = useNavigate()
  const setup = useQuery({ queryKey: keys.setup, queryFn: () => api<SetupView>('GET', '/v1/setup') })
  const models = useQuery({ queryKey: keys.models, queryFn: fetchModels })
  const installs = useInstalls()
  const jobs = useQuery({ queryKey: keys.jobs, queryFn: () => api<Job[]>('GET', '/v1/jobs?limit=100') })
  const wanted = setup.data?.install ?? []
  const checks = useChecks(wanted, setup.data !== undefined && !setup.data.ready)
  const [accepted, setAccepted] = useState<Record<string, boolean>>({})
  const [testJob, setTestJob] = useState<string>()

  const byId = new Map(models.data?.map((m) => [m.id, m]))
  const licenses = wanted.map((id) => byId.get(id)).filter((m): m is Model => !!m && m.needs_acceptance && !m.accepted_at)
  const tasks = wanted.map((id) => taskFor(installs.data, 'install', id)).filter((t) => t !== undefined)
  const installing = tasks.some((t) => !taskDone(t))
  const checksOk = checks.data?.ok ?? false
  const licensesOk = licenses.every((m) => accepted[m.id])

  const install = useMutation({
    mutationFn: async () => {
      for (const id of wanted) await installModel(id, accepted[id] ?? false)
    },
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
      <div className="setup enter">
        <div className="setup-head">
          <strong>Looking at this computer…</strong>
          <span className="muted">Starting the engine to see the GPU and the installed models.</span>
        </div>
        <div className="skeleton" style={{ height: 120 }} />
      </div>
    )
  }
  if (setup.error) return <div className="error">{(setup.error as Error).message}</div>
  const s = setup.data!
  const remote = s.stages.filter((st) => st.status === 'remote')

  if (s.ready && !testJob) {
    return (
      <div className="setup done enter">
        <div className="setup-head">
          <strong>Every preset has its models</strong>
          <span className="muted">Run the sample once to check the whole pipeline on this computer.</span>
        </div>
        <div className="row">
          <button className="btn primary" onClick={() => test.mutate()} disabled={test.isPending}>
            Generate the sample
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

  return (
    <div className="setup enter">
      <div className="setup-head">
        <span className="eyebrow">
          <Sparkle /> Set up this computer
        </span>
        <strong>{gpuOf(s)}</strong>
        <span className="muted">
          {s.install.length > 0
            ? `Recommended: run ${remote.length ? 'what fits' : 'everything'} here. ${s.install.length} model${s.install.length > 1 ? 's' : ''}, about ${Math.round(s.disk_gb)} GB on disk.`
            : remote.length
              ? 'Nothing that is missing can run on this computer.'
              : 'Everything is installed.'}
        </span>
      </div>

      {remote.length > 0 && (
        <div className="notice">
          <div>
            <strong>No {listOf(remote.map((r) => taskNoun(r.use)))} model runs here</strong>
            <span>This computer lacks the GPU those models need. Use another computer that runs Forja, or a cloud GPU.</span>
          </div>
          <button className="btn" onClick={() => transition(() => navigate({ screen: 'settings' }))}>
            Where models run
          </button>
        </div>
      )}

      {s.install.length > 0 && (
        <ol className="setup-steps">
          <li className={checksOk ? 'complete' : ''}>
            <div className="setup-step-head">
              <h3>What this computer needs</h3>
              <button className="btn ghost" onClick={() => void checks.refetch()} disabled={checks.isFetching}>
                {checks.isFetching ? 'Checking…' : 'Check again'}
              </button>
            </div>
            {checks.isPending && <div className="skeleton" style={{ height: 90 }} />}
            {checks.error && <div className="error">{(checks.error as Error).message}</div>}
            {checks.data && <CheckList checks={checks.data.checks} />}
            <p className="muted small">The CUDA toolkit and C++ tools are only used while installing, to build GPU code once.</p>
          </li>

          {licenses.length > 0 && (
            <li className={licensesOk ? 'complete' : ''}>
              <div className="setup-step-head">
                <h3>Licenses</h3>
              </div>
              {licenses.map((m) => (
                <label key={m.id} className="accept">
                  <input
                    type="checkbox"
                    checked={accepted[m.id] ?? false}
                    onChange={(e) => setAccepted({ ...accepted, [m.id]: e.target.checked })}
                  />
                  <span>
                    I accept the license of <strong>{m.name}</strong>: <LicenseLine license={m.license} />
                  </span>
                </label>
              ))}
            </li>
          )}

          <li className={tasks.length === wanted.length && tasks.every((t) => t.status === 'succeeded') ? 'complete' : ''}>
            <div className="setup-step-head">
              <h3>Install</h3>
              {!installing && (
                <button className="btn primary" onClick={() => install.mutate()} disabled={!checksOk || !licensesOk || install.isPending}>
                  Install {listOf(wanted.map((id) => byId.get(id)?.name ?? id))}
                </button>
              )}
            </div>
            {!checksOk && !installing && <p className="muted small">Available once every check above passes.</p>}
            {tasks.map((t) => (
              <div key={t.id} className="setup-install">
                <strong>{byId.get(t.target)?.name ?? t.target}</strong>
                <TaskProgress task={t} />
              </div>
            ))}
          </li>
        </ol>
      )}

      {testJob && (
        <div className="setup-test">
          <h3>Test run</h3>
          {job ? (
            <>
              <StageTrack stages={job.stages} />
              <p className="muted">{summarize(job).headline}</p>
              {!isActive(job) && job.asset_id && (
                <div className="row">
                  <button className="btn primary" onClick={() => transition(() => navigate({ screen: 'asset', id: job.asset_id! }))}>
                    Open the sample
                  </button>
                  {onDone && (
                    <button className="btn ghost" onClick={onDone}>
                      Done
                    </button>
                  )}
                </div>
              )}
              {job.status === 'failed' && <div className="error">{job.error}</div>}
            </>
          ) : (
            <div className="skeleton" style={{ height: 60 }} />
          )}
        </div>
      )}
    </div>
  )
}
