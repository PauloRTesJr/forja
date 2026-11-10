// The export that is running, wherever it was started, and where this session's exports went.
// An export outlives the menu that started it (a Blender conversion takes a while, and the
// selection toolbar closes when the selection is cleared), so its state is kept here.
import { useSyncExternalStore } from 'react'
import type { ExportFormat, ExportResult } from './api'
import { baseName } from './selection'

export interface RunningExport {
  /** "Exporting 12 assets as FBX" */
  label: string
  cancel: () => void
}

export interface LastExport {
  file: string
  format: string
  at: number
}

interface State {
  running: RunningExport | null
  /** By asset id: the main file its last export wrote, since the app started. */
  last: ReadonlyMap<string, LastExport>
}

let state: State = { running: null, last: new Map() }
const listeners = new Set<() => void>()
const set = (next: State): void => {
  state = next
  for (const fn of listeners) fn()
}

export const exportsStore = {
  get: (): State => state,
  subscribe(fn: () => void): () => void {
    listeners.add(fn)
    return () => listeners.delete(fn)
  },
  start(running: RunningExport): void {
    set({ ...state, running })
  },
  /** The export ended, however it did; `res` records where each asset's file went. */
  finish(res?: ExportResult, format?: ExportFormat, now = Date.now()): void {
    const last = new Map(state.last)
    for (const a of res?.assets ?? []) {
      const file = a.files?.[0]
      if (file && !a.error) last.set(a.asset, { file, format: format?.name ?? res!.format, at: now })
    }
    set({ running: null, last })
  },
  reset(): void {
    set({ running: null, last: new Map() })
  },
}

export function useExports(): State {
  return useSyncExternalStore(exportsStore.subscribe, exportsStore.get)
}

/** "Exporting red-barrel as FBX", "Exporting 12 assets as GLB". */
export function exportLabel(names: string[], f: { name: string }): string {
  return `Exporting ${names.length === 1 ? names[0] : `${names.length} assets`} as ${f.name}`
}

export interface ExportReport {
  text: string
  bad: boolean
  /** A file to show in its folder. */
  file?: string
}

/** Says how an export went: the file it saved, or how many assets went where, or what failed. */
export function exportReport(res: ExportResult, f: { name: string }): ExportReport {
  const failed = res.assets.filter((a) => a.error)
  const file = res.assets.find((a) => a.files?.length && !a.error)?.files?.[0]
  const n = res.assets.length
  if (failed.length > 0) {
    const what = n === 1 ? `The ${f.name} export failed` : `${failed.length} of ${n} assets could not be exported as ${f.name}`
    return { text: `${what}: ${failed[0]!.error}`, bad: true, file }
  }
  if (n === 1 && file) return { text: `Saved ${baseName(file)}`, bad: false, file }
  return { text: `Exported ${n} assets as ${f.name}`, bad: false, file }
}
