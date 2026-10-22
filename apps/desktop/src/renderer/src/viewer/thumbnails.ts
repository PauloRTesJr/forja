// Renders each asset's thumbnail once, off-screen, and stores it in the core as WebP.
// One shared hidden canvas and a serial queue keep this from competing with the UI.
import { CORE, artifactURL, type Asset } from '../lib/api'
import { Viewer } from './scene'

let viewer: Viewer | null = null
let queue: Promise<void> = Promise.resolve()
const pending = new Set<string>()

function offscreenViewer(): Viewer {
  if (viewer) return viewer
  const canvas = document.createElement('canvas')
  canvas.style.cssText = 'position:fixed;left:-9999px;top:0;width:256px;height:256px;pointer-events:none'
  document.body.appendChild(canvas)
  viewer = new Viewer(canvas)
  return viewer
}

export function ensureThumbnail(asset: Asset, onDone: () => void): void {
  if (asset.thumbnail || pending.has(asset.id)) return
  pending.add(asset.id)
  queue = queue
    .then(async () => {
      const v = offscreenViewer()
      const stats = await v.load(artifactURL(asset.glb))
      const blob = await v.thumbnail(256)
      if (blob) await fetch(`${CORE}/v1/assets/${asset.id}/thumbnail`, { method: 'PUT', body: blob })
      await fetch(`${CORE}/v1/assets/${asset.id}`, {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ stats: { triangles: stats.triangles, vertices: stats.vertices, lods: stats.lods } }),
      })
      onDone()
    })
    .catch((err) => console.warn('thumbnail failed', asset.id, err))
    .finally(() => pending.delete(asset.id))
}
