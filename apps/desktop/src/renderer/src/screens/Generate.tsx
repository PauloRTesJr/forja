import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useEffect, useEffectEvent, useRef, useState, type DragEvent } from 'react'
import { Composer, EstimateNote, optionalOverrides } from '../components/Composer'
import { ErrorBlock } from '../components/Controls'
import { AlertIcon, GenerateIcon } from '../components/icons'
import { ImageTarget } from '../components/ImageTarget'
import { ActivityPanel } from '../components/Prepare'
import { ScreenHead } from '../components/ScreenHead'
import { Setup } from '../components/Setup'
import { queuedMessage, stillTracked } from '../lib/activity'
import { api, fetchEstimate, fetchPresets, type Job, type Setup as SetupView } from '../lib/api'
import {
  addImages,
  clearStaged,
  draftStore,
  generateLabel,
  removeImage,
  replaceImage,
  setSide,
  sidesInUse,
  sideViews,
  startingPreset,
  storedOptions,
  storedPreset,
  storeOptions,
  storePreset,
  useDraft,
} from '../lib/draft'
import { keys } from '../lib/events'
import { bytes, stageLabel } from '../lib/format'
import { droppedFrom, duplicatesNote, IMAGE_TYPE, skippedNote, sortDropped, stagePasted, stagePaths, submitStaged, type Dropped } from '../lib/intake'
import { upsertJob } from '../lib/jobs'
import { transition, useNavigate } from '../lib/nav'
import { useToast } from '../lib/toast'
import { SIDE_VIEWS, viewReader, type SideView } from '../lib/views'

interface Config {
  project?: { default_preset?: string }
  models?: { stages?: Record<string, string> }
}

/** Something to say about the last images added, beside the target: what was left out, and why. */
interface Note {
  text: string
  bad?: boolean
}

const notesOf = (problems: string[], ...info: (string | undefined)[]): Note[] => [
  ...problems.map((text) => ({ text, bad: true })),
  ...info.filter((t): t is string => !!t).map((text) => ({ text })),
]

const hasFiles = (e: DragEvent): boolean => e.dataTransfer.types.includes('Files')

