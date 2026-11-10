// Getting images into Generate: sorting what was dropped, pasted or picked, copying it into
// the core's store, and sending what is staged as jobs.
import { api, ApiError, uploadImage, type Job } from './api'
import { planSubmit, type Draft, type Staged } from './draft'

export const IMAGE = /\.(png|jpe?g|webp)$/i
export const IMAGE_TYPE = /^image\/(png|jpeg|webp)$/

export const fileName = (path: string): string => path.split(/[\\/]/).pop() || path

/** Something dropped on the window: a file's or a folder's path. */
export interface Dropped {
  path: string
  directory?: boolean
}

/** The files and folders of a drop or a paste. `pathFor` is the bridge's `pathForFile`. */
export function droppedFrom(data: Pick<DataTransfer, 'files' | 'items'>, pathFor: (f: File) => string): Dropped[] {
  // Files and the items of kind "file" come in the same order; only an item says "folder".
  const entries = [...data.items].filter((i) => i.kind === 'file').map((i) => i.webkitGetAsEntry?.() ?? null)
  return [...data.files].map((f, i) => ({ path: pathFor(f), directory: entries[i]?.isDirectory ?? false }))
}

/** What a drop holds: images to stage, folders to open, and the names of everything else. */
export function sortDropped(items: Dropped[]): { images: string[]; folders: string[]; skipped: string[] } {
  const images: string[] = []
  const folders: string[] = []
  const skipped: string[] = []
  for (const { path, directory } of items) {
    if (!path) continue
    if (directory) folders.push(path)
    else if (IMAGE.test(path)) images.push(path)
    else skipped.push(fileName(path))
  }
  return { images, folders, skipped }
}

/** Says which files of a drop were left out, so none goes missing silently. */
export function skippedNote(skipped: string[]): string | undefined {
  if (skipped.length === 0) return undefined
  if (skipped.length === 1) return `${skipped[0]} was skipped: it is not a PNG, JPEG or WebP image.`
  return `${skipped.length} files were skipped: they are not PNG, JPEG or WebP images.`
}

/** Says how many of the added images were already staged. */
export function duplicatesNote(n: number): string | undefined {
  if (n === 0) return undefined
  return n === 1 ? '1 image was already here.' : `${n} images were already here.`
}

/** A pasted picture has no file name: it gets the time it was pasted, so several stay apart. */
export function pastedName(type: string, at: Date): string {
  const two = (n: number): string => String(n).padStart(2, '0')
  const ext = type === 'image/jpeg' ? 'jpg' : type.slice('image/'.length) || 'png'
  return `pasted-${two(at.getHours())}${two(at.getMinutes())}${two(at.getSeconds())}.${ext}`
}

const newId = (): string => crypto.randomUUID()

/**
 * Copies picked files into the core's store, which also checks that each is an image. A folder
 * is staged as one entry and goes by its path: the core opens it, and refuses an empty one,
 * when the jobs are made. `problems` are sentences for the files that could not be staged.
 */
export async function stagePaths(images: string[], folders: string[] = []): Promise<{ staged: Staged[]; problems: string[] }> {
  const results = await Promise.allSettled(images.map((path) => api<{ hash: string }>('POST', '/v1/artifacts/import', { path })))
  const staged: Staged[] = []
  const problems: string[] = []
  results.forEach((r, i) => {
    const path = images[i]!
    if (r.status === 'rejected') problems.push(importProblem(path, r.reason))
    else staged.push({ id: newId(), name: fileName(path), path, ref: r.value.hash })
  })
  for (const path of folders) staged.push({ id: newId(), name: fileName(path), path, ref: '', folder: true })
  return { staged, problems }
}

function importProblem(path: string, reason: unknown): string {
  const said = reason instanceof Error ? reason.message : String(reason)
  // The core names the file in its own sentence; anything else gets the name in front.
  return said.includes(fileName(path)) ? said : `${fileName(path)}: ${said}`
}

/** Copies a picture that has no file, such as a pasted screenshot, into the store. */
export async function stagePasted(image: Blob, at = new Date()): Promise<Staged> {
  const ref = await uploadImage(image)
  return { id: newId(), name: pastedName(image.type, at), blob: image, ref }
}

/**
 * Turns the staged images into jobs. Returns the jobs the core created and, when it stopped
 * part-way (a spending limit, or a second request failing), the error that stopped it.
 */
export async function submitStaged(
  draft: Pick<Draft, 'images' | 'sides'>,
  settings: { preset: string; overrides: Record<string, unknown> },
): Promise<{ created: Job[]; error?: Error }> {
  const created: Job[] = []
  for (const step of planSubmit(draft)) {
    try {
      // The store may have swept a picture pasted long ago: its bytes go again.
      const input = step.upload ? await uploadImage(step.upload.blob!) : undefined
      created.push(...(await api<Job[]>('POST', '/v1/jobs', { ...step.body, ...settings, ...(input ? { input } : {}) })))
    } catch (e) {
      const error = e instanceof Error ? e : new Error(String(e))
      // A batch a spending limit stopped part-way says which jobs it had already queued.
      const partial = error instanceof ApiError ? error.problem?.created : undefined
      if (Array.isArray(partial)) created.push(...(partial as Job[]))
      return { created, error }
    }
  }
  return { created }
}
