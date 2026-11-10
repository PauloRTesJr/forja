import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useEffect, useRef, useState, type ReactNode } from 'react'
import { ConfirmDelete } from '../components/ConfirmDelete'
import { ErrorBlock, Pill } from '../components/Controls'
import { Empty } from '../components/Empty'
import { ExportMenu } from '../components/ExportMenu'
import { BackIcon, ChevronIcon, DragHandleIcon, FolderIcon, NoResultsIcon, Open3DIcon, RenameIcon } from '../components/icons'
import { MenuItem, MoreMenu } from '../components/Menu'
import { StageList } from '../components/StageTrack'
import { SectionHead } from '../components/ScreenHead'
import { api, ApiError, artifactURL, deleteAsset, fetchExportFormats, fetchPresets, openInBlender, rigOf, type Asset, type Job } from '../lib/api'
import { assetCountKey, lodCount, nameProblem, renameAsset, statNumber, withAsset, withoutAssets } from '../lib/assets'
import { dragOut, readyDrag } from '../lib/drag'
import { keys } from '../lib/events'
import { useExports } from '../lib/exports'
import { count, relative, seconds, skeletonName } from '../lib/format'
import { presetName } from '../lib/jobs'
import { transition, useNavigate } from '../lib/nav'
import { useToast } from '../lib/toast'
import type { MeshStats } from '../viewer/scene'
import { remakeThumbnail } from '../viewer/thumbnails'
import { ViewerView } from '../viewer/ViewerView'

