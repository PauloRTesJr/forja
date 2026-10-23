import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useEffect, useRef, useState, type CSSProperties } from 'react'
import { CheckList, FitBadge, LicenseLine, TaskProgress, taskFor, useChecks, useInstalls } from '../components/Install'
import { PauseMoon, TaskSigil } from '../components/icons'
import { ScreenHead, SectionHead } from '../components/ScreenHead'
import { Setup } from '../components/Setup'
import { Catalog, ConnectHub } from '../components/Hub'
import {
  api,
  fetchModels,
  installModel,
  pinModel,
  removeHubModel,
  removeModel,
  taskDone,
  type Model,
  type ProviderRow,
  type Setup as SetupView,
} from '../lib/api'
import { keys } from '../lib/events'
import { money, stageLabel, taskNoun } from '../lib/format'
import { useToast } from '../lib/toast'

const UNUSABLE = 'unusable'
const ORDER = ['background-removal', 'shape', 'texture']

function groupsOf(models: Model[]): [string, Model[]][] {
  const by = new Map<string, Model[]>()
  for (const m of models) {
    const use = m.tasks[0] ?? UNUSABLE
    by.set(use, [...(by.get(use) ?? []), m])
  }
  const rank = (use: string): number => (ORDER.includes(use) ? ORDER.indexOf(use) : ORDER.length)
  return [...by.entries()].sort(([a], [b]) => rank(a) - rank(b) || a.localeCompare(b))
}

export function Models() {
  const toast = useToast()
  const models = useQuery({ queryKey: keys.models, queryFn: fetchModels })
  const setup = useQuery({ queryKey: keys.setup, queryFn: () => api<SetupView>('GET', '/v1/setup') })
  const config = useQuery({ queryKey: keys.config, queryFn: () => api<{ models?: { stubs?: boolean } }>('GET', '/v1/config/effective') })
  const [installing, setInstalling] = useState<Model>()
  const [showSetup, setShowSetup] = useState(false)
  const pause = useMutation({
    mutationFn: () => api<void>('POST', '/v1/engine/unload'),
    onSuccess: () => toast('Engine paused: all GPU memory freed'),
    onError: (e: Error) => toast(e.message, true),
  })
  const providers = useQuery({ queryKey: keys.providers, queryFn: () => api<ProviderRow[]>('GET', '/v1/providers') })
  const [connecting, setConnecting] = useState(false)
  const [browsing, setBrowsing] = useState(false)
  const hub = providers.data?.find((p) => p.kind === 'api')
  const stubs = config.data?.models?.stubs ?? false
  const visible = models.data?.filter((m) => stubs || !m.stub) ?? []

  return (
    <div className="screen">
      <ScreenHead eyebrow={['Arcanum IV', 'The Seers']} title="Models" sub="What each stage runs, where, and under which license.">
        <button className="btn" onClick={() => pause.mutate()} disabled={pause.isPending}>
          <PauseMoon />
          Pause engine
        </button>
      </ScreenHead>

      {(showSetup || (setup.data && !setup.data.ready)) && <Setup onDone={() => setShowSetup(false)} />}
      {setup.data?.ready && !showSetup && (
        <div className="row ready-line enter">
          <span className="status ready">
            <i aria-hidden />
            Every preset has its models
          </span>
          <button className="btn ghost" onClick={() => setShowSetup(true)}>
            Test the pipeline
          </button>
        </div>
      )}

      {models.isPending && <div className="skeleton" style={{ height: 160, marginTop: 24 }} />}
      {models.error && <div className="error">{(models.error as Error).message}</div>}
      {groupsOf(visible).map(([use, list]) => (
        <div className="section" key={use}>
          <SectionHead>{use === UNUSABLE ? 'Cannot be used' : stageLabel(use)}</SectionHead>
          <div className="model-grid">
            {list.map((m, i) => (
              <ModelCard key={`${m.provider}|${m.id}`} model={m} i={i} onInstall={() => setInstalling(m)} />
            ))}
          </div>
        </div>
      ))}

      {hub ? (
        <div className="hub-cta">
          <div>
            <strong>More hosted models on {hub.label}</strong>
            <span>Search {hub.label}'s image-to-3D catalog and add the models you want to pick from.</span>
          </div>
          <button className="btn" onClick={() => setBrowsing(true)}>
            Add models…
          </button>
        </div>
      ) : (
        providers.data && (
          <div className="hub-cta">
            <div>
              <strong>Hosted models</strong>
              <span>
                Connect fal.ai for Hunyuan3D 3.1 Pro, TRELLIS 2, Tripo, Meshy, Rodin and more with one API key. Paid per
                generation, used only when you pick them.
              </span>
            </div>
            <button className="btn primary" onClick={() => setConnecting(true)}>
              Connect fal.ai
            </button>
          </div>
        )
      )}
      {installing && <InstallDialog model={installing} onClose={() => setInstalling(undefined)} />}
      {connecting && <ConnectHub onClose={() => setConnecting(false)} />}
      {browsing && hub && <Catalog provider={hub.id} label={hub.label} onClose={() => setBrowsing(false)} />}
    </div>
  )
}

