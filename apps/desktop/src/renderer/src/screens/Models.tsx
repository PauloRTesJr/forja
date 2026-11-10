import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useState, type CSSProperties } from 'react'
import { Benchmark } from '../components/Benchmark'
import { ErrorBlock, Pill } from '../components/Controls'
import { Dialog, DialogActions } from '../components/Dialog'
import { Empty } from '../components/Empty'
import { InstallDialog, TaskProgress, taskFor, useChecks, useInstalls } from '../components/Install'
import { AlertIcon, ChevronIcon, ModelsIcon } from '../components/icons'
import { MenuItem, MoreMenu } from '../components/Menu'
import { ScreenHead } from '../components/ScreenHead'
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
  type InstallTask,
  type Job,
  type Model,
  type ProviderRow,
  type Setup as SetupView,
} from '../lib/api'
import { keys } from '../lib/events'
import { stageLabel, taskNoun } from '../lib/format'
import { canRetry, taskPhase, type TaskPhase } from '../lib/installTasks'
import { isActive } from '../lib/jobs'
import {
  canManage,
  cost,
  gb,
  isHosted,
  isInstalled,
  licenseFact,
  limits,
  modelState,
  orderModels,
  removalEffect,
  removalPlan,
  runsHere,
  runsOn,
  stageOf,
  stageRule,
  type Fact,
  type ModelState,
  type StageRule,
} from '../lib/models'
import { useToast } from '../lib/toast'

const UNUSABLE = 'unusable'
const ORDER = ['background-removal', 'shape', 'texture', 'rig']
const rowKey = (m: Model): string => `${m.provider}|${m.id}`

function groupsOf(models: Model[]): [string, Model[]][] {
  const by = new Map<string, Model[]>()
  for (const m of models) {
    const use = stageOf(m) ?? UNUSABLE
    by.set(use, [...(by.get(use) ?? []), m])
  }
  const rank = (use: string): number => (ORDER.includes(use) ? ORDER.indexOf(use) : ORDER.length)
  return [...by.entries()].sort(([a], [b]) => rank(a) - rank(b) || a.localeCompare(b)).map(([use, list]) => [use, orderModels(list)])
}

