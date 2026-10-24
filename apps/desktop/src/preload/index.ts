// The only bridge between the sandboxed renderer and Electron. Keep it small and typed.
import { contextBridge, ipcRenderer, webUtils, type IpcRendererEvent } from 'electron'

type Listener<T> = (value: T) => void

const bridge = {
  platform: process.platform,
  onEvent(fn: Listener<{ seq: number; type: string; data: unknown }>): () => void {
    const handler = (_: IpcRendererEvent, ev: { seq: number; type: string; data: unknown }): void => fn(ev)
    ipcRenderer.on('core:event', handler)
    return () => ipcRenderer.off('core:event', handler)
  },
  onStatus(fn: Listener<boolean>): () => void {
    const handler = (_: IpcRendererEvent, ok: boolean): void => fn(ok)
    ipcRenderer.on('core:status', handler)
    return () => ipcRenderer.off('core:status', handler)
  },
  pathForFile: (file: File): string => webUtils.getPathForFile(file),
  pickFolder: (): Promise<string | null> => ipcRenderer.invoke('dialog:pickFolder'),
  pickImages: (): Promise<string[]> => ipcRenderer.invoke('dialog:pickImages'),
  /** Asks where to save one file of a format, suggesting `name` plus its extension. */
  pickSavePath: (name: string, ext: string, label: string): Promise<string | null> =>
    ipcRenderer.invoke('dialog:saveFile', name, ext, label),
  showItem: (path: string): Promise<void> => ipcRenderer.invoke('shell:showItem', path),
  openConfig: (): Promise<void> => ipcRenderer.invoke('settings:openConfig'),
  openDataFolder: (): Promise<void> => ipcRenderer.invoke('settings:openData'),
  copyText: (text: string): Promise<void> => ipcRenderer.invoke('clipboard:write', text),
}

export type Bridge = typeof bridge

contextBridge.exposeInMainWorld('forja', bridge)
