import { useMutation, useQuery } from '@tanstack/react-query'
import { api, fetchModels, pinModel, type Estimate, type Model, type Preset } from '../lib/api'
import { describeEstimate } from '../lib/estimate'
import { keys } from '../lib/events'
import { money, stageLabel } from '../lib/format'
import { transition, useNavigate } from '../lib/nav'
import { useToast } from '../lib/toast'
import { ErrorBlock, Hint, Pill, Switch } from './Controls'
import { FinishPhaseIcon, PaintPhaseIcon, PreparePhaseIcon, RigPhaseIcon, ShapePhaseIcon } from './icons'
import { SectionHead } from './ScreenHead'
import { StageSetup } from './StageSetup'
import { ViewsNotice } from './ViewsNotice'

interface Config {
  models?: { stages?: Record<string, string>; stubs?: boolean }
}

const ready = (m: Model): boolean => (m.status === 'ready' || m.status === 'loaded') && m.device !== null

function optionLabel(m: Model): string {
  const parts = [m.name]
  if (m.paid) parts.push(money(m.price))
  if (!ready(m)) parts.push('not installed')
  else if (m.fit === 'offload') parts.push('slower here')
  return parts.join(' · ')
}

/** Models for a stage, grouped by where they run: this computer first, then the others. */
function groupsFor(models: Model[], use: string, stubs: boolean, pinned?: string): [string, Model[]][] {
  const by = new Map<string, Model[]>()
  for (const m of models) {
    if (!m.tasks.includes(use) || m.status === 'unsupported' || (m.stub && !stubs && m.id !== pinned)) continue
    by.set(m.provider_label, [...(by.get(m.provider_label) ?? []), m])
  }
  for (const list of by.values()) list.sort((a, b) => Number(!!b.recommended) - Number(!!a.recommended) || (b.rank ?? 0) - (a.rank ?? 0))
  return [...by.entries()].sort(([a], [b]) => (a === 'This computer' ? -1 : b === 'This computer' ? 1 : a.localeCompare(b)))
}

/**
 * The model "Automatic" would pick on this computer while another one is pinned: the
 * highest-ranked one that is ready here, as the core picks it. Undefined when none is, since
 * the choice among other computers follows the routing order, which this cannot see.
 */
export function automaticPick(models: Model[], use: string, stubs: boolean): Model | undefined {
  let best: Model | undefined
  for (const m of models) {
    if (m.provider !== 'local' || !m.tasks.includes(use) || !ready(m) || (m.stub && !stubs)) continue
    const rank = m.rank ?? 0
    const top = best?.rank ?? 0
    if (!best || rank > top || (rank === top && m.id < best.id)) best = m
  }
  return best
}

/**
 * The optional steps' switches as a job's overrides. A step no provider can run here (a Blender
 * step without Blender) goes off, so a switch left on earlier never refuses the job.
 */
export function optionalOverrides(
  preset: Preset,
  isOn: (stage: string, fallback: boolean) => boolean,
): Record<string, boolean> {
  return Object.fromEntries(
    preset.stages
      .filter((s) => s.optional)
      .map((s) => [`${s.id}.enabled`, isOn(s.id, s.optional!.default) && !preset.unavailable?.[s.id]]),
  )
}

/** How long the images should take and what they should cost, as the core estimates it before queueing. */
export function EstimateNote({ estimate }: { estimate?: Estimate | null }) {
  if (estimate === null) return null
  if (!estimate) return <div className="estimate skeleton" aria-hidden />
  const text = describeEstimate(estimate)
  return (
    <div className="estimate" role="status">
      <p>
        <strong>{text.time}</strong>
        {text.cost && <span> · {text.cost}</span>}
      </p>
      {text.notes.map((n) => (
        <p key={n} className="small">
          {n}
        </p>
      ))}
      {estimate.paid && <p className="small">The reference image is sent to the paid provider; the asset comes back to this computer.</p>}
      {text.problem && <ErrorBlock>This would be refused: {text.problem}</ErrorBlock>}
    </div>
  )
}

/** The icon of the phase a stage belongs to, so a model row is told apart by more than its name. */
function StageIcon({ use }: { use: string }) {
  const Icon =
    use === 'background-removal' || use === 'framing'
      ? PreparePhaseIcon
      : use === 'texture' || use.startsWith('texture.') || use === 'bake'
        ? PaintPhaseIcon
        : use === 'rig'
          ? RigPhaseIcon
          : use === 'mesh.lods' || use === 'export'
            ? FinishPhaseIcon
            : ShapePhaseIcon
  return <Icon size={18} />
}

/**
 * How an image becomes an asset: the preset (a budget of quality), the model of each step and
 * the optional steps. A model chosen here is the same setting as "Use for this stage" on the
 * Models screen, so it holds for every later job.
 */
