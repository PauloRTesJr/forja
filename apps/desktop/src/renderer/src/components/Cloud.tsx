import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useMemo, useRef, useState, type FormEvent } from 'react'
import { addCloudProvider, providerKinds, testProvider, type ProbeView, type ProviderField, type ProviderKind } from '../lib/api'
import { VOLUME_FIELDS, WORKFLOW_STAGES, badNumber, bodyFor, carryValues, cloudKinds, kindKey, missing, plainProblem } from '../lib/cloud'
import { keys } from '../lib/events'
import { useToast } from '../lib/toast'
import { Choices, ErrorBlock, Pill } from './Controls'
import { Dialog, DialogActions } from './Dialog'
import { ChevronIcon } from './icons'

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
  const [problem, setProblem] = useState<string>()
  const [volumeOpen, setVolumeOpen] = useState(false)
  // The request under way, so Cancel and Escape call it off instead of leaving it to finish unseen.
  const attempt = useRef<AbortController>(undefined)
  const fresh = (): AbortSignal => {
    attempt.current?.abort()
    attempt.current = new AbortController()
    return attempt.current.signal
  }
  const failed = (e: Error): void => {
    if (!attempt.current?.signal.aborted) setProblem(plainProblem(e.message, true))
  }

  const body = kind ? bodyFor(kind, values, workflows) : {}
  const lacking = kind ? missing(kind, values, workflows) : []
  const bad = kind ? badNumber(kind, values) : undefined
  const ready = !!kind && lacking.length === 0 && !bad
  const test = useMutation({ mutationFn: () => testProvider(body, fresh()), onSuccess: setProbe, onError: failed })
  const add = useMutation({
    mutationFn: () => addCloudProvider(body, fresh()),
    onSuccess: (p) => {
      toast(`Added ${p.label}`)
      void qc.invalidateQueries({ queryKey: keys.providers })
      onClose()
    },
    onError: failed,
  })
  const busy = test.isPending || add.isPending
  const close = (): void => {
    attempt.current?.abort()
    onClose()
  }
  const edited = (): void => {
    setProbe(undefined)
    setProblem(undefined)
  }
  const pick = (k: string): void => {
    const to = options.find((o) => kindKey(o) === k)
    if (!to || k === (kind && kindKey(kind))) return
    setChosen(k)
    setValues((old) => carryValues(to, old))
    setWorkflows({})
    edited()
  }
  const submit = (e: FormEvent): void => {
    e.preventDefault()
    if (!ready || busy) return
    setProblem(undefined)
    add.mutate()
  }
  const tryIt = (): void => {
    setProblem(undefined)
    setProbe(undefined)
    test.mutate()
  }
  const dirty = Object.values(values).some(Boolean) || Object.values(workflows).some(Boolean)
  const field = (f: ProviderField) =>
    f.type === 'workflows' ? (
      <WorkflowPicks
        key={f.key}
        field={f}
        chosen={workflows}
        disabled={busy}
        onChange={(next) => {
          setWorkflows(next)
          edited()
        }}
      />
    ) : (
      <FieldInput
        key={f.key}
        field={f}
        kind={kind!}
        value={values[f.key] ?? ''}
        readOnly={busy}
        onChange={(v) => {
          setValues((old) => ({ ...old, [f.key]: v }))
          edited()
        }}
      />
    )
  const volume = kind?.fields.filter((f) => VOLUME_FIELDS.includes(f.key)) ?? []
  const volumeShown = volumeOpen || volume.some((f) => !!values[f.key])

  return (
    <Dialog title="Add a cloud GPU or ComfyUI" keepOpen={dirty || busy} onClose={close}>
      {kinds.isPending && <div className="skeleton" style={{ height: 160 }} />}
      {kinds.error && <ErrorBlock>{(kinds.error as Error).message}</ErrorBlock>}
      {kinds.data && !kind && <ErrorBlock>This version of Forja's core cannot add cloud providers.</ErrorBlock>}
      {kind && (
        <>
          <Choices label="Kind of provider" value={kindKey(kind)} disabled={busy} onChange={pick} options={options.map((k) => ({ value: kindKey(k), label: k.label }))} />
          <p className="muted">{kind.description}</p>
          <form className="dialog-form cloud-form" onSubmit={submit}>
            {kind.fields.filter((f) => !VOLUME_FIELDS.includes(f.key)).map(field)}
            {volume.length > 0 && (
              <div className="cloud-volume">
                <button type="button" className="disclosure" aria-expanded={volumeShown} onClick={() => setVolumeOpen(!volumeShown)}>
                  Network volume (optional)
                  <ChevronIcon size={14} />
                </button>
                {volumeShown && (
                  <div className="dialog-form enter">
                    <p className="muted small">Fill in all four, or none: with a volume, files move through it instead of inside each job.</p>
                    {volume.map(field)}
                  </div>
                )}
              </div>
            )}
            <label className="field">
              <span>Name (optional)</span>
              <input
                className="input"
                value={values.label ?? ''}
                readOnly={busy}
                onChange={(e) => setValues((old) => ({ ...old, label: e.target.value }))}
                placeholder={kind.label}
              />
            </label>
            <p className="muted small">
              Keys and tokens go to this computer's keychain, never to a file.
              {kind.paid && ' It costs money: stages use it for the models you pick it for, or when paid providers may step in.'}
            </p>
            <div className="dialog-foot">
              {probe && (
                <div className="probe" role="status">
                  <Pill tone="ok" dot>
                    It answers
                  </Pill>
                  <span>
                    {probe.version}
                    {probe.gpu ? ` · ${probe.gpu}` : ''} · {probe.latency_ms} ms
                  </span>
                  {probe.detail && <span className="muted">{probe.detail}</span>}
                </div>
              )}
              {bad && <ErrorBlock>{bad.label} must be a number, 0 or more.</ErrorBlock>}
              {problem && <ErrorBlock role="alert">{problem}</ErrorBlock>}
              <DialogActions>
                {!ready && !busy && lacking.length > 0 && <span className="dim small cloud-lacking">Still needed: {lacking.map((f) => f.label).join(', ')}</span>}
                <button type="button" className="btn ghost" onClick={close}>
                  Cancel
                </button>
                <button type="button" className="btn" disabled={!ready || busy} onClick={tryIt}>
                  {test.isPending ? 'Testing…' : 'Test connection'}
                </button>
                <button className="btn primary" disabled={!ready || busy}>
                  {add.isPending ? 'Adding…' : 'Add'}
                </button>
              </DialogActions>
            </div>
          </form>
        </>
      )}
    </Dialog>
  )
}

