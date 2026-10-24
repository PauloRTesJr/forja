// Dragging assets out of the window into Blender, Unity or a folder. The main process starts
// the OS drag, which must begin the moment the pointer moves, so each asset's file and icon
// are readied while the pointer rests on it.
import type { DragEvent } from 'react'
import { artifactURL, type Asset } from './api'

// A drop may move the file away (into a folder on the same drive): ready it again after this.
const FRESH_MS = 10_000
const readied = new Map<string, number>()
const icons = new Map<string, Promise<ArrayBuffer | undefined>>()

/** Readies an asset for a drag, at most once in a while. */
export function readyDrag(asset: Asset): void {
  const now = Date.now()
  if (now - (readied.get(asset.id) ?? 0) < FRESH_MS) return
  readied.set(asset.id, now)
  void iconOf(asset)
    .then((icon) => window.forja.prepareDrag(asset.id, icon))
    .catch(() => readied.delete(asset.id))
}

/** Hands the drag to the OS with these assets' files, in place of the page's own drag. */
export function dragOut(e: DragEvent, ids: string[]): void {
  e.preventDefault()
  window.forja.startDrag(ids)
}

/** The asset's thumbnail as a small PNG, which is what the OS drag image takes. */
function iconOf(asset: Asset): Promise<ArrayBuffer | undefined> {
  const thumb = asset.thumbnail
  if (!thumb) return Promise.resolve(undefined)
  let icon = icons.get(thumb)
  if (!icon) {
    icon = (async () => {
      const blob = await (await fetch(artifactURL(thumb, 'image/webp'))).blob()
      const bitmap = await createImageBitmap(blob, { resizeWidth: 96, resizeHeight: 96, resizeQuality: 'high' })
      const canvas = new OffscreenCanvas(96, 96)
      canvas.getContext('2d')!.drawImage(bitmap, 0, 0)
      bitmap.close()
      return (await canvas.convertToBlob({ type: 'image/png' })).arrayBuffer()
    })().catch(() => undefined)
    icons.set(thumb, icon)
  }
  return icon
}