export function Composer({
  presets,
  preset,
  onPreset,
  isOn,
  onToggle,
}: {
  /** The presets as they would run now; with side views staged, as they would run with those. */
  presets: Preset[]
  preset: string
  onPreset: (id: string) => void
  isOn: (stage: string, fallback: boolean) => boolean
  onToggle: (stage: string, on: boolean) => void
}) {
  const toast = useToast()
  const navigate = useNavigate()
  const models = useQuery({ queryKey: keys.models, queryFn: fetchModels })
  const config = useQuery({ queryKey: keys.config, queryFn: () => api<Config>('GET', '/v1/config/effective') })
  const pin = useMutation({
    mutationFn: (v: { use: string; model: string; provider?: string }) => pinModel(v.use, v.model, v.provider),
    onError: (e: Error) => toast(e.message, true),
  })
  const current = presets.find((p) => p.id === preset)
  if (!current) return null
  const pins = config.data?.models?.stages ?? {}
  const stubs = config.data?.models?.stubs ?? false
  const byStage = new Map(current.plan.map((p) => [p.stage, p]))
  const isOptional = new Set(current.stages.filter((s) => s.optional).map((s) => s.id))
  // A skipped optional step just goes away with its switch; it has no model to stand in for.
  const rows = current.plan.filter((p) => p.model || p.missing || (p.skipped && !isOptional.has(p.stage)))
  const options = current.stages.filter((s) => s.optional && !byStage.get(s.id)?.skipped)

  return (
    <div className="composer">
      <div className="composer-block">
        <SectionHead>Preset</SectionHead>
        <select className="input preset-select" aria-label="Preset" aria-describedby="preset-note" value={preset} onChange={(e) => onPreset(e.target.value)}>
          {presets.map((p) => (
            <option key={p.id} value={p.id}>
              {p.name}
              {p.ready ? '' : ' · Needs setup'}
            </option>
          ))}
        </select>
        <p className="composer-note" id="preset-note">
          {!current.ready && <Pill tone="warn">Needs setup</Pill>} {current.description}
        </p>
      </div>

      {rows.length > 0 && (
        <div className="composer-block">
          <SectionHead>Models</SectionHead>
          <div className="opt-list stages">
            {rows.map((row) => {
              const by = row.by ? byStage.get(row.by) : undefined
              const pinned = pins[row.use]
              const list = models.data ?? []
              // The pinned model as the list has it: the plan cannot say where a missing one runs.
              const chosen = pinned ? (list.find((m) => m.id === pinned && m.provider === row.provider) ?? list.find((m) => m.id === pinned)) : undefined
              const auto = pinned ? automaticPick(list, row.use, stubs)?.name : row.model_name || row.model
              const groups = groupsFor(list, row.use, stubs, pinned)
              // Nothing to choose until a model is installed; a pinned one that is gone keeps its select, to be unpinned.
              const none = !!models.data && !pinned && groups.every(([, g]) => g.every((m) => !ready(m)))
              const where = row.skipped || row.problem || row.missing ? undefined : row.provider_label || (row.provider && row.provider !== 'local' ? row.provider : 'This computer')
              return (
                <div key={row.stage} className="opt stage-row">
                  <span className="opt-icon">
                    <StageIcon use={row.use} />
                  </span>
                  {row.skipped || row.problem ? (
                    <span className="opt-stage">{stageLabel(row.use)}</span>
                  ) : (
                    <label className="opt-stage" htmlFor={`model-${row.stage}`}>
                      {stageLabel(row.use)}
                    </label>
                  )}
                  <div className="opt-main">
                    {row.skipped ? (
                      <span className="composer-skip">
                        Done by {by?.model_name || by?.model} in the {stageLabel(by?.use ?? '').toLowerCase()} step
                      </span>
                    ) : row.problem ? (
                      <span className="composer-skip">{row.problem}</span>
                    ) : (
                      <select
                        id={`model-${row.stage}`}
                        className="model-pick"
                        value={chosen ? `${chosen.provider}|${chosen.id}` : ''}
                        // Until the list is here there is nothing to choose from.
                        disabled={pin.isPending || !models.data || !config.data || none}
                        onChange={(e) => {
                          const [provider, ...model] = e.target.value.split('|')
                          pin.mutate({ use: row.use, model: model.join('|'), provider: provider || undefined })
                        }}
                      >
                        <option value="">{none ? 'Not installed' : auto ? `Automatic (${auto})` : 'Automatic'}</option>
                        {groups.map(([label, group]) => (
                          <optgroup key={label} label={label}>
                            {group.map((m) => (
                              <option key={`${m.provider}|${m.id}`} value={`${m.provider}|${m.id}`} disabled={!ready(m)}>
                                {optionLabel(m)}
                              </option>
                            ))}
                          </optgroup>
                        ))}
                      </select>
                    )}
                    {row.missing && pinned && !row.problem && (
                      <span className="composer-where">
                        {chosen?.name ?? pinned} is not installed.{' '}
                        <button className="text-btn" disabled={pin.isPending} onClick={() => pin.mutate({ use: row.use, model: '' })}>
                          Use automatic
                        </button>
                      </span>
                    )}
                  </div>
                  {where && <span className="opt-where">{where}</span>}
                </div>
              )
            })}
          </div>
          <ViewsNotice plan={current.plan} onOpenModels={() => transition(() => navigate({ screen: 'models' }))} />
          <p className="composer-note">
            A model chosen here is used for every job.{' '}
            <button className="text-btn" onClick={() => transition(() => navigate({ screen: 'models' }))}>
              Models
            </button>
          </p>
        </div>
      )}

      {options.length > 0 && (
        <div className="composer-block">
          <SectionHead>Options</SectionHead>
          <div className="opt-list">
            {options.map((s) => {
              const optional = s.optional!
              const cannot = current.unavailable?.[s.id]
              const on = isOn(s.id, optional.default) && !cannot
              return (
                <div key={s.id} className="opt option">
                  <span className="opt-line">
                    <label className="opt-name" htmlFor={`option-${s.id}`}>
                      {optional.label}
                    </label>
                    {optional.help && <Hint>{optional.help}</Hint>}
                    <Switch id={`option-${s.id}`} checked={on} disabled={!!cannot} onChange={(v) => onToggle(s.id, v)} />
                  </span>
                  {cannot && <span className="composer-skip">{cannot}</span>}
                  {cannot && current.install?.[s.id] && <StageSetup install={current.install[s.id]!} onReady={() => onToggle(s.id, true)} />}
                </div>
              )
            })}
          </div>
        </div>
      )}
    </div>
  )
}