export function Models() {
  const toast = useToast()
  const qc = useQueryClient()
  const models = useQuery({ queryKey: keys.models, queryFn: fetchModels })
  const setup = useQuery({ queryKey: keys.setup, queryFn: () => api<SetupView>('GET', '/v1/setup') })
  const config = useQuery({ queryKey: keys.config, queryFn: () => api<{ models?: { stubs?: boolean } }>('GET', '/v1/config/effective') })
  const providers = useQuery({ queryKey: keys.providers, queryFn: () => api<ProviderRow[]>('GET', '/v1/providers') })
  const stubs = config.data?.models?.stubs ?? false
  // Until the settings say whether stand-ins count, none are listed: they would pop in or out.
  const visible = config.data ? (models.data?.filter((m) => stubs || !m.stub) ?? []) : []
  const loading = models.isPending || config.isPending

  // The list of every provider leaves out one that cannot answer, so an engine that will not
  // start gives an empty list and no reason. Asking this computer alone gives the reason.
  const noLocal = models.isSuccess && !models.data.some((m) => m.provider === 'local')
  const engine = useQuery({
    queryKey: [...keys.models, 'this-computer'],
    queryFn: () => api<Model[]>('GET', '/v1/models?fresh=1', undefined, { timeoutMs: 10 * 60_000 }),
    enabled: noLocal,
    retry: false,
  })
  const retryEngine = (): void => {
    void engine.refetch().then((r) => {
      if (r.isSuccess) void qc.invalidateQueries({ queryKey: keys.models })
    })
    void qc.invalidateQueries({ queryKey: keys.setup })
  }

  const [installing, setInstalling] = useState<string>()
  const [removing, setRemoving] = useState<string>()
  const [open, setOpen] = useState<string>()
  const [testing, setTesting] = useState(false)
  const [later, setLater] = useState(false)
  const [connecting, setConnecting] = useState(false)
  const [browsing, setBrowsing] = useState(false)
  const find = (key?: string): Model | undefined => (key ? models.data?.find((m) => rowKey(m) === key) : undefined)
  const hub = providers.data?.find((p) => p.kind === 'api')

  const free = useMutation({
    mutationFn: () => api<void>('POST', '/v1/engine/unload'),
    onSuccess: () => {
      const busy = qc.getQueryData<Job[]>(keys.jobs)?.some(isActive)
      toast(
        busy
          ? 'GPU memory is freed when the running step ends. The job loads what its next step needs.'
          : 'GPU memory freed. Models load again when the next job needs them.',
      )
    },
    onError: (e: Error) => toast(e.message, true),
  })

  const unready = setup.data && !setup.data.ready
  const missing = setup.data?.stages.filter((s) => s.status !== 'ready').length ?? 0
  return (
    <div className="screen capped">
      <ScreenHead title="Models" sub="Which model runs each step, and where.">
        <button
          className="btn"
          onClick={() => free.mutate()}
          disabled={free.isPending}
          title="Unloads every model from the GPU, for a game or another program. Nothing is paused: the next job loads its models again."
        >
          Free GPU memory
        </button>
      </ScreenHead>

      {engine.error && (
        <ErrorBlock role="alert">
          <strong className="block">The engine on this computer did not start, so its models cannot be listed or installed.</strong>
          <span className="block selectable">{(engine.error as Error).message}</span>
          <button className="btn sm" onClick={retryEngine} disabled={engine.isFetching}>
            {engine.isFetching ? 'Starting…' : 'Try again'}
          </button>
        </ErrorBlock>
      )}

      {(testing || (unready && !later)) && (
        <Setup
          onDone={() => {
            setTesting(false)
            setLater(true)
          }}
        />
      )}
      {unready && later && !testing && (
        <div className="notice enter">
          <div>
            <strong>Setup is not finished</strong>
            <span>
              {missing === 1 ? 'One step has no model yet' : `${missing} steps have no model yet`}, so some presets cannot run.
            </span>
          </div>
          <button className="btn" onClick={() => setLater(false)}>
            Continue setup
          </button>
        </div>
      )}
      {setup.data?.ready && !testing && (
        <p className="models-ready enter">
          <span className="dot ok" aria-hidden />
          Every step has a model: all presets can run.
          <button className="text-btn" onClick={() => setTesting(true)}>
            Test with a sample image
          </button>
        </p>
      )}

      {loading && !models.error && (
        <div className="section" aria-busy>
          <div className="skeleton" style={{ height: 16, width: 120, marginBottom: 12 }} />
          <div className="skeleton" style={{ height: 196 }} />
        </div>
      )}
      {models.error && (
        <ErrorBlock>
          <strong className="block">The models could not be listed.</strong>
          <span className="block">{(models.error as Error).message}</span>
          <button className="btn sm" onClick={() => void models.refetch()} disabled={models.isFetching}>
            Try again
          </button>
        </ErrorBlock>
      )}
      {!loading && models.isSuccess && visible.length === 0 && !engine.error && !engine.isFetching && (
        <Empty icon={ModelsIcon} title="No models to show">
          Models appear here once the engine on this computer has started, or when you add another computer or a hosted service.
        </Empty>
      )}

      {groupsOf(visible).map(([use, list]) => (
        <StageSection
          key={use}
          use={use}
          models={list}
          all={models.data ?? []}
          open={open}
          onToggle={(key) => setOpen(open === key ? undefined : key)}
          onInstall={setInstalling}
          onRemove={setRemoving}
        />
      ))}

      {providers.data && (
        <div className="notice hub-cta">
          <div>
            <strong>{hub ? `More hosted models on ${hub.label}` : 'Hosted models'}</strong>
            <span>
              {hub
                ? `Search ${hub.label}'s image-to-3D catalog and add the models you want to choose from.`
                : 'Connect fal.ai to use Hunyuan3D 3.1 Pro, TRELLIS 2, Tripo, Meshy, Rodin and more with one API key. Paid per image, and used only when you choose one for a step.'}
            </span>
          </div>
          <button className="btn" onClick={() => (hub ? setBrowsing(true) : setConnecting(true))}>
            {hub ? 'Add models…' : 'Connect fal.ai…'}
          </button>
        </div>
      )}
      <Benchmark />

      {find(installing) && <InstallDialog model={find(installing)!} onClose={() => setInstalling(undefined)} />}
      {find(removing) && (
        <RemoveDialog
          model={find(removing)!}
          models={models.data ?? []}
          stubs={stubs}
          onClose={() => {
            const name = find(removing)?.name
            setRemoving(undefined)
            // The menu item that opened the dialog is gone with its menu: focus goes to the menu's button.
            requestAnimationFrame(() => document.querySelector<HTMLElement>(`button[aria-label="More actions for ${name}"]`)?.focus())
          }}
        />
      )}
      {connecting && <ConnectHub onClose={() => setConnecting(false)} />}
      {browsing && hub && <Catalog provider={hub.id} label={hub.label} onClose={() => setBrowsing(false)} />}
    </div>
  )
}