export function AssetDetail({ id }: { id: string }) {
  const navigate = useNavigate()
  const qc = useQueryClient()
  const toast = useToast()
  const asset = useQuery({ queryKey: keys.asset(id), queryFn: ({ signal }) => api<Asset>('GET', `/v1/assets/${id}`, undefined, { signal }) })
  const a = asset.data
  const job = useQuery({
    queryKey: ['job', a?.job_id],
    queryFn: ({ signal }) => api<Job>('GET', `/v1/jobs/${a!.job_id}`, undefined, { signal }),
    enabled: !!a,
  })
  const presets = useQuery({ queryKey: keys.presets, queryFn: () => fetchPresets() })
  const formats = useQuery({ queryKey: keys.exportFormats, queryFn: fetchExportFormats, staleTime: 30_000 })
  const { last } = useExports()
  const [stats, setStats] = useState<MeshStats | null>(null)
  const [confirming, setConfirming] = useState(false)
  const [renaming, setRenaming] = useState(false)
  const [stagesOpen, setStagesOpen] = useState(false)
  // The viewer reads the file it shows; the core's stats stand in until it has.
  const rig = stats ? (stats.bones > 0 ? { bones: stats.bones, skeleton: stats.skeleton } : undefined) : a && rigOf(a)
  const back = (): void => transition(() => navigate({ screen: 'library' }))
  useEffect(() => {
    if (a) readyDrag(a)
  }, [a])

  const blenderProblem = formats.data && !formats.data.blender ? (formats.data.blender_problem ?? 'Blender was not found.') : undefined
  const blender = useMutation({
    mutationFn: () => openInBlender(id),
    onSuccess: (b) => toast(`Opening in Blender ${b.version}…`),
    onError: (e: Error) => toast(e.message, true),
  })
  const remove = useMutation({
    mutationFn: () => deleteAsset(id),
    onSuccess: () => {
      qc.setQueriesData({ queryKey: ['assets'] }, (cache) => withoutAssets(cache, new Set([id])))
      void qc.invalidateQueries({ queryKey: assetCountKey })
      toast(`Deleted ${a?.name ?? 'the asset'}`)
      back()
    },
    onError: (e: Error) => toast(e.message, true),
  })
  const rename = useMutation({
    mutationFn: (name: string) => renameAsset(id, name.trim()),
    onSuccess: (updated) => {
      qc.setQueryData(keys.asset(id), updated)
      qc.setQueriesData({ queryKey: ['assets'] }, (cache) => withAsset(cache, updated))
      // A list sorted by name has the asset elsewhere now; it is read again when the library shows.
      void qc.invalidateQueries({ queryKey: ['assets'], refetchType: 'none' })
      setRenaming(false)
    },
  })

  // Deleted from elsewhere (the command line, another window): say so. Deleted from here, the
  // screen is already on its way back to the library.
  if (asset.error instanceof ApiError && asset.error.status === 404 && remove.isIdle) {
    return (
      <div className="screen asset">
        <BackButton onClick={back} />
        <Empty
          icon={NoResultsIcon}
          title="This asset was deleted"
          action={
            <button className="btn" onClick={back}>
              Back to Library
            </button>
          }
        >
          Files exported from it are still where you saved them.
        </Empty>
      </div>
    )
  }
  if (asset.isError) {
    return (
      <div className="screen asset">
        <BackButton onClick={back} />
        <ErrorBlock role="alert">This asset could not be loaded. {asset.error.message}</ErrorBlock>
        <div className="asset-error-actions">
          <button className="btn" onClick={() => void asset.refetch()}>
            Try again
          </button>
          <button className="btn ghost" onClick={back}>
            Back to Library
          </button>
        </div>
      </div>
    )
  }

  const exported = a ? (last.get(a.id) ?? (a.export_path ? { file: a.export_path, format: 'Project export', at: 0 } : undefined)) : undefined
  const tris = a ? statNumber(a, 'triangles') : undefined
  const lods = a ? lodCount(a) : undefined
  const lodLine =
    lods === undefined
      ? undefined
      : stats && stats.lodTriangles.length > 1
        ? `${lods} (${stats.lodTriangles.map(count).join(' / ')})`
        : String(lods)
  const textures = stats ? (stats.maps.length ? stats.maps : [{ name: 'None', size: '' }]) : undefined
  const texLine = textures?.length
    ? textures[0]!.size
      ? `${textures.length === 1 ? '1 map' : `${textures.length} maps`} · ${stats!.textures[0] ?? textures[0]!.size}`
      : 'None'
    : undefined

  return (
    <div className="screen asset">
      <header className="asset-head">
        <BackButton onClick={back} />
        <AssetTitle
          name={a?.name}
          editing={renaming}
          saving={rename.isPending}
          error={rename.error?.message}
          onEdit={() => {
            rename.reset()
            setRenaming(true)
          }}
          onSave={(name) => rename.mutate(name)}
          onCancel={() => {
            rename.reset()
            setRenaming(false)
          }}
        />
        {rig && (
          <Pill title={`${rig.bones} bones${rig.skeleton === 'humanoid' ? ', named for humanoid retargeting' : ''}`}>Rigged</Pill>
        )}
        <div className="asset-actions">
          <ExportMenu assets={a ? [a] : []} primary />
          <button
            className="btn"
            aria-label="Open in Blender"
            title={blenderProblem ? `${blenderProblem} Set the Blender path in Settings.` : 'Open this asset in Blender'}
            onClick={() => blender.mutate()}
            disabled={!a || blender.isPending || !!blenderProblem}
          >
            <Open3DIcon />
            <span className="btn-label">Open in Blender</span>
          </button>
          {a && (
            <button
              className="btn drag-out"
              draggable
              aria-label="Drag to another app"
              title="Drag this asset into Blender, Unity or a folder. Without a mouse, use Export."
              onPointerEnter={() => readyDrag(a)}
              onPointerDown={() => readyDrag(a)}
              onDragStart={(e) => dragOut(e, [a.id])}
              onClick={() => toast('Drag this button into Blender, Unity or a folder. Export saves a file without dragging.')}
            >
              <DragHandleIcon />
              <span className="btn-label">Drag to app</span>
            </button>
          )}
          <MoreMenu label="More actions">
            {(menu) => (
              <>
                <MenuItem menu={menu} onClick={() => setRenaming(true)} disabled={!a}>
                  Rename
                </MenuItem>
                <MenuItem menu={menu} disabled={!exported} onClick={() => exported && void window.forja.showItem(exported.file)}>
                  Show last export in folder
                </MenuItem>
                <MenuItem
                  menu={menu}
                  disabled={!a}
                  onClick={() => a && remakeThumbnail(a, (u) => qc.setQueriesData({ queryKey: ['assets'] }, (cache) => withAsset(cache, u)))}
                >
                  Refresh thumbnail
                </MenuItem>
                <MenuItem menu={menu} danger disabled={!a || remove.isPending} onClick={() => setConfirming(true)}>
                  Delete
                </MenuItem>
              </>
            )}
          </MoreMenu>
        </div>
      </header>
      {confirming && a && (
        <ConfirmDelete
          names={[a.name]}
          onConfirm={() => {
            setConfirming(false)
            remove.mutate()
          }}
          onClose={() => setConfirming(false)}
        />
      )}
      <div className="asset-body">
        <ViewerView
          url={a ? artifactURL(a.glb) : undefined}
          name={a?.name}
          rigged={!!rig}
          placeholder={a?.thumbnail ? artifactURL(a.thumbnail, 'image/webp') : undefined}
          onStats={setStats}
        />
        <aside className="inspector" aria-label="Properties">
          <section>
            <SectionHead>Mesh</SectionHead>
            <dl className="kv asset-kv">
              <Row label="Triangles" value={tris === undefined ? undefined : count(tris)} wait={!a} />
              <Row label="Vertices" value={a ? fmt(statNumber(a, 'vertices')) : undefined} wait={!a} />
              <Row label="Materials" value={a ? fmt(statNumber(a, 'materials'), String) : undefined} wait={!a} />
              <Row label="LODs" value={lodLine} wait={!a} />
              <Row label="Size" value={stats ? stats.size.map((v) => v.toFixed(2)).join(' × ') + ' m' : undefined} wait />
              <Row label="Textures" value={texLine} title={stats?.maps.map((m) => `${m.name} ${m.size}`).join('\n')} wait />
            </dl>
          </section>
          {rig && (
            <section>
              <SectionHead>Rig</SectionHead>
              <dl className="kv asset-kv">
                <Row label="Bones" value={count(rig.bones)} />
                <Row label="Skeleton" value={skeletonName(rig)} />
              </dl>
            </section>
          )}
          <section>
            <SectionHead>Details</SectionHead>
            <dl className="kv asset-kv">
              <Row label="Preset" value={a ? presetName(a.preset, presets.data) : undefined} wait={!a} />
              <Row label="Created" value={a ? relative(a.created_at) : undefined} title={a ? new Date(a.created_at).toLocaleString() : undefined} wait={!a} />
              <Row
                label="Last export"
                wait={!a}
                value={
                  a && (
                    <span className="asset-export">
                      {exported ? `${exported.format}${exported.at ? ` · ${relative(new Date(exported.at).toISOString())}` : ''}` : 'Not exported yet'}
                      {exported && (
                        <button className="btn ghost icon sm" aria-label="Show in folder" title="Show in folder" onClick={() => void window.forja.showItem(exported.file)}>
                          <FolderIcon />
                        </button>
                      )}
                    </span>
                  )
                }
              />
              {a && (
                <Row
                  label="Source image"
                  value={
                    <span className="thumb">
                      <img src={artifactURL(a.input)} alt="" onError={(e) => (e.currentTarget.style.visibility = 'hidden')} />
                    </span>
                  }
                />
              )}
            </dl>
          </section>
          {job.data && job.data.stages.length > 0 && (
            <section>
              <button className="disclosure" aria-expanded={stagesOpen} onClick={() => setStagesOpen(!stagesOpen)}>
                <ChevronIcon size={14} />
                {job.data.stages.length} stages · {seconds(job.data.stages.reduce((n, s) => n + (s.seconds ?? 0), 0))}
              </button>
              {stagesOpen && <StageList stages={job.data.stages} />}
            </section>
          )}
        </aside>
      </div>
    </div>
  )
}