export function Generate() {
  const navigate = useNavigate()
  const toast = useToast()
  const qc = useQueryClient()
  const draft = useDraft()
  const jobs = useQuery({ queryKey: keys.jobs, queryFn: () => api<Job[]>('GET', '/v1/jobs?limit=100') })
  const presets = useQuery({ queryKey: keys.presets, queryFn: () => fetchPresets() })
  const config = useQuery({ queryKey: keys.config, queryFn: () => api<Config>('GET', '/v1/config/effective') })

  // The preset chosen here is remembered; before any choice, the "Default preset" setting stands.
  const [chosen, setChosen] = useState(storedPreset)
  const preset = startingPreset(presets.data, chosen, config.data?.project?.default_preset)
  const choose = (id: string): void => {
    setChosen(id)
    storePreset(id)
  }
  // Switch states for optional stages, by "<preset>.<stage>".
  const [options, setOptions] = useState(storedOptions)
  const isOn = (stage: string, fallback: boolean): boolean => options[`${preset}.${stage}`] ?? fallback
  const toggle = (stage: string, on: boolean): void => {
    const next = { ...options, [`${preset}.${stage}`]: on }
    setOptions(next)
    storeOptions(next)
  }

  // The same presets as if a job brought every other view: says which model would read them.
  const withViews = useQuery({ queryKey: keys.presetsWithViews, queryFn: () => fetchPresets([...SIDE_VIEWS]) })
  const reader = viewReader(withViews.data?.find((p) => p.id === preset)?.plan ?? [])
  // With side views staged, the settings show the models that would run with exactly those.
  const views = sideViews(draft)
  const forViews = useQuery({
    queryKey: [...keys.presetsWithViews, views.join(',')],
    queryFn: () => fetchPresets(views),
    enabled: views.length > 0,
    placeholderData: keepPreviousData,
  })
  const shown = (views.length > 0 && forViews.data) || presets.data
  const current = shown?.find((p) => p.id === preset)
  const blocked = current !== undefined && !current.ready
  // A first run: nothing can be generated with any preset, so there is nothing to stage for.
  const noneReady = presets.data !== undefined && presets.data.length > 0 && presets.data.every((p) => !p.ready)
  const pins = config.data?.models?.stages ?? {}
  const pinnedMissing = current?.plan.find((p) => p.missing && !p.problem && pins[p.use])

  const overrides = current ? optionalOverrides(current, isOn) : {}
  const count = draft.images.length
  // What the staged images would take and cost as the settings stand. The last answer stays
  // while the next loads, so the line changes in place.
  const estimate = useQuery({
    queryKey: [...keys.estimate(preset, overrides, views), Math.max(1, count)],
    queryFn: () => fetchEstimate({ preset, overrides, views, images: Math.max(1, count) }),
    enabled: current !== undefined && !blocked,
    placeholderData: keepPreviousData,
  })

  const [over, setOver] = useState(false)
  const [reading, setReading] = useState(0)
  const [notes, setNotes] = useState<Note[]>([])
  // Opened from the notice, and kept until closed: its last step, the test run, comes after
  // the presets are already ready.
  const [setupOpen, setSetupOpen] = useState(false)

  // The "added to the queue" notice goes when every job it spoke of ended before it was due.
  const queuedToast = useRef<{ ids: string[]; take: () => void } | null>(null)
  useEffect(() => {
    const shown = queuedToast.current
    if (!shown || !jobs.data) return
    const ended = shown.ids.every((id) => {
      const status = jobs.data.find((j) => j.id === id)?.status
      return status === 'cancelled' || status === 'failed'
    })
    if (!ended) return
    shown.take()
    queuedToast.current = null
  }, [jobs.data])

  // The button is disabled while a submit is in flight, but two activations can land before
  // that renders: this is what keeps them from queueing the images twice.
  const sending = useRef(false)
  const submit = useMutation({
    mutationFn: () => submitStaged(draftStore.get(), { preset, overrides }),
    onSuccess: ({ created, error }) => {
      if (created.length > 0) {
        qc.setQueryData<Job[]>(keys.jobs, (list) => created.reduce((l, j) => upsertJob(l, j), list ?? []))
        // What ended earlier makes room in the panel for the jobs just started.
        draftStore.set((d) => ({ ...clearStaged(d), tracked: [...stillTracked(d.tracked, qc.getQueryData<Job[]>(keys.jobs)), ...created.map((j) => j.id)] }))
        const take = toast(queuedMessage(created), false, { label: 'View queue', run: () => transition(() => navigate({ screen: 'queue' })) })
        queuedToast.current = { ids: created.map((j) => j.id), take }
      }
      setNotes(error ? [{ text: error.message, bad: true }] : [])
    },
    onError: (e: Error) => setNotes([{ text: e.message, bad: true }]),
    onSettled: () => {
      sending.current = false
    },
  })
  const busy = submit.isPending
  const canGenerate = count > 0 && reading === 0 && current !== undefined && !blocked
  const generate = (): void => {
    if (sending.current || !canGenerate) return
    sending.current = true
    submit.mutate()
  }

  const stage = async (items: Dropped[]): Promise<void> => {
    if (noneReady || sending.current) return
    const sorted = sortDropped(items)
    const { images, folders, skipped } = sorted
    const adding = images.length + folders.length
    if (adding === 0) return setNotes(notesOf([], skippedNote(skipped) ?? 'Add PNG, JPEG or WebP images.'))
    setReading((n) => n + adding)
    try {
      const { staged, problems } = await stagePaths(images, folders)
      const { draft: next, duplicates } = addImages(draftStore.get(), staged)
      draftStore.set(next)
      setNotes(notesOf(problems, skippedNote(skipped), duplicatesNote(duplicates)))
    } finally {
      setReading((n) => n - adding)
    }
  }
  const browse = async (): Promise<void> => stage((await window.forja.pickImages()).map((path) => ({ path })))

  const replace = async (id: string): Promise<void> => {
    const [path] = await window.forja.pickImages()
    if (!path) return
    const { staged, problems } = await stagePaths([path])
    if (staged[0]) draftStore.set((d) => replaceImage(d, id, staged[0]!))
    setNotes(notesOf(problems))
  }

  const setSideView = async (view: SideView, path?: string): Promise<void> => {
    if (!path) return draftStore.set((d) => setSide(d, view))
    const { staged, problems } = await stagePaths([path])
    if (staged[0]) draftStore.set((d) => setSide(d, view, staged[0]))
    setNotes(notesOf(problems))
  }

  // Copied files paste as their paths; a copied bitmap, such as a screenshot, has no file and
  // goes to the store as bytes.
  const onPaste = async (e: ClipboardEvent): Promise<void> => {
    if (noneReady || (e.target instanceof Element && e.target.closest('input, textarea, select, [contenteditable]'))) return
    const data = e.clipboardData
    if (!data || data.files.length === 0) return
    e.preventDefault()
    const items = droppedFrom(data, window.forja.pathForFile)
    if (items.some((i) => i.path)) return stage(items)
    const image = [...data.files].find((f) => IMAGE_TYPE.test(f.type))
    if (!image) return setNotes(notesOf([], 'Paste PNG, JPEG or WebP images.'))
    setReading((n) => n + 1)
    try {
      const pasted = await stagePasted(image)
      const { draft: next, duplicates } = addImages(draftStore.get(), [pasted])
      draftStore.set(next)
      setNotes(notesOf([], duplicatesNote(duplicates)))
    } catch (err) {
      setNotes(notesOf([`The pasted picture could not be read: ${(err as Error).message}`]))
    } finally {
      setReading((n) => n - 1)
    }
  }
  const pasted = useEffectEvent(onPaste)
  const pressed = useEffectEvent((e: KeyboardEvent): void => {
    const mod = window.forja.platform === 'darwin' ? e.metaKey : e.ctrlKey
    if (mod && e.key === 'Enter') {
      e.preventDefault()
      generate()
    }
  })
  useEffect(() => {
    const paste = (e: ClipboardEvent): void => void pasted(e)
    const key = (e: KeyboardEvent): void => pressed(e)
    document.addEventListener('paste', paste)
    document.addEventListener('keydown', key)
    return () => {
      document.removeEventListener('paste', paste)
      document.removeEventListener('keydown', key)
    }
  }, [])

  // The whole screen takes a drop, and always answers the drag itself: a file let through to
  // the browser would open in place of the app.
  const onDragOver = (e: DragEvent): void => {
    if (!hasFiles(e)) return
    // A side-view slot under the pointer has taken it.
    if (e.defaultPrevented) return setOver(false)
    e.preventDefault()
    if (noneReady || busy) e.dataTransfer.dropEffect = 'none'
    else setOver(true)
  }
  const onDrop = (e: DragEvent): void => {
    if (!hasFiles(e)) return
    const taken = e.defaultPrevented
    e.preventDefault()
    setOver(false)
    if (!taken) void stage(droppedFrom(e.dataTransfer, window.forja.pathForFile))
  }

  const why = count === 0 ? 'Add an image to start.' : blocked ? `${current.name} cannot run yet.` : undefined

  return (
    <div
      className="screen generate"
      onDragEnter={onDragOver}
      onDragOver={onDragOver}
      onDragLeave={(e) => {
        if (!e.currentTarget.contains(e.relatedTarget as Node | null)) setOver(false)
      }}
      onDrop={onDrop}
    >
      <ScreenHead title="Generate" sub="Turn reference images into game-ready 3D assets." />

      {blocked && current.error && (
        <ErrorBlock role="status">
          {current.name} cannot run: {current.error}
        </ErrorBlock>
      )}
      {blocked && !current.error && pinnedMissing && (
        <div className="notice generate-notice enter">
          <div>
            <strong>
              {stageLabel(pinnedMissing.use)} is set to a model that is not installed
            </strong>
            <span>Choose another model for it in the settings below, or install it on the Models screen.</span>
          </div>
          <button className="btn" onClick={() => transition(() => navigate({ screen: 'models' }))}>
            Open Models
          </button>
        </div>
      )}
      {((blocked && !current.error && !pinnedMissing) || setupOpen) && (
        <SetupNotice
          title={!blocked ? 'Model setup' : noneReady ? 'Set up models to start' : `${current.name} needs a model that is not installed`}
          open={setupOpen}
          onToggle={() => setSetupOpen(!setupOpen)}
        />
      )}
      {setupOpen && <Setup onDone={() => setSetupOpen(false)} />}

      <ActivityPanel jobs={jobs.data} tracked={draft.tracked} />

      <div className="workspace">
        <div className="workspace-input">
          <ImageTarget
            images={draft.images}
            sides={sidesInUse(draft)}
            reader={reader}
            over={over}
            reading={reading}
            blocked={noneReady}
            busy={busy}
            onBrowse={() => void browse()}
            onRemove={(id) => draftStore.set((d) => removeImage(d, id))}
            onReplace={(id) => void replace(id)}
            onClear={() => {
              draftStore.set(clearStaged)
              setNotes([])
            }}
            onSide={setSideView}
          />
          {notes.length > 0 && (
            <ul className="target-notes" role="status">
              {notes.map((n) => (
                <li key={n.text} className={n.bad ? 'bad' : ''}>
                  {n.bad && <AlertIcon size={14} />}
                  <span>{n.text}</span>
                </li>
              ))}
            </ul>
          )}
        </div>

        <section className="workspace-settings" aria-label="Settings">
          {presets.isPending && <div className="skeleton" style={{ height: 320 }} />}
          {presets.error && (
            <ErrorBlock>
              The settings could not be loaded: {(presets.error as Error).message}{' '}
              <button className="text-btn" onClick={() => void presets.refetch()}>
                Try again
              </button>
            </ErrorBlock>
          )}
          {shown && <Composer presets={shown} preset={preset} onPreset={choose} isOn={isOn} onToggle={toggle} />}
          {shown && (
            <div className="generate-foot">
              <div className="generate-row">
                <EstimateNote estimate={blocked || estimate.isError ? null : estimate.data} />
                <button className="btn primary" onClick={generate} disabled={!canGenerate || busy} aria-describedby={why ? 'generate-why' : undefined}>
                  <GenerateIcon />
                  {busy ? 'Queuing…' : generateLabel(draft)}
                </button>
              </div>
              {why && (
                <span className="generate-why" id="generate-why">
                  {why}
                </span>
              )}
            </div>
          )}
        </section>
      </div>
    </div>
  )
}

