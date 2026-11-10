import { useQuery } from '@tanstack/react-query'
import { useEffect, useRef, useState, type KeyboardEvent } from 'react'
import { fetchExportFormats, type Asset, type ExportFormat } from '../lib/api'
import { exportTo, withExtension } from '../lib/assets'
import { keys } from '../lib/events'
import { exportLabel, exportReport, exportsStore, useExports } from '../lib/exports'
import { transition, useNavigate } from '../lib/nav'
import { fileStem } from '../lib/selection'
import { useToast } from '../lib/toast'
import { ChevronIcon, ExportIcon } from './icons'

const HINT = 'One asset is saved as a file; several go into a folder you choose.'

/**
 * The Export button and its menu of formats. Picking one asks where to save (a file for one
 * asset, a folder for several) and exports there. The menu asks the core which formats this
 * computer can write each time it opens, so a Blender installed or removed meanwhile shows.
 * It opens under the button's right edge; `up` opens it above, `left` under the left edge.
 */
export function ExportMenu({
  assets,
  up = false,
  left = false,
  small = false,
  primary = false,
}: {
  assets: Asset[]
  up?: boolean
  left?: boolean
  small?: boolean
  primary?: boolean
}) {
  const toast = useToast()
  const navigate = useNavigate()
  const [open, setOpen] = useState(false)
  const [picking, setPicking] = useState(false)
  const [active, setActive] = useState<string | null>(null)
  const anchor = useRef<HTMLDivElement>(null)
  const button = useRef<HTMLButtonElement>(null)
  const { running } = useExports()
  const formats = useQuery({ queryKey: keys.exportFormats, queryFn: fetchExportFormats, enabled: open, staleTime: 0 })

  useEffect(() => {
    if (!open) return
    const away = (e: MouseEvent): void => {
      if (!anchor.current?.contains(e.target as Node)) setOpen(false)
    }
    document.addEventListener('mousedown', away)
    return () => document.removeEventListener('mousedown', away)
  }, [open])

  // The first format takes the focus once there is one: the list may arrive after the menu opens.
  const listed = formats.data?.formats.length ?? 0
  useEffect(() => {
    if (open && listed > 0 && !anchor.current?.querySelector('.menu')?.contains(document.activeElement)) {
      anchor.current?.querySelector<HTMLButtonElement>('.menu-item:not(:disabled)')?.focus()
    }
  }, [open, listed])

  const close = (refocus: boolean): void => {
    setOpen(false)
    setActive(null)
    if (refocus) button.current?.focus()
  }

  const onKey = (e: KeyboardEvent): void => {
    if (!open) return
    if (e.key === 'Escape') {
      e.stopPropagation()
      close(true)
      return
    }
    if (e.key === 'Tab') {
      close(false)
      return
    }
    if (!['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(e.key)) return
    e.preventDefault()
    const items = [...(anchor.current?.querySelectorAll<HTMLButtonElement>('.menu-item:not(:disabled)') ?? [])]
    if (items.length === 0) return
    const at = items.indexOf(document.activeElement as HTMLButtonElement)
    const to =
      e.key === 'Home' ? 0 : e.key === 'End' ? items.length - 1 : (at + (e.key === 'ArrowDown' ? 1 : items.length - 1)) % items.length
    items[to]?.focus()
  }

  const run = async (f: ExportFormat): Promise<void> => {
    close(true)
    const chosen = assets
    const one = chosen.length === 1
    setPicking(true)
    let picked: string | null
    try {
      picked = one ? await window.forja.pickSavePath(fileStem(chosen[0]!.name), f.ext, f.name) : await window.forja.pickFolder()
    } finally {
      setPicking(false)
    }
    if (!picked) return
    const ctrl = new AbortController()
    exportsStore.start({ label: exportLabel(chosen.map((a) => a.name), f), cancel: () => ctrl.abort() })
    try {
      const target = one ? { file: withExtension(picked, f.ext) } : { folder: picked }
      const res = await exportTo(chosen.map((a) => a.id), f.id, target, ctrl.signal)
      exportsStore.finish(res, f)
      const { text, bad, file } = exportReport(res, f)
      toast(text, bad, file ? { label: 'Show in folder', run: () => void window.forja.showItem(file) } : undefined)
    } catch (err) {
      exportsStore.finish()
      if (ctrl.signal.aborted) toast('Export cancelled')
      else toast(`The ${f.name} export failed: ${(err as Error).message}`, true)
    }
  }

  const all = formats.data?.formats ?? []
  const direct = all.filter((f) => !f.blender)
  const converted = all.filter((f) => f.blender)
  const blender = formats.data?.blender
  const item = (f: ExportFormat) => (
    <button
      key={f.id}
      role="menuitem"
      className="menu-item export-format"
      disabled={!f.available}
      onClick={() => void run(f)}
      onFocus={() => setActive(f.id)}
      onPointerEnter={() => setActive(f.id)}
    >
      <span>{f.name}</span>
      <span className="mono">{f.ext}</span>
    </button>
  )
  const busy = picking || running !== null
  return (
    <div className="menu-anchor" ref={anchor} onKeyDown={onKey}>
      <button
        ref={button}
        className={`btn ${small ? 'sm' : ''} ${primary ? 'primary' : ''}`}
        aria-haspopup="menu"
        aria-expanded={open}
        title={running?.label}
        disabled={busy || assets.length === 0}
        onClick={() => (open ? close(false) : setOpen(true))}
      >
        <ExportIcon />
        {running ? 'Exporting…' : 'Export'}
        <ChevronIcon size={14} />
        {running && <span className="btn-bar" role="progressbar" aria-label={running.label} />}
      </button>
      <div className={`menu export-menu ${up ? 'up' : ''} ${left ? 'left' : ''}`} role="menu" aria-label="Export as" data-open={open} inert={!open}>
        {!formats.data && formats.isPending && [0, 1, 2].map((i) => <div key={i} className="skeleton export-skeleton" />)}
        {!formats.data && formats.isError && <p className="menu-note">{formats.error.message}</p>}
        {converted.length > 0 && !blender && (
          <p className="export-missing" role="note">
            <strong>Most formats need Blender 4.2 or newer.</strong>
            {formats.data?.blender_problem ?? 'Blender was not found.'}{' '}
            <button
              className="text-btn"
              onClick={() => {
                close(false)
                transition(() => navigate({ screen: 'settings' }))
              }}
            >
              Set the Blender path in Settings
            </button>
          </p>
        )}
        {direct.map(item)}
        {converted.length > 0 && (
          <>
            <div className="export-group" role="presentation">
              {blender ? `Converted by Blender ${blender.version}` : 'Needs Blender'}
            </div>
            {converted.map(item)}
          </>
        )}
        {all.length > 0 && <p className="export-caption">{all.find((f) => f.id === active)?.for ?? HINT}</p>}
      </div>
    </div>
  )
}
