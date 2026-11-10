// Renders each asset's thumbnail once, off-screen, and stores it in the core as WebP.
// One hidden canvas and a serial queue keep this from competing with the UI. The canvas holds
// a WebGL context only while there is work: a browser keeps a limited number of contexts and
// takes the oldest away, and a thumbnail drawn on a lost context is an empty picture.
import { CORE, artifactURL, type Asset } from '../lib/api'
import { isBlank, Viewer } from './scene'

/** The widest a grid card's picture gets on screen, in CSS pixels. */
const WELL = 240
const MIN_SIZE = 256
const MAX_SIZE = 512

/** Pixels to render a thumbnail at: a card's picture at the display's pixel ratio, in steps of 64, within limits. */
export function thumbnailSize(pixelRatio: number): number {
  const wanted = Math.ceil((WELL * (Number.isFinite(pixelRatio) && pixelRatio > 0 ? pixelRatio : 1)) / 64) * 64
  return Math.min(MAX_SIZE, Math.max(MIN_SIZE, wanted))
}
/** How long the renderer waits for more work before it gives its context back. */
const IDLE_MS = 2000
/** No picture of a model is this small: a picture with nothing in it is a few hundred bytes. */
const SUSPICIOUSLY_SMALL = 4096

let viewer: Viewer | null = null
let canvas: HTMLCanvasElement | null = null
let idle: ReturnType<typeof setTimeout> | undefined
let queue: Promise<void> = Promise.resolve()
const pending = new Set<string>()
// Keyed by model hash too, so a repaired GLB gets a fresh attempt instead of a stale error.
const failed = new Map<string, string>()
const listeners = new Set<() => void>()
let failures = 0
/** Stored thumbnails already looked at, by hash, and models whose thumbnail was made again. */
const verified = new Set<string>()
const remade = new Set<string>()

const failureKey = (asset: Asset): string => `${asset.id}:${asset.glb}`

function renderer(): Viewer {
  clearTimeout(idle)
  if (viewer?.lost) release()
  if (!viewer) {
    canvas = document.createElement('canvas')
    canvas.style.cssText = 'position:fixed;left:-9999px;top:0;width:256px;height:256px;pointer-events:none'
    document.body.appendChild(canvas)
    viewer = new Viewer(canvas)
  }
  return viewer
}

function release(): void {
  clearTimeout(idle)
  viewer?.dispose()
  canvas?.remove()
  viewer = null
  canvas = null
}

/** The picture of one model. A context lost under it is the browser's doing: one more try on a new one. */
async function render(asset: Asset): Promise<Blob> {
  for (let attempt = 0; ; attempt++) {
    const v = renderer()
    try {
      await v.load(artifactURL(asset.glb))
      return await v.thumbnail(thumbnailSize(globalThis.devicePixelRatio))
    } catch (err) {
      const blank = (err as Error)?.name === 'BlankFrameError'
      if (attempt > 0 || !(blank || v.lost)) throw err
      release()
    }
  }
}

/** Why this asset's model could not be loaded, if it could not. */
export function thumbnailError(asset: Asset): string | undefined {
  return failed.get(failureKey(asset))
}

/** For useSyncExternalStore: the snapshot changes whenever a thumbnail fails. */
export function subscribeThumbnailErrors(onChange: () => void): () => void {
  listeners.add(onChange)
  return () => listeners.delete(onChange)
}
export const thumbnailErrorsVersion = (): number => failures

function enqueue(asset: Asset, onDone: (updated: Asset) => void): void {
  pending.add(asset.id)
  queue = queue
    .then(async () => {
      const blob = await render(asset)
      const res = await fetch(`${CORE}/v1/assets/${asset.id}/thumbnail`, { method: 'PUT', body: blob })
      // The asset may have been deleted meanwhile: nothing to show, and nothing went wrong.
      if (res.status === 404) return
      if (!res.ok) throw new Error(`saving the thumbnail failed: ${res.status}`)
      const updated = (await res.json()) as Asset
      if (updated.thumbnail) verified.add(updated.thumbnail)
      onDone(updated)
    })
    .catch((err: unknown) => {
      console.warn('thumbnail failed', asset.id, err)
      failed.set(failureKey(asset), err instanceof Error ? err.message : String(err))
      failures++
      for (const fn of listeners) fn()
    })
    .finally(() => {
      pending.delete(asset.id)
      if (pending.size === 0) {
        clearTimeout(idle)
        idle = setTimeout(release, IDLE_MS)
      }
    })
}

/** Makes the thumbnail of an asset that has none; `onDone` gets the asset as the core now has it. */
export function ensureThumbnail(asset: Asset, onDone: (updated: Asset) => void): void {
  // A model that failed once fails again: retrying on every library refresh only burns time.
  if (asset.thumbnail || pending.has(asset.id) || failed.has(failureKey(asset))) return
  enqueue(asset, onDone)
}

/** Makes an asset's thumbnail again, whatever it has now. */
export function remakeThumbnail(asset: Asset, onDone: (updated: Asset) => void): void {
  if (pending.has(asset.id)) return
  failed.delete(failureKey(asset))
  enqueue(asset, onDone)
}

/** True when the stored image shows nothing at all. */
async function storedIsBlank(hash: string): Promise<boolean> {
  const url = artifactURL(hash, 'image/webp')
  const head = await fetch(url, { method: 'HEAD' })
  const length = Number(head.headers.get('content-length'))
  if (!head.ok || !(length > 0) || length >= SUSPICIOUSLY_SMALL) return false
  const bitmap = await createImageBitmap(await (await fetch(url)).blob(), { resizeWidth: 32, resizeHeight: 32 })
  const c = new OffscreenCanvas(32, 32)
  const g = c.getContext('2d')!
  g.drawImage(bitmap, 0, 0)
  bitmap.close()
  return isBlank(g.getImageData(0, 0, 32, 32).data)
}

/**
 * Looks once at a thumbnail the library is showing, and makes it again if it shows nothing:
 * older versions stored empty pictures after the browser took their 3D context away.
 */
export function verifyThumbnail(asset: Asset, onDone: (updated: Asset) => void): void {
  const hash = asset.thumbnail
  if (!hash || verified.has(hash)) return
  verified.add(hash)
  void storedIsBlank(hash)
    .then((blank) => {
      // Once per model: one that really draws nothing must not be rendered on every visit.
      if (!blank || remade.has(failureKey(asset))) return
      remade.add(failureKey(asset))
      remakeThumbnail(asset, onDone)
    })
    .catch(() => undefined)
}
