import { useMutation, useQuery } from '@tanstack/react-query'
import { useState, type DragEvent } from 'react'
import { api, submitJobs, type Preset } from '../lib/api'
import { keys } from '../lib/events'
import { transition, useNavigate } from '../lib/nav'
import { useToast } from '../lib/toast'

const IMAGE = /\.(png|jpe?g|webp)$/i
const PROJECT_KEY = 'forja.project'

function storedProject(): string {
  try {
    return localStorage.getItem(PROJECT_KEY) ?? ''
  } catch {
    return ''
  }
}

export function Generate() {
  const navigate = useNavigate()
  const toast = useToast()
  const [over, setOver] = useState(false)
  const [preset, setPreset] = useState('mobile-prop')
  const [project, setProject] = useState(storedProject)
  const presets = useQuery({ queryKey: keys.presets, queryFn: () => api<Preset[]>('GET', '/v1/presets') })

  const submit = useMutation({
    mutationFn: (paths: string[]) =>
      submitJobs({ paths, preset, project: project || undefined, priority: paths.length === 1 ? 'interactive' : 'batch' }),
    onSuccess: (jobs) => {
      toast(`${jobs.length === 1 ? jobs[0]!.name : `${jobs.length} images`} queued`)
      transition(() => navigate({ screen: 'queue' }))
    },
    onError: (e: Error) => toast(e.message, true),
  })

  const onDrop = (e: DragEvent): void => {
    e.preventDefault()
    setOver(false)
    const paths = [...e.dataTransfer.files].map((f) => window.forja.pathForFile(f)).filter((p) => IMAGE.test(p))
    if (paths.length === 0) return toast('Drop PNG, JPEG or WebP images', true)
    submit.mutate(paths)
  }

  const pickProject = async (): Promise<void> => {
    const dir = await window.forja.pickFolder()
    if (!dir) return
    setProject(dir)
    try {
      localStorage.setItem(PROJECT_KEY, dir)
    } catch {
      /* per-viewer convenience only */
    }
  }

  return (
    <div className="screen">
      <div className="screen-head">
        <div>
          <h1>Generate</h1>
          <p className="sub">Drop reference images. Each becomes a game-ready GLB.</p>
        </div>
      </div>

      <div
        className={`drop enter ${over ? 'over' : ''}`}
        role="button"
        tabIndex={0}
        onClick={async () => {
          const paths = await window.forja.pickImages()
          if (paths.length) submit.mutate(paths)
        }}
        onKeyDown={async (e) => {
          if (e.key !== 'Enter' && e.key !== ' ') return
          const paths = await window.forja.pickImages()
          if (paths.length) submit.mutate(paths)
        }}
        onDragOver={(e) => {
          e.preventDefault()
          setOver(true)
        }}
        onDragLeave={() => setOver(false)}
        onDrop={onDrop}
      >
        <div className="drop-inner">
          <div className="drop-icon">⬆</div>
          <strong style={{ fontSize: 15 }}>{submit.isPending ? 'Queuing…' : 'Drop images or a folder of images'}</strong>
          <span className="muted">or click to browse · PNG, JPEG, WebP</span>
        </div>
      </div>

      <div className="section">
        <h2>Preset</h2>
        <div className="chips">
          {presets.isPending &&
            [0, 1, 2, 3].map((i) => <div key={i} className="skeleton" style={{ width: 200, height: 62 }} />)}
          {presets.data?.map((p, i) => (
            <button
              key={p.id}
              className="chip enter"
              style={{ '--i': i } as React.CSSProperties}
              aria-pressed={preset === p.id}
              onClick={() => setPreset(p.id)}
            >
              <strong>{p.name}</strong>
              <small>{p.description}</small>
            </button>
          ))}
        </div>
      </div>

      <div className="section">
        <h2>Export to</h2>
        <div className="row">
          <button className="btn" onClick={pickProject}>
            {project ? 'Change project…' : 'Choose a Unity project…'}
          </button>
          {project ? (
            <>
              <span className="mono muted">{project}</span>
              <button
                className="btn ghost"
                onClick={() => {
                  setProject('')
                  try {
                    localStorage.removeItem(PROJECT_KEY)
                  } catch {
                    /* ignore */
                  }
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
