import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useEffect, useMemo, useState, type FormEvent } from 'react'
import { addCloudProvider, providerKinds, testProvider, type ProbeView, type ProviderField, type ProviderKind } from '../lib/api'
import { WORKFLOW_STAGES, badNumber, bodyFor, cloudKinds, kindKey, missing } from '../lib/cloud'
import { keys } from '../lib/events'
import { useToast } from '../lib/toast'
import { Choices } from './Controls'

/**
 * Adds a GPU the user rents (RunPod serverless or a pod, Modal) or a ComfyUI. The core says
 * which fields each kind needs; the settings are tried before anything is saved.
 */
export function AddCloud({ onClose }: { onClose: () => void }) {
  const toast = useToast()
  const qc = useQueryClient()
  const kinds = useQuery({ queryKey: ['provider-kinds'], queryFn: providerKinds, staleTime: Infinity })
  const options = useMemo(() => cloudKinds(kinds.data ?? []), [kinds.data])
  const [chosen, setChosen] = useState<string>()
  const kind = options.find((k) => kindKey(k) === chosen) ?? options[0]
  const [values, setValues] = useState<Record<string, string>>({})
  const [workflows, setWorkflows] = useState<Record<string, string>>({})
  const [probe, setProbe] = useState<ProbeView>()
  useEffect(() => {
    const onKey = (e: KeyboardEvent): void => {
      if (e.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose])

  const body = kind ? bodyFor(kind, values, workflows) : {}
  const lacking = kind ? missing(kind, values, workflows) : []
  const bad = kind ? badNumber(kind, values) : undefined
  const ready = !!kind && lacking.length === 0 && !bad
  const test = useMutation({ mutationFn: () => testProvider(body), onSuccess: setProbe, onError: () => setProbe(undefined) })
  const add = useMutation({
    mutationFn: () => addCloudProvider(body),
    onSuccess: (p) => {
      toast(`Added ${p.label}`)
      void qc.invalidateQueries({ queryKey: keys.providers })
      onClose()
    },
  })
  const edited = (): void => {
    setProbe(undefined)
    test.reset()
    add.reset()
  }
  const pick = (k: string): void => {
    setChosen(k)
    setValues({})
    setWorkflows({})
    edited()
  }
  const submit = (e: FormEvent): void => {
    e.preventDefault()
    if (ready) add.mutate()
  }
  const error = (add.error ?? test.error) as Error | null

  return (
    <div className="dialog-backdrop" onClick={onClose}>
      <div className="dialog" role="dialog" aria-modal aria-label="Add a cloud GPU or ComfyUI" onClick={(e) => e.stopPropagation()}>
        <h2>Add a cloud GPU or ComfyUI</h2>
        {kinds.isPending && <div className="skeleton" style={{ height: 160 }} />}
        {kinds.error && <div className="error">{(kinds.error as Error).message}</div>}
        {kind && (
          <>
            <Choices value={kindKey(kind)} onChange={pick} options={options.map((k) => ({ value: kindKey(k), label: k.label }))} />
            <p className="muted">{kind.description}</p>
            <form className="dialog-form" onSubmit={submit}>
              {kind.fields.map((f) =>
                f.type === 'workflows' ? (
                  <WorkflowPicks
                    key={f.key}
                    field={f}
                    chosen={workflows}
                    onChange={(next) => {
                      setWorkflows(next)
                      edited()
                    }}
                  />
                ) : (
                  <FieldInput
                    key={f.key}
                    field={f}
                    kind={kind}
                    value={values[f.key] ?? ''}
                    onChange={(v) => {
                      setValues((old) => ({ ...old, [f.key]: v }))
                      edited()
                    }}
                  />
                ),
              )}
              <label className="field">
                <span>Name (optional)</span>
                <input className="input" value={values.label ?? ''} onChange={(e) => setValues((old) => ({ ...old, label: e.target.value }))} placeholder={kind.label} />
              </label>
              {probe && (
                <div className="probe" role="status">
                  <span className="dot ok" />
                  <span>
                    {probe.version}
                    {probe.gpu ? ` · ${probe.gpu}` : ''} · {probe.latency_ms} ms
                  </span>
                  {probe.detail && <span className="muted">{probe.detail}</span>}
                </div>
              )}
              {bad && <div className="error">{bad.label} must be a number, 0 or more.</div>}
              {error && <div className="error">{error.message}</div>}
              <p className="muted small">
                Keys and tokens go to this computer's keychain, never to a file.
                {kind.paid && ' It is paid: stages use it when you pin a model to it, or when paid providers may step in.'}
              </p>
              <div className="row dialog-actions">
                <button type="button" className="btn ghost" onClick={onClose}>
                  Cancel
                </button>
                <button type="button" className="btn" disabled={!ready || test.isPending} onClick={() => test.mutate()}>
                  {test.isPending ? 'Trying…' : 'Try it'}
                </button>
                <button className="btn primary" disabled={!ready || add.isPending}>
                  {add.isPending ? 'Adding…' : 'Add'}
                </button>
              </div>
            </form>
          </>
        )}
      </div>
    </div>
  )
}

function FieldInput({ field: f, kind, value, onChange }: { field: ProviderField; kind: ProviderKind; value: string; onChange: (v: string) => void }) {
  return (
    <label className="field">
      <span>
        {f.label}
        {f.unit && ` (${f.unit})`}
        {f.optional && ' · optional'}
        {f.key === 'token' && kind.key_url && (
          <>
            {' · '}
            <a href={kind.key_url} target="_blank" rel="noreferrer">
              create one
            </a>
          </>
        )}
      </span>
      <input
        className="input"
        type={f.type === 'secret' ? 'password' : f.type === 'number' ? 'number' : 'text'}
        inputMode={f.type === 'number' ? 'decimal' : undefined}
        min={f.type === 'number' ? 0 : undefined}
        step={f.type === 'number' ? 'any' : undefined}
        autoComplete="off"
        value={value}
        placeholder={f.placeholder}
        onChange={(e) => onChange(e.target.value)}
      />
      {f.help && <span className="muted small">{f.help}</span>}
    </label>
  )
}

/** One workflow file per stage type, picked from disk; the core reads and keeps a copy. */
function WorkflowPicks({ field, chosen, onChange }: { field: ProviderField; chosen: Record<string, string>; onChange: (next: Record<string, string>) => void }) {
  const choose = async (use: string): Promise<void> => {
    const path = await window.forja.pickFile('ComfyUI workflow (API format)', ['json'])
    if (path) onChange({ ...chosen, [use]: path })
  }
  return (
    <div className="field">
      <span>{field.label}</span>
      <div className="workflow-picks">
        {WORKFLOW_STAGES.map(({ use, label }) => (
          <div key={use} className="workflow-pick">
            <strong>{label}</strong>
            <span className={`mono ${chosen[use] ? '' : 'muted'}`} title={chosen[use]}>
              {chosen[use] ? chosen[use].split(/[\\/]/).pop() : 'none'}
            </span>
            <button type="button" className="btn ghost" onClick={() => void choose(use)}>
              Choose…
            </button>
            {chosen[use] && (
              <button type="button" className="btn ghost" onClick={() => onChange({ ...chosen, [use]: '' })}>
                Clear
              </button>
            )}
          </div>
        ))}
      </div>
      {field.help && <span className="muted small">{field.help}</span>}
    </div>
  )
}