/**
 * Says that models are missing without taking the screen over: what this computer could run,
 * a way into the guided setup (and back out of it), and the way to another computer or a cloud.
 */
function SetupNotice({ title, open, onToggle }: { title: string; open: boolean; onToggle: () => void }) {
  const navigate = useNavigate()
  const setup = useQuery({ queryKey: keys.setup, queryFn: () => api<SetupView>('GET', '/v1/setup') })
  const s = setup.data
  const gpu = s?.system?.devices?.find((d) => d.backend !== 'cpu')
  const here = gpu ? `This computer (${gpu.name}, ${bytes(gpu.vram_bytes)})` : 'This computer'
  const n = s?.install.length ?? 0
  const remote = s?.stages.some((st) => st.status === 'remote') ?? false
  const line = setup.isPending
    ? 'Looking at what this computer can run…'
    : !s
      ? 'Forja needs its models before it can generate.'
      : s.ready
        ? 'Every preset has its models. Run the sample once to check the whole pipeline.'
        : n > 0
          ? `${here} can run ${remote ? 'part of the pipeline' : 'everything'}: ${n} ${n === 1 ? 'model' : 'models'}, about ${Math.round(s.disk_gb)} GB to download.`
          : `${here} cannot run the models that are missing. Use another computer that runs Forja, or a cloud GPU.`
  return (
    <div className="notice generate-notice enter">
      <div>
        <strong>{title}</strong>
        <span>{line}</span>
      </div>
      <div className="row">
        <button className="btn ghost" onClick={() => transition(() => navigate({ screen: 'settings' }))}>
          Use another computer or cloud
        </button>
        <button className={`btn ${open ? '' : 'primary'}`} aria-expanded={open} onClick={onToggle}>
          {open ? 'Not now' : 'Set up models'}
        </button>
      </div>
    </div>
  )
}
