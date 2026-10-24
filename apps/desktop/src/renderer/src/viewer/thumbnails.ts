// Renders each asset's thumbnail once, off-screen, and stores it in the core as WebP.
// One shared hidden canvas and a serial queue keep this from competing with the UI.
import { CORE, artifactURL, type Asset } from '../lib/api'
import { Viewer } from './scene'

let viewer: Viewer | null = null
let queue: Promise<void> = Promise.resolve()
const pending = new Set<string>()
// Keyed by model hash too, so a repaired GLB gets a fresh attempt instead of a stale error.
const failed = new Map<string, string>()
const listeners = new Set<() => void>()
let failures = 0

const failureKey = (asset: Asset): string => `${asset.id}:${asset.glb}`

function offscreenViewer(): Viewer {
  if (viewer) return viewer
  const canvas = document.createElement('canvas')
  canvas.style.cssText = 'position:fixed;left:-9999px;top:0;width:256px;height:256px;pointer-events:none'
  document.body.appendChild(canvas)
  viewer = new Viewer(canvas)
  return viewer
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

export function ensureThumbnail(asset: Asset, onDone: () => void): void {
  // A model that failed once fails again: retrying on every library refresh only burns time.
  if (asset.thumbnail || pending.has(asset.id) || failed.has(failureKey(asset))) return
  pending.add(asset.id)
  queue = queue
    .then(async () => {
      const v = offscreenViewer()
      await v.load(artifactURL(asset.glb))
      const blob = await v.thumbnail(256)
      if (blob) await fetch(`${CORE}/v1/assets/${asset.id}/thumbnail`, { method: 'PUT', body: blob })
      onDone()
    })
    .catch((err: unknown) => {
      console.warn('thumbnail failed', asset.id, err)
      failed.set(failureKey(asset), err instanceof Error ? err.message : String(err))
      failures++
      for (const fn of listeners) fn()
    })
    .finally(() => pending.delete(asset.id))
}
