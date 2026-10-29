// The image under the cursor while assets are dragged out of the window. The file's own icon
// (app.getFileIcon) is not an option: on macOS 26 it kills the main process with SIGTRAP
// (Electron 45). An asset without a thumbnail yet shows the app's icon on every OS instead.
import { nativeImage, type NativeImage } from 'electron'
import iconPng from '../../build/icon.png?asset'

// Matches the PNG the renderer makes from a thumbnail (lib/drag.ts).
const SIZE = 96

let appIcon: NativeImage | undefined

/** The drag image for an asset: its thumbnail as a PNG when it has one, else the app's icon. */
export function dragIcon(png?: ArrayBuffer): NativeImage {
  if (png) {
    const thumb = nativeImage.createFromBuffer(Buffer.from(png))
    if (!thumb.isEmpty()) return thumb
  }
  appIcon ??= nativeImage.createFromPath(iconPng).resize({ width: SIZE, height: SIZE, quality: 'best' })
  return appIcon
}