function ModelCard({ model: m, i, onInstall }: { model: Model; i: number; onInstall: () => void }) {
  const toast = useToast()
  const qc = useQueryClient()
  const installs = useInstalls()
  const task = taskFor(installs.data, 'install', m.id) ?? taskFor(installs.data, 'remove', m.id)
  const busy = task !== undefined && !taskDone(task)
  const [confirm, setConfirm] = useState(false)
  const installed = m.status === 'ready' || m.status === 'loaded'
  const hosted = m.fit === 'hosted'
  // A hosted model Forja cannot call has no stage; its description says why.
  const use = m.tasks[0] as string | undefined
  const usedFor = use ? m.uses.find((u) => u.use === use) : undefined
  const runnable = m.fit === 'fits' || m.fit === 'offload'

  const pick = useMutation({
    mutationFn: (model: string) => pinModel(use!, model, model ? m.provider : undefined),
    onSuccess: () => void qc.invalidateQueries({ queryKey: keys.models }),
    onError: (e: Error) => toast(e.message, true),
  })
  const remove = useMutation({
    mutationFn: () => (hosted ? removeHubModel(m.provider, m.id) : removeModel(m.id).then(() => undefined)),
    onError: (e: Error) => toast(e.message, true),
  })
  const repair = useMutation({ mutationFn: () => installModel(m.id, false), onError: (e: Error) => toast(e.message, true) })

  return (
    <article className={`model-card enter ${installed ? 'installed' : ''}`} style={{ '--i': i } as CSSProperties}>
      <div className="model-cell">
        <span className={`sigil-badge ${m.status}`}>
          <TaskSigil task={use ?? 'shape'} />
        </span>
        <div>
          <strong>{m.name}</strong>
          <div className="muted mono">{m.id}</div>
        </div>
        <span className={`status ${m.status}`}>
          <i aria-hidden />
          {m.status === 'loaded'
            ? 'in VRAM'
            : hosted
              ? use
                ? `on ${m.provider_label}`
                : 'cannot be used'
              : installed
                ? `ready on ${m.provider === 'local' ? m.device : m.provider_label}`
                : 'not installed'}
        </span>
      </div>
      {m.description && <p className="model-desc">{m.description}</p>}
      <div className="chips">
        {m.recommended && <span className="chip recommended">Recommended</span>}
        {hosted ? <span className="chip paid">{money(m.price)}</span> : <FitBadge fit={m.fit} />}
        {m.disk_gb ? <span className="chip">{m.disk_gb.toFixed(1)} GB</span> : null}
        {hosted && m.tasks.length > 1 && <span className="chip">Shape and texture in one pass</span>}
        {usedFor && use && <span className="chip used">{usedFor.pinned ? `Chosen for ${taskNoun(use)}` : `In use for ${taskNoun(use)}`}</span>}
      </div>
      <LicenseLine license={m.license} />
      {hosted ? (
        <div className="row model-actions">
          {use && !usedFor && (
            <button className="btn" onClick={() => pick.mutate(m.id)} disabled={pick.isPending}>
              Use for {taskNoun(use)}
            </button>
          )}
          {usedFor?.pinned && (
            <button className="btn ghost" onClick={() => pick.mutate('')} disabled={pick.isPending}>
              Pick automatically
            </button>
          )}
          <button
            className={`btn ghost ${confirm ? 'danger' : ''}`}
            onClick={() => (confirm ? remove.mutate() : setConfirm(true))}
            onBlur={() => setConfirm(false)}
            disabled={remove.isPending}
          >
            {confirm ? 'Remove from Forja?' : 'Remove'}
          </button>
        </div>
      ) : busy ? (
        <TaskProgress task={task} />
      ) : (
        <div className="row model-actions">
          {!installed && runnable && !m.stub && (
            <button className="btn primary" onClick={onInstall}>
              Install…
            </button>
          )}
          {!installed && !runnable && <span className="muted">Runs only on another computer or a cloud GPU.</span>}
          {installed && use && !usedFor && (
            <button className="btn" onClick={() => pick.mutate(m.id)} disabled={pick.isPending}>
              Use for {taskNoun(use)}
            </button>
          )}
          {installed && usedFor?.pinned && (
            <button className="btn ghost" onClick={() => pick.mutate('')} disabled={pick.isPending}>
              Pick automatically
            </button>
          )}
          {installed && !m.stub && (
            <>
              <button className="btn ghost" onClick={() => repair.mutate()} disabled={repair.isPending}>
                Repair
              </button>
              <button
                className={`btn ghost ${confirm ? 'danger' : ''}`}
                onClick={() => (confirm ? remove.mutate() : setConfirm(true))}
                onBlur={() => setConfirm(false)}
                disabled={remove.isPending}
              >
                {confirm ? `Remove ${m.disk_gb ? `${m.disk_gb.toFixed(1)} GB` : 'it'}?` : 'Remove'}
              </button>
            </>
          )}
        </div>
      )}
      {task && task.status === 'failed' && !busy && <TaskProgress task={task} />}
    </article>
  )
}