function FieldInput({
  field: f,
  kind,
  value,
  readOnly,
  onChange,
}: {
  field: ProviderField
  kind: ProviderKind
  value: string
  readOnly: boolean
  onChange: (v: string) => void
}) {
  return (
    <label className="field">
      <span>
        {f.label}
        {f.unit && ` (${f.unit})`}
        <span className="dim"> · {f.optional ? 'optional' : 'required'}</span>
        {f.key === 'token' && kind.key_url && (
          <>
            {' · '}
            <a className="field-link" href={kind.key_url} target="_blank" rel="noreferrer">
              create one
            </a>
          </>
        )}
      </span>
      <input
        className="input"
        type={f.type === 'secret' ? 'password' : 'text'}
        inputMode={f.type === 'number' ? 'decimal' : undefined}
        autoComplete="off"
        spellCheck={false}
        aria-required={!f.optional || undefined}
        value={value}
        readOnly={readOnly}
        placeholder={f.placeholder}
        onChange={(e) => onChange(e.target.value)}
      />
      {f.help && <span className="muted small">{f.help}</span>}
    </label>
  )
}

/** One workflow file per stage type, picked from disk; the core reads and keeps a copy. */
function WorkflowPicks({
  field,
  chosen,
  disabled,
  onChange,
}: {
  field: ProviderField
  chosen: Record<string, string>
  disabled: boolean
  onChange: (next: Record<string, string>) => void
}) {
  const choose = async (use: string): Promise<void> => {
    const path = await window.forja.pickFile('ComfyUI workflow (API format)', ['json'])
    if (path) onChange({ ...chosen, [use]: path })
  }
  return (
    <div className="field" role="group" aria-label={field.label}>
      <span>{field.label}</span>
      <div className="workflow-picks">
        {WORKFLOW_STAGES.map(({ use, label }) => (
          <div key={use} className="workflow-pick">
            <strong>{label}</strong>
            <span className={`mono ${chosen[use] ? '' : 'dim'}`} title={chosen[use]}>
              {chosen[use] ? chosen[use].split(/[\\/]/).pop() : 'None'}
            </span>
            <button type="button" className="btn sm" disabled={disabled} aria-label={`Choose the ${label.toLowerCase()} workflow`} onClick={() => void choose(use)}>
              Choose…
            </button>
            {chosen[use] && (
              <button
                type="button"
                className="btn ghost sm"
                disabled={disabled}
                aria-label={`Clear the ${label.toLowerCase()} workflow`}
                onClick={() => onChange({ ...chosen, [use]: '' })}
              >
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