/** One step of the pipeline: which model it uses and why, then every model that can do it. */
function StageSection({
  use,
  models,
  all,
  open,
  onToggle,
  onInstall,
  onRemove,
}: {
  use: string
  models: Model[]
  all: Model[]
  open?: string
  onToggle: (key: string) => void
  onInstall: (key: string) => void
  onRemove: (key: string) => void
}) {
  const usable = use !== UNUSABLE
  return (
    <section className="section" aria-label={usable ? stageLabel(use) : 'Cannot be used'}>
      <div className="section-head stage-head">
        <h2>{usable ? stageLabel(use) : 'Cannot be used'}</h2>
        {usable && <Rule use={use} rule={stageRule(all, use)} />}
      </div>
      <div className="model-list group">
        <div className="model-cols" aria-hidden>
          <span>Model</span>
          <span>Runs on</span>
          <span>Size or price</span>
          <span className="col-license">License</span>
          <span />
        </div>
        <ul>
          {models.map((m, i) => (
            <ModelRow
              key={rowKey(m)}
              model={m}
              i={i}
              open={open === rowKey(m)}
              onToggle={() => onToggle(rowKey(m))}
              onInstall={() => onInstall(rowKey(m))}
              onRemove={() => onRemove(rowKey(m))}
            />
          ))}
        </ul>
      </div>
    </section>
  )
}

/** Which model the step uses, said once for the step: picked automatically, or chosen by the user (who can hand the choice back). */
function Rule({ use, rule }: { use: string; rule?: StageRule }) {
  const toast = useToast()
  const qc = useQueryClient()
  const auto = useMutation({
    mutationFn: () => pinModel(use, ''),
    onSuccess: () => void qc.invalidateQueries({ queryKey: keys.models }),
    onError: (e: Error) => toast(e.message, true),
  })
  const back = (
    <button className="text-btn" onClick={() => auto.mutate()} disabled={auto.isPending}>
      Back to automatic
    </button>
  )
  if (!rule) return <p className="stage-rule dim">No model yet</p>
  if (rule.missing) {
    return (
      <p className="stage-rule warn-text" role="status">
        <AlertIcon size={14} />
        Set to {rule.model.name}, which is not installed · {back}
      </p>
    )
  }
  if (rule.pinned) {
    return (
      <p className="stage-rule">
        Chosen by you: {rule.model.name} · {back}
      </p>
    )
  }
  return (
    <p className="stage-rule" title="The best model that is ready is used. Installing a better one switches to it.">
      Automatic: {rule.model.name}
    </p>
  )
}

/**
 * One model as a row: who it is, the facts that tell it apart from the others (where it runs,
 * what it takes, what its license allows), then its state and the one thing to do with it now.
 * Rare actions wait in a menu; the rest of what there is to know opens under the row.
 */