/** Installs one model: what this computer needs for it, its license, then progress. */
function InstallDialog({ model: m, onClose }: { model: Model; onClose: () => void }) {
  const toast = useToast()
  const ref = useRef<HTMLDivElement>(null)
  const checks = useChecks([m.id])
  const installs = useInstalls()
  const [accepted, setAccepted] = useState(!m.needs_acceptance || !!m.accepted_at)
  const task = taskFor(installs.data, 'install', m.id)
  const install = useMutation({
    mutationFn: () => installModel(m.id, m.needs_acceptance && accepted),
    onError: (e: Error) => toast(e.message, true),
  })

  useEffect(() => {
    ref.current?.focus()
    const onKey = (e: KeyboardEvent): void => {
      if (e.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose])

  const started = install.isSuccess || (task !== undefined && !taskDone(task))
  return (
    <div className="dialog-backdrop" onClick={onClose}>
      <div className="dialog" role="dialog" aria-modal aria-label={`Install ${m.name}`} tabIndex={-1} ref={ref} onClick={(e) => e.stopPropagation()}>
        <h2>Install {m.name}</h2>
        <p className="muted">
          {m.description} About {m.disk_gb?.toFixed(1) ?? '?'} GB on disk. <FitBadge fit={m.fit} />
        </p>
        {!started && (
          <>
            {checks.isPending && <div className="skeleton" style={{ height: 80 }} />}
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
        {task && (started || task.status === 'succeeded') && <TaskProgress task={task} />}
        <div className="row dialog-actions">
          {!started && task?.status !== 'succeeded' && (
            <button className="btn primary" onClick={() => install.mutate()} disabled={!accepted || !checks.data?.ok || install.isPending}>
              Install
            </button>
          )}
          <button className="btn ghost" onClick={onClose}>
            {task?.status === 'succeeded' ? 'Done' : 'Close'}
          </button>
        </div>
        {!started && checks.data && !checks.data.ok && <p className="muted small">Install is available once every check passes.</p>}
      </div>
    </div>
  )
}
