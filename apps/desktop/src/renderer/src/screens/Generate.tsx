import { useMutation, useQuery } from '@tanstack/react-query'
import { useState, type DragEvent } from 'react'
import { Corners, DropSigil, Folder, PresetEmblem, Sparkle } from '../components/icons'
import { ScreenHead, SectionHead } from '../components/ScreenHead'
import { api, submitJobs, type Preset } from '../lib/api'
import { keys } from '../lib/events'
import { roman } from '../lib/format'
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
      <ScreenHead eyebrow={['Arcanum I', 'The Forge']} title="Generate" sub="Drop reference images. Each becomes a game-ready GLB." />

      <div
        className={`drop enter ${over ? 'over' : ''} ${submit.isPending ? 'busy' : ''}`}
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
        <span className="frame" aria-hidden />
        <Corners />
        <div className="drop-inner">
          <DropSigil />
          <strong className="drop-title">
            {submit.isPending ? 'Queuing…' : over ? 'Release to queue them' : 'Drop images or a folder of images'}
          </strong>
          <span className="drop-hint">or click to browse · PNG, JPEG, WebP</span>
        </div>
      </div>

      <div className="section">
        <SectionHead>Preset</SectionHead>
        <div className="presets">
          {presets.isPending && [0, 1, 2, 3].map((i) => <div key={i} className="skeleton" style={{ height: 196 }} />)}
          {presets.data?.map((p, i) => (
            <button
              key={p.id}
              className="preset enter"
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
              <Sparkle size={12} className="chosen" />
            </button>
          ))}
        </div>
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