function ModelRow({
  model: m,
  i,
  open,
  onToggle,
  onInstall,
  onRemove,
}: {
  model: Model
  i: number
  open: boolean
  onToggle: () => void
  onInstall: () => void
  onRemove: () => void
}) {
  const toast = useToast()
  const qc = useQueryClient()
  const installs = useInstalls()
  const install = taskFor(installs.data, 'install', m.id, m.provider)
  const removal = taskFor(installs.data, 'remove', m.id, m.provider)
  const task = [install, removal].find((t) => t && !taskDone(t)) ?? install
  const state = modelState(m, task)
  const manage = canManage(m)
  const hosted = isHosted(m)
  const use = stageOf(m)
  const phase = taskPhase(task)
  const stopped = manage && !isInstalled(m) && task?.kind === 'install' && canRetry(phase)
  const details = `model-${m.provider}-${m.id}`.replace(/[^\w-]/g, '-')

  const pick = useMutation({
    mutationFn: () => pinModel(use!, m.id, m.provider),
    onSuccess: () => void qc.invalidateQueries({ queryKey: keys.models }),
    onError: (e: Error) => toast(e.message, true),
  })
  const repair = useMutation({ mutationFn: () => installModel(m.id, false, m.provider), onError: (e: Error) => toast(e.message, true) })
  const license = licenseFact(m.license)

  return (
    <li className={`model-row enter ${state === 'in-use' ? 'in-use' : ''} ${state === 'unavailable' ? 'out' : ''}`} style={{ '--i': i } as CSSProperties}>
      <div className="model-main">
        <button className="model-toggle" aria-expanded={open} aria-controls={details} onClick={onToggle}>
          <ChevronIcon size={14} />
          <strong>{m.name}</strong>
        </button>
        {(m.recommended || m.stub || (hosted && m.tasks.length > 1)) && (
          <span className="pills">
            {m.recommended && <Pill>Recommended</Pill>}
            {hosted && m.tasks.length > 1 && <Pill>Shape and texture</Pill>}
            {m.stub && <Pill>Test stand-in</Pill>}
          </span>
        )}
        {m.description && !open && (
          <p className="model-desc truncate" title={m.description}>
            {m.description}
          </p>
        )}
      </div>
      <FactCell label="Runs on" fact={runsOn(m)} />
      <FactCell label="Size or price" fact={cost(m)} />
      <FactCell label="License" fact={license} href={m.license.url} className="col-license" />
      <div className="model-act">
        <StateAction state={state} phase={phase} model={m} use={use} busy={pick.isPending} onUse={() => pick.mutate()} onInstall={onInstall} />
        <span className="model-more">
          {(manage || hosted) && (isInstalled(m) || hosted || stopped) && state !== 'installing' && state !== 'removing' && (
            <MoreMenu label={`More actions for ${m.name}`}>
              {(menu) => (
                <>
                  {manage && isInstalled(m) && (
                    <MenuItem menu={menu} help="Checks its files and fetches what is missing" onClick={() => repair.mutate()} disabled={repair.isPending}>
                      Repair
                    </MenuItem>
                  )}
                  <MenuItem menu={menu} danger onClick={onRemove}>
                    {hosted ? 'Remove from Forja…' : stopped ? 'Delete downloaded files…' : 'Remove…'}
                  </MenuItem>
                </>
              )}
            </MoreMenu>
          )}
        </span>
      </div>
      {open && (
        <div className="model-details" id={details}>
          {m.description && <p>{m.description}</p>}
          {limits(m.license) && (
            <p className="caution">
              <AlertIcon size={14} />
              License limits: {limits(m.license)}
            </p>
          )}
          <dl className="kv">
            <div className="kv-row col-license-alt">
              <dt>License</dt>
              <dd>
                {m.license.url ? (
                  <a href={m.license.url} target="_blank" rel="noreferrer">
                    {license.value}
                  </a>
                ) : (
                  license.value
                )}{' '}
                · {license.detail}
              </dd>
            </div>
            {!hosted && !m.stub && m.vram_gb ? (
              <div className="kv-row">
                <dt>GPU memory</dt>
                <dd>Up to {gb(m.vram_gb)} at default settings</dd>
              </div>
            ) : null}
            {m.fit === 'unsupported' && !hosted && <WhyNot id={m.id} />}
            <div className="kv-row">
              <dt>Id, for the command line</dt>
              <dd className="mono">{m.id}</dd>
            </div>
          </dl>
        </div>
      )}
      {task && (!taskDone(task) || stopped || (task.kind === 'remove' && task.status === 'failed')) && (
        <div className="model-task">
          <TaskProgress task={task} onRetry={task.kind === 'install' && state !== 'failed' ? onInstall : undefined} />
        </div>
      )}
    </li>
  )
}

