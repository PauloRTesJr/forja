import { useMutation, useQuery } from '@tanstack/react-query'
import { api, fetchModels, pinModel, type Model, type Pick, type Preset } from '../lib/api'
import { keys } from '../lib/events'
import { money, stageLabel } from '../lib/format'
import { useToast } from '../lib/toast'
import { PresetEmblem } from './icons'

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

/** What the paid steps of one image cost, and the models whose price is not fixed. */
export function estimate(plan: Pick[]): { usd: number; unknown: string[]; hosts: string[] } {
  let usd = 0
  const unknown: string[] = []
  const hosts = new Set<string>()
  for (const p of plan) {
    if (p.skipped || !p.paid) continue
    if (p.provider) hosts.add(p.provider)
    if (!p.price || /second|minute|megapixel|token|step/i.test(p.price.unit)) unknown.push(p.model_name || p.model || p.stage)
    else usd += p.price.amount
  }
  return { usd, unknown, hosts: [...hosts] }
}

/**
 * How an image becomes an asset: the quality (a preset's budget) and the model of each step.
 * A model chosen here is the same setting as "Use for this stage" on the Models screen.
 */
export function Composer({
  presets,
  preset,
  onPreset,
  isOn,
  onToggle,
}: {
  presets: Preset[]
  preset: string
  onPreset: (id: string) => void
  isOn: (stage: string, fallback: boolean) => boolean
  onToggle: (stage: string, on: boolean) => void
}) {
  const toast = useToast()
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
  const rows = current.plan.filter((p) => p.model || p.missing || p.skipped)
  const optional = current.stages.filter((s) => s.optional && !byStage.get(s.id)?.skipped)
  const cost = estimate(current.plan)

  return (
    <div className="composer">
      <div className="composer-row">
        <span className="composer-label">Quality</span>
        <div className="quality" role="radiogroup" aria-label="Quality">
          {presets.map((p) => (
            <button
              key={p.id}
              role="radio"
              aria-checked={p.id === preset}
              className={`quality-option ${p.ready ? '' : 'unready'}`}
              onClick={() => onPreset(p.id)}
            >
              <span className="quality-emblem" aria-hidden>
                <PresetEmblem id={p.id} />
              </span>
              {p.name}
            </button>
          ))}
        </div>
      </div>
      <p className="composer-note">{current.description}</p>

      {rows.map((row) => {
        const by = row.by ? byStage.get(row.by) : undefined
        return (
          <div className="composer-row" key={row.stage}>
            <label className="composer-label" htmlFor={`model-${row.stage}`}>
              {stageLabel(row.use)}
            </label>
            {row.skipped ? (
              <span className="composer-skip">
                Done by {by?.model_name || by?.model} in the {stageLabel(by?.use ?? '').toLowerCase()} step
              </span>
            ) : (
              <select
                id={`model-${row.stage}`}
                className="input composer-select"
                value={pins[row.use] ? `${row.provider}|${row.model}` : ''}
                disabled={pin.isPending}
                onChange={(e) => {
                  const [provider, ...model] = e.target.value.split('|')
                  pin.mutate({ use: row.use, model: model.join('|'), provider: provider || undefined })
                }}
              >
                <option value="">
                  {pins[row.use] ? 'Automatic' : `Automatic · ${row.model_name || row.model || 'none installed'}`}
                </option>
                {groupsFor(models.data ?? [], row.use, stubs, pins[row.use]).map(([label, list]) => (
                  <optgroup key={label} label={label}>
                    {list.map((m) => (
                      <option key={`${m.provider}|${m.id}`} value={`${m.provider}|${m.id}`} disabled={!ready(m)}>
                        {optionLabel(m)}
                      </option>
                    ))}
                  </optgroup>
                ))}
              </select>
            )}
          </div>
        )
      })}

      {optional.length > 0 && (
        <div className="options">
          {optional.map((s) => {
            const on = isOn(s.id, s.optional!.default)
            return (
              <button key={s.id} className="switch" role="switch" aria-checked={on} onClick={() => onToggle(s.id, !on)}>
                <span className="switch-track" aria-hidden />
                {s.optional!.label}
              </button>
            )
          })}
        </div>
      )}

      {(cost.usd > 0 || cost.unknown.length > 0) && (
        <p className="composer-cost">
          {cost.usd > 0 && (
            <>
              About ${cost.usd.toFixed(2)} per image on{' '}
              {cost.hosts.map((id) => models.data?.find((m) => m.provider === id)?.provider_label ?? id).join(', ')}.{' '}
            </>
          )}
          {cost.unknown.length > 0 && <>{cost.unknown.join(', ')} has no fixed price. </>}
          The reference image is sent there; the asset comes back to this computer.
        </p>
      )}
    </div>
  )
}