const fmt = (n: number | undefined, f: (n: number) => string = count): string | undefined => (n === undefined ? undefined : f(n))

function BackButton({ onClick }: { onClick: () => void }) {
  return (
    <button className="back" aria-label="Back to Library" onClick={onClick}>
      <BackIcon />
      Library
    </button>
  )
}

/** One label and value; `wait` shows a skeleton of the final size until there is a value. */
function Row({ label, value, wait, title }: { label: string; value?: ReactNode; wait?: boolean; title?: string }) {
  return (
    <div className="kv-row">
      <dt>{label}</dt>
      <dd className="num" title={title}>
        {value ?? (wait ? <span className="skeleton asset-value-wait" /> : '–')}
      </dd>
    </div>
  )
}

/** The asset's name as the page's heading. Click it, or the pencil, to type another; Enter saves, Escape cancels. */
function AssetTitle({
  name,
  editing,
  saving,
  error,
  onEdit,
  onSave,
  onCancel,
}: {
  name: string | undefined
  editing: boolean
  saving: boolean
  error?: string
  onEdit: () => void
  onSave: (name: string) => void
  onCancel: () => void
}) {
  const [draft, setDraft] = useState('')
  const [problem, setProblem] = useState<string>()
  const input = useRef<HTMLInputElement>(null)
  const pencil = useRef<HTMLButtonElement>(null)
  const wasEditing = useRef(false)
  const [seeded, setSeeded] = useState(false)
  if (editing && !seeded) {
    setSeeded(true)
    setDraft(name ?? '')
    setProblem(undefined)
  } else if (!editing && seeded) {
    setSeeded(false)
  }
  useEffect(() => {
    if (editing) {
      input.current?.focus()
      input.current?.select()
    } else if (wasEditing.current) pencil.current?.focus()
    wasEditing.current = editing
  }, [editing])

  const submit = (): void => {
    if (saving) return
    const trimmed = draft.trim()
    if (trimmed === name) return onCancel()
    const bad = nameProblem(draft)
    if (bad) return setProblem(bad)
    onSave(draft)
  }
  const shown = problem ?? error
  if (editing) {
    return (
      <div className="asset-title editing">
        <input
          ref={input}
          className="input"
          aria-label="Asset name"
          aria-invalid={!!shown}
          aria-describedby="asset-name-problem"
          value={draft}
          disabled={saving}
          onChange={(e) => {
            setDraft(e.target.value)
            setProblem(undefined)
          }}
          onKeyDown={(e) => {
            if (e.key === 'Enter') submit()
            else if (e.key === 'Escape') {
              e.stopPropagation()
              onCancel()
            }
          }}
          onBlur={() => {
            // Leaving the field keeps what was typed when it is a good name, and drops it otherwise.
            if (saving || shown) return
            if (draft.trim() === name || nameProblem(draft)) onCancel()
            else onSave(draft)
          }}
        />
        <p id="asset-name-problem" className="asset-title-problem" role="alert">
          {shown}
        </p>
      </div>
    )
  }
  return (
    <div className="asset-title">
      <h1>
        {name === undefined ? (
          <span className="skeleton asset-title-wait" />
        ) : (
          <button className="asset-name" title={`${name}. Click to rename.`} onClick={onEdit}>
            {name}
          </button>
        )}
      </h1>
      {name !== undefined && (
        <button ref={pencil} className="btn ghost icon sm" aria-label="Rename" title="Rename" onClick={onEdit}>
          <RenameIcon />
        </button>
      )}
    </div>
  )
}