/** A row's state and its one action. Install and remove are offered only for models this computer manages. */
function StateAction({
  state,
  phase,
  model: m,
  use,
  busy,
  onUse,
  onInstall,
}: {
  state: ModelState
  phase: TaskPhase
  model: Model
  use?: string
  busy: boolean
  onUse: () => void
  onInstall: () => void
}) {
  const installable = canManage(m) && runsHere(m)
  const pinned = use !== undefined && m.uses.some((u) => u.use === use && u.pinned)
  switch (state) {
    case 'installing':
      return phase === 'queued' ? (
        <Pill>Waiting</Pill>
      ) : (
        <Pill tone="accent" live>
          {phase === 'cancelling' ? 'Cancelling' : 'Installing'}
        </Pill>
      )
    case 'removing':
      return (
        <Pill tone="accent" live>
          Removing
        </Pill>
      )
    case 'in-use':
      return (
        <span className="state-stack">
          <Pill tone="accent">In use</Pill>
          <small>{pinned ? 'Chosen by you' : 'Picked automatically'}</small>
        </span>
      )
    case 'chosen-missing':
      return (
        <>
          <span className="state-stack">
            <Pill tone="warn">Not installed</Pill>
            <small>Chosen by you</small>
          </span>
          {installable && (
            <button className="btn sm" onClick={onInstall} aria-label={`Install ${m.name}`}>
              Install…
            </button>
          )}
        </>
      )
    case 'ready':
      return use ? (
        <button className="btn sm" onClick={onUse} disabled={busy} aria-label={`Use ${m.name} for ${taskNoun(use)}`}>
          Use
        </button>
      ) : null
    case 'failed':
      return (
        <>
          <Pill tone="bad">{phase === 'interrupted' ? 'Interrupted' : 'Install stopped'}</Pill>
          <button className="btn sm" onClick={onInstall} aria-label={`Try installing ${m.name} again`}>
            Try again…
          </button>
        </>
      )
    case 'not-installed':
      return (
        <button className="btn sm" onClick={onInstall} aria-label={`Install ${m.name}`}>
          Install…
        </button>
      )
    case 'elsewhere':
      return <span className="small dim">Not installed there</span>
    default:
      return null
  }
}

/** Why this computer cannot run the model, in the engine's own words. */
function WhyNot({ id }: { id: string }) {
  const checks = useChecks([id])
  const why = checks.data?.checks.find((c) => !c.ok && c.id.startsWith('runs-here:'))
  if (!why) return null
  return (
    <div className="kv-row">
      <dt>Why not here</dt>
      <dd>{why.why}</dd>
    </div>
  )
}

function FactCell({ label, fact, href, className }: { label: string; fact: Fact; href?: string; className?: string }) {
  return (
    <div className={`fact ${fact.tone ?? ''} ${className ?? ''}`}>
      <span className="fact-label">{label}</span>
      <span className="fact-value">
        {href ? (
          <a href={href} target="_blank" rel="noreferrer">
            {fact.value}
          </a>
        ) : (
          fact.value
        )}
      </span>
      {fact.detail && <span className="fact-detail">{fact.detail}</span>}
    </div>
  )
}

/** Asks before a model's files are deleted (or a hosted model is taken off the list), and says what the step it serves will do without it. */
function RemoveDialog({ model: m, models, stubs, onClose }: { model: Model; models: Model[]; stubs: boolean; onClose: () => void }) {
  const toast = useToast()
  const qc = useQueryClient()
  const hosted = isHosted(m)
  const use = stageOf(m)
  const chosen = use !== undefined && m.uses.some((u) => u.use === use && u.pinned)
  const effect = removalEffect(m, models, stubs)
  const remove = useMutation({
    mutationFn: async (): Promise<InstallTask | void> => {
      const plan = removalPlan(m)
      const task = plan.hosted ? await removeHubModel(m.provider, m.id) : await removeModel(m.id)
      if (plan.unpin) await pinModel(plan.unpin, '').catch(() => undefined)
      return task
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: keys.models })
      if (hosted) toast(`${m.name} removed from Forja`)
      onClose()
    },
    onError: (e: Error) => toast(e.message, true),
  })
  return (
    <Dialog title={hosted ? `Remove ${m.name} from Forja?` : isInstalled(m) ? `Remove ${m.name}?` : `Delete the files of ${m.name}?`} size="narrow" alert onClose={onClose}>
      <p>
        {hosted
          ? `It leaves this list and is no longer offered for a step. Nothing changes at ${m.provider_label}, and you can add it again from the catalog.`
          : isInstalled(m)
            ? `Its files are deleted${m.disk_gb ? `, which frees about ${gb(m.disk_gb)}` : ''}. You can install it again later.`
            : 'What the unfinished install downloaded is deleted. Installing it later starts from the beginning.'}
      </p>
      {effect && <p className="muted">{chosen ? `${stageLabel(use)} goes back to automatic. ` : ''}{effect}</p>}
      <DialogActions>
        <button className="btn ghost" onClick={onClose} autoFocus>
          Cancel
        </button>
        <button className="btn danger" onClick={() => remove.mutate()} disabled={remove.isPending}>
          {remove.isPending ? 'Removing…' : hosted ? 'Remove' : isInstalled(m) ? 'Remove' : 'Delete files'}
        </button>
      </DialogActions>
    </Dialog>
  )
}
