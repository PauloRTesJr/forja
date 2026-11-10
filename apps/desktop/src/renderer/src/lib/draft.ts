// What Generate holds before "Generate" is pressed: the staged images, the side views of a
// single one, the jobs it started and how the user labelled a sheet. It lives outside the
// screen, so going to another screen and back loses nothing.
import { useSyncExternalStore } from 'react'
import type { SubmitBody, View } from './api'
import { SIDE_VIEWS, type SideView } from './views'

/** An image waiting to be generated, already copied into the core's store. */
export interface Staged {
  id: string
  /** The file's name, or the name given to a pasted picture. */
  name: string
  /** Where the file is; absent for a pasted picture, which exists only as `blob`. */
  path?: string
  blob?: Blob
  /** Its ref in the core's store, for showing it; empty for a folder, which has nothing to show. */
  ref: string
  /** A folder whose images the core opens when the job is made. */
  folder?: boolean
}

export interface Draft {
  images: Staged[]
  /** Other views of the subject; they count only while exactly one image is staged. */
  sides: Partial<Record<SideView, Staged>>
  /** Jobs started from this screen that the activity panel still follows. */
  tracked: string[]
  /** A waiting sheet's labels as the user left them, by job id. */
  labels: Record<string, (View | '')[]>
}

export const emptyDraft: Draft = { images: [], sides: {}, tracked: [], labels: {} }

// Windows and macOS file systems do not tell `Knight.png` from `knight.png`.
const same = (a: Staged, b: Staged): boolean => (a.path && b.path ? a.path.toLowerCase() === b.path.toLowerCase() : !a.path && !b.path && a.ref === b.ref)

/** Adds images, leaving out the ones already staged; `duplicates` counts those. */
export function addImages(d: Draft, add: Staged[]): { draft: Draft; duplicates: number } {
  const images = d.images.slice()
  let duplicates = 0
  for (const s of add) {
    if (images.some((x) => same(x, s))) duplicates++
    else images.push(s)
  }
  return { draft: duplicates === add.length ? d : { ...d, images }, duplicates }
}

export function removeImage(d: Draft, id: string): Draft {
  const images = d.images.filter((s) => s.id !== id)
  // Side views belong to the image they were added for.
  return { ...d, images, sides: images.length === 0 ? {} : d.sides }
}

/** Puts another image in the place of a staged one. */
export function replaceImage(d: Draft, id: string, s: Staged): Draft {
  return { ...d, images: d.images.map((x) => (x.id === id ? s : x)) }
}

/** Sets or, with no image, clears one side view. */
export function setSide(d: Draft, view: SideView, s?: Staged): Draft {
  const sides = { ...d.sides }
  if (s) sides[view] = s
  else delete sides[view]
  return { ...d, sides }
}

/** Empties the target; the jobs being followed and the sheet labels stay. */
export function clearStaged(d: Draft): Draft {
  return { ...d, images: [], sides: {} }
}

/** The side views that would be sent: only a single image has any. */
export function sidesInUse(d: Pick<Draft, 'images' | 'sides'>): Partial<Record<SideView, Staged>> {
  return d.images.length === 1 && !d.images[0]!.folder ? d.sides : {}
}

/** The other views a job from this draft would bring, in view order. */
export function sideViews(d: Pick<Draft, 'images' | 'sides'>): SideView[] {
  const sides = sidesInUse(d)
  return SIDE_VIEWS.filter((v) => sides[v])
}

/** One request to the core. `upload` names the staged picture whose bytes go first, as `input`. */
export interface SubmitStep {
  body: Omit<SubmitBody, 'preset' | 'overrides'> & { path?: string }
  upload?: Staged
}

/**
 * The requests that turn the staged images into jobs. Files go by path, so the core reads them
 * again as they are now and refuses the whole batch if one is gone; several files are one
 * request. A pasted picture has no file: its bytes are sent again and it goes by ref. A single
 * image takes its side views with it.
 */
