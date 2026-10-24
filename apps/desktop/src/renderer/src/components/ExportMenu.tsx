import { useMutation, useQuery } from '@tanstack/react-query'
import { useEffect, useRef, useState, type KeyboardEvent } from 'react'
import { exportAssets, fetchExportFormats, type Asset, type ExportFormat, type ExportResult } from '../lib/api'
import { keys } from '../lib/events'
import { baseName, fileStem } from '../lib/selection'
import { useToast } from '../lib/toast'
import { Chevron, ExportArrow } from './icons'

/** Says how an export went: the file it saved, or how many assets went where. */
function report(res: ExportResult, f: ExportFormat): { text: string; bad: boolean; file?: string } {
  const failed = res.assets.filter((a) => a.error)
  const file = res.assets.find((a) => a.files?.length)?.files?.[0]
  if (failed.length > 0) {
    const n = res.assets.length
    return { text: `${failed.length} of ${n} could not be exported as ${f.name}: ${failed[0]!.error}`, bad: true, file }
  }
  if (res.assets.length === 1 && file) return { text: `Saved ${baseName(file)}`, bad: false, file }
  return { text: `Exported ${res.assets.length} assets as ${f.name}`, bad: false, file }
}

/**
 * The Export button and its menu of formats. Picking one asks where to save (a file for one
 * asset, a folder for several) and exports there; formats Blender makes wait for Blender.
 */
export function ExportMenu({ assets, up = false }: { assets: Asset[]; up?: boolean }) {
  const toast = useToast()
  const [open, setOpen] = useState(false)
  const anchor = useRef<HTMLDivElement>(null)
  const button = useRef<HTMLButtonElement>(null)
  const formats = useQuery({ queryKey: keys.exportFormats, queryFn: fetchExportFormats, staleTime: 5 * 60_000 })
  const run = useMutation({
    mutationFn: async (f: ExportFormat) => {
      const path =
        assets.length === 1 ? await window.forja.pickSavePath(fileStem(assets[0]!.name), f.ext, f.name) : await window.forja.pickFolder()
      return path ? { res: await exportAssets(assets.map((a) => a.id), f.id, path), f } : null
    },
    onSuccess: (done) => {
      if (!done) return
      const { text, bad, file } = report(done.res, done.f)
      toast(text, bad, file ? { label: 'Show in folder', run: () => void window.forja.showItem(file) } : undefined)
    },
    onError: (e: Error) => toast(e.message, true),
  })

  useEffect(() => {
    if (!open) return
    const away = (e: MouseEvent): void => {
      if (!anchor.current?.contains(e.target as Node)) setOpen(false)
    }
    document.addEventListener('mousedown', away)
    anchor.current?.querySelector<HTMLButtonElement>('.menu-item:not(:disabled)')?.focus()
    return () => document.removeEventListener('mousedown', away)
  }, [open])

  const onKey = (e: KeyboardEvent): void => {
    if (!open) return
    if (e.key === 'Escape') {
      e.stopPropagation()
      setOpen(false)
      button.current?.focus()
      return
    }
    if (e.key !== 'ArrowDown' && e.key !== 'ArrowUp') return
    e.preventDefault()
    const items = [...(anchor.current?.querySelectorAll<HTMLButtonElement>('.menu-item:not(:disabled)') ?? [])]
    const at = items.indexOf(document.activeElement as HTMLButtonElement)
    items[(at + (e.key === 'ArrowDown' ? 1 : items.length - 1)) % items.length]?.focus()
  }

  const pick = (f: ExportFormat): void => {
    setOpen(false)
    run.mutate(f)
  }
  const blenderMissing = formats.data && !formats.data.blender
  return (
    <div className="menu-anchor" ref={anchor} onKeyDown={onKey}>
      <button
        ref={button}
        className="btn"
        aria-haspopup="menu"
        aria-expanded={open}
        disabled={run.isPending || assets.length === 0}
        onClick={() => setOpen(!open)}
      >
        <ExportArrow />
        {run.isPending ? 'Exporting…' : 'Export'}
        <Chevron />
      </button>
      <div className={`menu ${up ? 'up' : ''}`} role="menu" aria-label="Export as" data-open={open} inert={!open}>
        {formats.isPending && [0, 1, 2].map((i) => <div key={i} className="skeleton" style={{ height: 50, margin: 4 }} />)}
        {formats.data?.formats.map((f) => (
          <button key={f.id} role="menuitem" className="menu-item" disabled={!f.available} onClick={() => pick(f)} title={f.problem}>
            <span className="menu-item-head">
              <strong>{f.name}</strong>
              <span className="mono">{f.ext}</span>
            </span>
            <span className="menu-item-help">{f.for}</span>
          </button>
        ))}
        {blenderMissing && <p className="menu-note">{formats.data.blender_problem}</p>}
        {formats.isError && <p className="menu-note">{formats.error.message}</p>}
      </div>
    </div>
  )
}
