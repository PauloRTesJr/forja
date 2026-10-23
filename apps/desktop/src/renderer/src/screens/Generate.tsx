import { useMutation, useQuery } from '@tanstack/react-query'
import { useState, type DragEvent } from 'react'
import { Corners, DropSigil, Folder, PresetEmblem, Sparkle } from '../components/icons'
import { ScreenHead, SectionHead } from '../components/ScreenHead'
import { Setup } from '../components/Setup'
import { api, submitJobs, type Preset } from '../lib/api'
import { keys } from '../lib/events'
import { listOf, roman, taskNoun } from '../lib/format'
import { transition, useNavigate } from '../lib/nav'
import { useToast } from '../lib/toast'

const IMAGE = /\.(png|jpe?g|webp)$/i
const PROJECT_KEY = 'forja.project'
const OPTIONS_KEY = 'forja.options'

// Per-viewer conveniences only: storage may be missing or full, and the screen works without it.
function read(key: string): string | null {
  try {
    return localStorage.getItem(key)
  } catch {
    return null
  }
}

function write(key: string, value: string | null): void {
  try {
    if (value === null) localStorage.removeItem(key)
    else localStorage.setItem(key, value)
  } catch {
    /* ignore */
  }
}

function storedOptions(): Record<string, boolean> {
  try {
    return JSON.parse(read(OPTIONS_KEY) ?? '{}') as Record<string, boolean>
  } catch {
    return {}
  }
}

/** The models a preset will use, in pipeline order, each once. */
function modelsOf(p: Preset): string[] {
  const names = p.plan.filter((s) => s.model).map((s) => s.model_name || s.model!)
  return [...new Set(names)]
}

function missingOf(p: Preset): string[] {
  return [...new Set(p.plan.filter((s) => s.missing).map((s) => taskNoun(s.use)))]
}