export function planSubmit(d: Pick<Draft, 'images' | 'sides'>): SubmitStep[] {
  const single = d.images.length === 1 && !d.images[0]!.folder
  const priority = single ? 'interactive' : 'batch'
  const sides = sidesInUse(d)
  const views = Object.fromEntries(SIDE_VIEWS.filter((v) => sides[v]).map((v) => [v, sides[v]!.path ?? sides[v]!.ref]))
  if (single) {
    const only = d.images[0]!
    const withViews = Object.keys(views).length > 0 ? { views } : {}
    // An image that turns out to hold several views waits for the user; one that brings its views does not.
    if (only.path) return [{ body: { path: only.path, priority, sheet: 'ask', ...withViews } }]
    return [{ body: { name: only.name, priority, sheet: 'ask', ...withViews }, upload: only }]
  }
  const steps: SubmitStep[] = []
  const files = d.images.filter((s) => s.path).map((s) => s.path!)
  if (files.length > 0) steps.push({ body: { paths: files, priority, sheet: 'ask' } })
  for (const s of d.images.filter((x) => !x.path)) steps.push({ body: { name: s.name, priority, sheet: 'ask' }, upload: s })
  return steps
}

/** "3 images", "1 folder" or "2 images and 1 folder": what is staged, counted. */
export function stagedCount(images: readonly Pick<Staged, 'folder'>[]): string {
  const folders = images.filter((s) => s.folder).length
  const files = images.length - folders
  const parts = [files > 0 && `${files} ${files === 1 ? 'image' : 'images'}`, folders > 0 && `${folders} ${folders === 1 ? 'folder' : 'folders'}`]
  return parts.filter(Boolean).join(' and ')
}

/** What the primary button says for what is staged. */
export function generateLabel(d: Pick<Draft, 'images' | 'sides'>): string {
  const n = d.images.length
  if (d.images.some((s) => s.folder)) return `Forge ${stagedCount(d.images)}`
  if (n > 1) return `Forge ${n} images`
  const views = sideViews(d).length
  return views > 0 ? `Forge from ${views + 1} views` : 'Forge'
}

/**
 * The preset the screen opens with: the one chosen here before, else the "Default preset"
 * setting, else Mobile prop. A choice that no longer exists (a deleted custom preset) is passed
 * over. Before the presets arrive, the first candidate stands.
 */
export function startingPreset(presets: { id: string }[] | undefined, chosen: string | null | undefined, configured: string | undefined): string {
  const candidates = [chosen, configured, 'mobile-prop'].filter((id): id is string => !!id)
  if (!presets || presets.length === 0) return candidates[0]!
  return candidates.find((id) => presets.some((p) => p.id === id)) ?? presets[0]!.id
}

// The store: one draft for the app's lifetime.
let state: Draft = emptyDraft
const listeners = new Set<() => void>()

export const draftStore = {
  get: (): Draft => state,
  set(next: Draft | ((d: Draft) => Draft)): void {
    const value = typeof next === 'function' ? next(state) : next
    if (value === state) return
    state = value
    for (const l of listeners) l()
  },
  subscribe(l: () => void): () => void {
    listeners.add(l)
    return () => listeners.delete(l)
  },
}

export const useDraft = (): Draft => useSyncExternalStore(draftStore.subscribe, draftStore.get)

// Per-viewer choices. Storage may be missing or full, and the screen works without it.
const OPTIONS_KEY = 'forja.options'
const PRESET_KEY = 'forja.preset'

function read(key: string): string | null {
  try {
    return localStorage.getItem(key)
  } catch {
    return null
  }
}

function write(key: string, value: string): void {
  try {
    localStorage.setItem(key, value)
  } catch {
    /* ignore */
  }
}

/** Switch states of optional steps, by "<preset>.<stage>". */
export function storedOptions(): Record<string, boolean> {
  try {
    const v: unknown = JSON.parse(read(OPTIONS_KEY) ?? '{}')
    return v && typeof v === 'object' && !Array.isArray(v) ? (v as Record<string, boolean>) : {}
  } catch {
    return {}
  }
}
export const storeOptions = (options: Record<string, boolean>): void => write(OPTIONS_KEY, JSON.stringify(options))
export const storedPreset = (): string | null => read(PRESET_KEY)
export const storePreset = (id: string): void => write(PRESET_KEY, id)
