// The only bridge between the sandboxed renderer and Electron. Keep it small and typed.
import { contextBridge, ipcRenderer, webUtils, type IpcRendererEvent } from 'electron'

type Listener<T> = (value: T) => void

/** The app's connection to its core (main/state.ts). `generation` counts event-stream connections. */
export interface CoreState {
  phase: 'starting' | 'connected' | 'reconnecting' | 'failed'
  generation: number
  /** `failed`: what went wrong, for people. */
  message?: string
  /** `failed`: the core's last words, for a bug report. */
  detail?: string
}

const bridge = {
  platform: process.platform,
  onEvent(fn: Listener<{ seq: number; type: string; data: unknown }>): () => void {
    const handler = (_: IpcRendererEvent, ev: { seq: number; type: string; data: unknown }): void => fn(ev)
    ipcRenderer.on('core:event', handler)
    return () => ipcRenderer.off('core:event', handler)
  },
  /** What the app knows about its core right now; the page asks when it loads, also after a reload. */
  coreState: (): Promise<CoreState> => ipcRenderer.invoke('core:state'),
  onCoreState(fn: Listener<CoreState>): () => void {
    const handler = (_: IpcRendererEvent, state: CoreState): void => fn(state)
    ipcRenderer.on('core:state', handler)
    return () => ipcRenderer.off('core:state', handler)
  },
  /** Events were lost on the way: what the page built from them must be read again. */
  onResync(fn: () => void): () => void {
    const handler = (): void => fn()
    ipcRenderer.on('core:resync', handler)
    return () => ipcRenderer.off('core:resync', handler)
  },
  /** Tries to start the core again after it could not be started. */
  retryCore: (): void => ipcRenderer.send('core:retry'),
  pathForFile: (file: File): string => webUtils.getPathForFile(file),
  pickImages: (): Promise<string[]> => ipcRenderer.invoke('dialog:pickImages'),
  /** Asks for a folder, such as where to export several assets. */
  pickFolder: (): Promise<string | null> => ipcRenderer.invoke('dialog:pickFolder'),
  /** Asks for one file of a type, such as a ComfyUI workflow or a benchmark suite (`label`, `['json']`). */
  pickFile: (label: string, extensions: string[]): Promise<string | null> => ipcRenderer.invoke('dialog:pickFile', label, extensions),
  /** Asks where to save one file of a format, suggesting `name` plus its extension. */
  pickSavePath: (name: string, ext: string, label: string): Promise<string | null> =>
    ipcRenderer.invoke('dialog:saveFile', name, ext, label),
  showItem: (path: string): Promise<void> => ipcRenderer.invoke('shell:showItem', path),
  /** Opens a benchmark's HTML report in the default browser. */
  openBenchReport: (id: string): Promise<void> => ipcRenderer.invoke('bench:openReport', id),
  openConfig: (): Promise<void> => ipcRenderer.invoke('settings:openConfig'),
  openDataFolder: (): Promise<void> => ipcRenderer.invoke('settings:openData'),
  copyText: (text: string): Promise<void> => ipcRenderer.invoke('clipboard:write', text),
  /** Readies an asset's GLB to drag out of the window; icon is a PNG to show under the cursor. */
  prepareDrag: (id: string, icon?: ArrayBuffer): Promise<void> => ipcRenderer.invoke('drag:prepare', id, icon),
  /** Drags these assets' GLBs out of the window; call it from a dragstart handler. */
  startDrag: (ids: string[]): void => ipcRenderer.send('drag:start', ids),
}

export type Bridge = typeof bridge

contextBridge.exposeInMainWorld('forja', bridge)
