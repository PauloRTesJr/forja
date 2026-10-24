import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useEffect, useRef, useState, type DragEvent } from 'react'
import { Composer } from '../components/Composer'
import { Corners, DropSigil, Folder } from '../components/icons'
import { Confirm, isPreparing, Preparing } from '../components/Prepare'
import { ScreenHead, SectionHead } from '../components/ScreenHead'
import { Setup } from '../components/Setup'
import { IMAGE, ViewsCard, type Draft } from '../components/ViewsCard'
import { api, fetchPresets, importImage, submitJobs, type Job, type SubmitBody } from '../lib/api'
import { keys } from '../lib/events'
import { needsAnswer, upsertJob } from '../lib/jobs'
import { transition, useNavigate } from '../lib/nav'
import { useToast } from '../lib/toast'
import { SIDE_VIEWS, viewReader } from '../lib/views'

const PROJECT_KEY = 'forja.project'
const OPTIONS_KEY = 'forja.options'
// A batch shows this many images being prepared, then a count of the rest.
const SHOWN_PREPARING = 3

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

export function Generate() {
  const navigate = useNavigate()
  const toast = useToast()
  const qc = useQueryClient()
  const [over, setOver] = useState(false)
  const jobs = useQuery({ queryKey: keys.jobs, queryFn: () => api<Job[]>('GET', '/v1/jobs?limit=100') })
  const preparing = jobs.data?.filter(isPreparing) ?? []
  const asking = jobs.data?.filter(needsAnswer) ?? []
  // The jobs dropped here last. The screen keeps them while they are prepared and confirmed,
  // then hands them to the queue.
  const [submitted, setSubmitted] = useState<string[]>([])
  const [preset, setPreset] = useState('mobile-prop')
  const [project, setProject] = useState(() => read(PROJECT_KEY) ?? '')
  // Switch states for optional stages, by "<preset>.<stage>".
  const [options, setOptions] = useState(storedOptions)
  const presets = useQuery({ queryKey: keys.presets, queryFn: () => fetchPresets() })
  // The same presets as if a job brought every other view: says which model would read them.
  const withViews = useQuery({ queryKey: keys.presetsWithViews, queryFn: () => fetchPresets([...SIDE_VIEWS]) })
  const reader = viewReader(withViews.data?.find((p) => p.id === preset)?.plan ?? [])
  // A single image waiting for its other views, when the shape model reads them.
  const [draft, setDraft] = useState<Draft | null>(null)
  const current = presets.data?.find((p) => p.id === preset)
  const optional = current?.stages.filter((s) => s.optional) ?? []
  const isOn = (stage: string, fallback: boolean): boolean => options[`${preset}.${stage}`] ?? fallback
  const blocked = current !== undefined && !current.ready
  // Once shown, setup stays until it is finished or skipped: its last step, the test run,
  // comes after the presets are already ready.
  const [setupOpen, setSetupOpen] = useState(false)
  if (blocked && !current?.error && !setupOpen) setSetupOpen(true)

  const submit = useMutation({
    mutationFn: (body: Pick<SubmitBody, 'paths' | 'input' | 'name' | 'views'>) =>
      submitJobs({
        ...body,
        preset,
        project: project || undefined,
        priority: (body.paths?.length ?? 1) === 1 ? 'interactive' : 'batch',
        overrides: Object.fromEntries(optional.map((s) => [`${s.id}.enabled`, isOn(s.id, s.optional!.default)])),
        // An image that turns out to hold several views waits for the user, on this screen.
        sheet: 'ask',
      }),
    onSuccess: (created) => {
      setDraft(null)
      qc.setQueryData<Job[]>(keys.jobs, (list) => created.reduce((l, j) => upsertJob(l, j), list ?? []))
      setSubmitted(created.map((j) => j.id))
    },
    onError: (e: Error) => toast(e.message, true),
  })

  // A drop's progress, and any question about it, go where the user is looking: the views
  // card that led here may have left the page scrolled past them.
  const prepsRef = useRef<HTMLDivElement>(null)
  const firstAsk = asking[0]?.id
  useEffect(() => {
    if (submitted.length === 0 && !firstAsk) return
    const reduce = window.matchMedia('(prefers-reduced-motion: reduce)').matches
    prepsRef.current?.scrollIntoView({ block: 'start', behavior: reduce ? 'auto' : 'smooth' })
  }, [submitted, firstAsk])

  // Once the jobs dropped here are prepared and none waits for the user, they go to the queue.
  // Going there unmounts this screen; the ref keeps a late job update from going twice.
  const handedOver = useRef<string[] | null>(null)
  useEffect(() => {
    if (submitted.length === 0 || handedOver.current === submitted || !jobs.data) return
    const mine = jobs.data.filter((j) => submitted.includes(j.id))
    if (mine.length < submitted.length || mine.some((j) => isPreparing(j) || needsAnswer(j))) return
    handedOver.current = submitted
    toast(`${mine.length === 1 ? mine[0]!.name : `${mine.length} images`} queued`)
    transition(() => navigate({ screen: 'queue' }))
  }, [jobs.data, submitted, toast, navigate])

  // One image, when the shape model reads other views, waits on the views card for them;
  // anything else queues at once.
  const queue = async (paths: string[]): Promise<void> => {
    if (blocked || paths.length === 0) return
    if (paths.length > 1 || !reader) return submit.mutate({ paths })
    try {
      const ref = await importImage(paths[0]!)
      transition(() => setDraft({ front: { path: paths[0]!, ref }, views: {} }))
    } catch (e) {
      toast((e as Error).message, true)
    }
  }

  const onDrop = (e: DragEvent): void => {
    e.preventDefault()
    setOver(false)
    if (blocked) return
    const paths = [...e.dataTransfer.files].map((f) => window.forja.pathForFile(f)).filter((p) => IMAGE.test(p))
    if (paths.length === 0) return toast('Drop PNG, JPEG or WebP images', true)
    void queue(paths)
  }

  const generate = (d: Draft): void => {
    const views = Object.fromEntries(SIDE_VIEWS.filter((v) => d.views[v]).map((v) => [v, d.views[v]!.ref]))
    submit.mutate({ input: d.front.ref, name: d.front.path, views })
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
      {setupOpen && <Setup onDone={() => setSetupOpen(false)} />}

      {(asking.length > 0 || preparing.length > 0) && (
        <div className="preps" ref={prepsRef}>
          {asking.map((j, i) => (
            <Confirm key={j.id} job={j} index={i} />
          ))}
          {preparing.slice(0, SHOWN_PREPARING).map((j, i) => (
            <Preparing key={j.id} job={j} index={asking.length + i} />
          ))}
          {preparing.length > SHOWN_PREPARING && (
            <p className="prep-more">
              and {preparing.length - SHOWN_PREPARING} more {preparing.length - SHOWN_PREPARING === 1 ? 'image' : 'images'} being
              prepared
            </p>
          )}
        </div>
      )}

      {draft ? (
        <ViewsCard
          draft={draft}
          reader={reader}
          busy={submit.isPending}
          onAdd={async (view, path) => {
            try {
              const ref = await importImage(path)
              setDraft((d) => d && { ...d, views: { ...d.views, [view]: { path, ref } } })
            } catch (e) {
              toast((e as Error).message, true)
            }
          }}
          onRemove={(view) => setDraft((d) => d && { ...d, views: { ...d.views, [view]: undefined } })}
          onGenerate={() => generate(draft)}
          onCancel={() => transition(() => setDraft(null))}
        />
      ) : (
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
            <span className="drop-hint">
              or click to browse · PNG, JPEG, WebP
              {reader && !blocked && ' · one image lets you add its other views'}
            </span>
          </div>
        </div>
      )}

      <div className="section">
        <SectionHead>How to make it</SectionHead>
        {presets.isPending && <div className="skeleton" style={{ height: 220 }} />}
        {presets.error && <div className="error">{(presets.error as Error).message}</div>}
        {presets.data && (
          <Composer presets={presets.data} preset={preset} onPreset={setPreset} isOn={isOn} onToggle={toggle} />
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