export function Generate() {
  const navigate = useNavigate()
  const toast = useToast()
  const [over, setOver] = useState(false)
  const [preset, setPreset] = useState('mobile-prop')
  const [project, setProject] = useState(() => read(PROJECT_KEY) ?? '')
  // Switch states for optional stages, by "<preset>.<stage>".
  const [options, setOptions] = useState(storedOptions)
  const presets = useQuery({ queryKey: keys.presets, queryFn: () => api<Preset[]>('GET', '/v1/presets') })
  const current = presets.data?.find((p) => p.id === preset)
  const optional = current?.stages.filter((s) => s.optional) ?? []
  const isOn = (stage: string, fallback: boolean): boolean => options[`${preset}.${stage}`] ?? fallback
  const blocked = current !== undefined && !current.ready

  const submit = useMutation({
    mutationFn: (paths: string[]) =>
      submitJobs({
        paths,
        preset,
        project: project || undefined,
        priority: paths.length === 1 ? 'interactive' : 'batch',
        overrides: Object.fromEntries(optional.map((s) => [`${s.id}.enabled`, isOn(s.id, s.optional!.default)])),
      }),
    onSuccess: (jobs) => {
      toast(`${jobs.length === 1 ? jobs[0]!.name : `${jobs.length} images`} queued`)
      transition(() => navigate({ screen: 'queue' }))
    },
    onError: (e: Error) => toast(e.message, true),
  })

  const queue = (paths: string[]): void => {
    if (blocked) return
    if (paths.length) submit.mutate(paths)
  }

  const onDrop = (e: DragEvent): void => {
    e.preventDefault()
    setOver(false)
    if (blocked) return
    const paths = [...e.dataTransfer.files].map((f) => window.forja.pathForFile(f)).filter((p) => IMAGE.test(p))
    if (paths.length === 0) return toast('Drop PNG, JPEG or WebP images', true)
    submit.mutate(paths)
  }

  const pickProject = async (): Promise<void> => {
    const dir = await window.forja.pickFolder()
    if (!dir) return
    setProject(dir)
    write(PROJECT_KEY, dir)
  }

  const toggle = (stage: string, on: boolean): void => {
    const next = { ...options, [`${preset}.${stage}`]: on }
    setOptions(next)
    write(OPTIONS_KEY, JSON.stringify(next))
  }

  return (
    <div className="screen">
      <ScreenHead eyebrow={['Arcanum I', 'The Forge']} title="Generate" sub="Drop reference images. Each becomes a game-ready GLB." />

      {blocked && current?.error && (
        <div className="error" role="status">
          {current.name} cannot run: {current.error}
        </div>
      )}
      {blocked && !current?.error && <Setup />}

      <div
        className={`drop enter ${over && !blocked ? 'over' : ''} ${submit.isPending ? 'busy' : ''} ${blocked ? 'blocked' : ''}`}
        role="button"
        tabIndex={0}
        aria-disabled={blocked}
        onClick={async () => queue(blocked ? [] : await window.forja.pickImages())}
        onKeyDown={async (e) => {
          if (e.key !== 'Enter' && e.key !== ' ') return
          queue(blocked ? [] : await window.forja.pickImages())
        }}
        onDragOver={(e) => {
          e.preventDefault()
          setOver(true)
        }}
        onDragLeave={() => setOver(false)}
        onDrop={onDrop}
      >
        <span className="frame" aria-hidden />
        <Corners />
        <div className="drop-inner">
          <DropSigil />
          <strong className="drop-title">
            {blocked ? 'Set up models to start' : submit.isPending ? 'Queuing…' : over ? 'Release to queue them' : 'Drop images or a folder of images'}
          </strong>
          <span className="drop-hint">or click to browse · PNG, JPEG, WebP</span>
        </div>
      </div>

      <div className="section">
        <SectionHead>Preset</SectionHead>
        <div className="presets">
          {presets.isPending && [0, 1, 2, 3].map((i) => <div key={i} className="skeleton" style={{ height: 196 }} />)}
          {presets.error && <div className="error">{(presets.error as Error).message}</div>}
          {presets.data?.map((p, i) => (
            <button
              key={p.id}
              className={`preset enter ${p.ready ? '' : 'unready'}`}
              style={{ '--i': i } as React.CSSProperties}
              aria-pressed={preset === p.id}
              onClick={() => setPreset(p.id)}
            >
              <span className="frame" aria-hidden />
              <span className="tarot-num" aria-hidden>
                {roman(i + 1)}
              </span>
              <span className="emblem">
                <PresetEmblem id={p.id} />
              </span>
              <strong>{p.name}</strong>
              <small>{p.description}</small>
              {p.ready ? (
                <span className="preset-models">{modelsOf(p).join(' · ')}</span>
              ) : (
                <span className="preset-needs">Needs {p.error ? 'the engine' : `a ${listOf(missingOf(p))} model`}</span>
              )}
              <Sparkle size={12} className="chosen" />
            </button>
          ))}
        </div>
        {optional.length > 0 && (
          <div className="options">
            {optional.map((s) => {
              const on = isOn(s.id, s.optional!.default)
              return (
                <button key={s.id} className="switch" role="switch" aria-checked={on} onClick={() => toggle(s.id, !on)}>
                  <span className="switch-track" aria-hidden />
                  {s.optional!.label}
                </button>
              )
            })}
          </div>
        )}
      </div>

      <div className="section">
        <SectionHead>Export to</SectionHead>
        <div className="row">
          <button className="btn" onClick={pickProject}>
            <Folder />
            {project ? 'Change project…' : 'Choose a Unity project…'}
          </button>
          {project ? (
            <>
              <span className="mono muted">{project}</span>
              <button
                className="btn ghost"
                onClick={() => {
                  setProject('')
                  write(PROJECT_KEY, null)
                }}
              >
                Clear
              </button>
            </>
          ) : (
            <span className="muted">Assets stay in the library until you pick a project.</span>
          )}
        </div>
      </div>
    </div>
  )
}
