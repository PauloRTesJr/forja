import { useMutation, useQuery } from '@tanstack/react-query'
import { useEffect, useState } from 'react'
import { ConfirmDelete } from '../components/ConfirmDelete'
import { Empty } from '../components/Empty'
import { ExportMenu } from '../components/ExportMenu'
import { ArrowLeft, Cube, Folder, Grip, Trash } from '../components/icons'
import { SectionHead } from '../components/ScreenHead'
import { api, ApiError, artifactURL, deleteAsset, openInBlender, rigOf, type Asset, type Job, type Rig } from '../lib/api'
import { dragOut, readyDrag } from '../lib/drag'
import { describeNotes } from '../lib/estimate'
import { keys } from '../lib/events'
import { count, relative, seconds, skeletonName, stageLabel } from '../lib/format'
import { transition, useNavigate } from '../lib/nav'
import { useToast } from '../lib/toast'
import type { MeshStats } from '../viewer/scene'
import { ViewerView } from '../viewer/ViewerView'

export function AssetDetail({ id }: { id: string }) {
  const navigate = useNavigate()
  const asset = useQuery({ queryKey: keys.asset(id), queryFn: () => api<Asset>('GET', `/v1/assets/${id}`) })
  const job = useQuery({
    queryKey: ['job', asset.data?.job_id],
    queryFn: () => api<Job>('GET', `/v1/jobs/${asset.data!.job_id}`),
    enabled: !!asset.data,
  })
  const [stats, setStats] = useState<MeshStats | null>(null)
  const [confirming, setConfirming] = useState(false)
  const toast = useToast()
  const a = asset.data
  // The viewer reads the file it shows; the core's stats stand in until it has.
  const rig: Rig | undefined = stats ? (stats.bones > 0 ? { bones: stats.bones, skeleton: stats.skeleton } : undefined) : a && rigOf(a)
  const back = (): void => transition(() => navigate({ screen: 'library' }))
  useEffect(() => {
    if (a) readyDrag(a)
  }, [a])
  const blender = useMutation({
    mutationFn: () => openInBlender(id),
    onSuccess: (b) => toast(`Opening in Blender ${b.version}…`),
    onError: (e: Error) => toast(e.message, true),
  })
  const remove = useMutation({
    mutationFn: () => deleteAsset(id),
    onSuccess: back,
    onError: (e: Error) => toast(e.message, true),
  })

  // Deleted from elsewhere (the command line, another window): say so. Deleted from here, the
  // screen is already on its way back to the library.
  if (asset.error instanceof ApiError && asset.error.status === 404 && remove.isIdle) {
    return (
      <div className="screen">
        <button className="back" onClick={back}>
          <ArrowLeft />
          Library
        </button>
        <Empty title="This asset was deleted">It left the library; anything exported from it stays where it was saved.</Empty>
      </div>
    )
  }

  return (
    <div className="screen" style={{ display: 'flex', flexDirection: 'column' }}>
      <div className="screen-head">
        <div className="screen-title">
          <button className="back" onClick={back}>
            <ArrowLeft />
            Library
          </button>
          <div className="row">
            <h1>{a?.name ?? ' '}</h1>
            {rig && (
              <span className="tag gold rig-tag" title={`${rig.bones} bones${rig.skeleton === 'humanoid' ? ', named for humanoid retargeting' : ''}`}>
                Rigged
              </span>
            )}
          </div>
        </div>
        <div className="row head-actions">
          {a?.export_path && (
            <button className="btn ghost" onClick={() => window.forja.showItem(a.export_path!)}>
              <Folder />
              Show in folder
            </button>
          )}
          <button className="btn" onClick={() => blender.mutate()} disabled={!a || blender.isPending}>
            <Cube />
            Open in Blender
          </button>
          <ExportMenu assets={a ? [a] : []} />
          {a && (
            <span
              className="btn drag-out"
              draggable
              title="Drag the GLB into Blender, Unity or a folder"
              onPointerEnter={() => readyDrag(a)}
              onDragStart={(e) => dragOut(e, [a.id])}
            >
              <Grip />
              Drag out
            </span>
          )}
          <button className="btn danger" onClick={() => setConfirming(true)} disabled={!a || remove.isPending}>
            <Trash />
            Delete
          </button>
        </div>
      </div>
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
      <div className="detail">
        <ViewerView url={a ? artifactURL(a.glb) : undefined} onStats={setStats} />
        <aside className="panel">
          <div className="stats">
            {stats ? (
              [
                ['Triangles', count(stats.triangles)],
                ['Vertices', count(stats.vertices)],
                ['LODs', String(stats.lods + 1)],
                ['Textures', stats.textures.join(', ') || 'none'],
                ['Size (m)', stats.size.map((v) => v.toFixed(2)).join(' × ')],
                ['Materials', String(stats.materials)],
                ...(rig ? [['Bones', count(rig.bones)], ['Skeleton', skeletonName(rig)]] : []),
              ].map(([k, v], i) => (
                <div className={`stat ${k === 'Textures' || k === 'Size (m)' ? 'wide' : ''}`} key={k} style={{ '--i': i } as React.CSSProperties}>
                  <b>{v}</b>
                  <span>{k}</span>
                </div>
              ))
            ) : (
              [0, 1, 2, 3].map((i) => <div key={i} className="skeleton" style={{ height: 58 }} />)
            )}
          </div>
          {a && (
            <section>
              <SectionHead>Details</SectionHead>
              <dl className="kv">
                <dt>Preset</dt>
                <dd>{a.preset}</dd>
                <dt>Created</dt>
                <dd>{relative(a.created_at)}</dd>
                <dt>Exported</dt>
                <dd title={a.export_path}>{a.export_path ?? 'library only'}</dd>
                <dt>File</dt>
                <dd className="mono">{a.glb.slice(7, 19)}…</dd>
              </dl>
            </section>
          )}
          {job.data && (
            <section>
              <SectionHead>Stages</SectionHead>
              <table className="table">
                <thead>
                  <tr>
                    <th>Stage</th>
                    <th>Model</th>
                    <th style={{ textAlign: 'right' }}>Time</th>
                  </tr>
                </thead>
                <tbody>
                  {job.data.stages.map((s, i) => (
                    <tr key={s.id} style={{ animationDelay: `${i * 25}ms` }}>
                      <td>{stageLabel(s.use)}</td>
                      <td className="muted">{s.model ?? 'built-in'}</td>
                      <td style={{ textAlign: 'right' }} className={s.cached ? 'muted' : ''}>
                        {s.cached ? 'cached' : seconds(s.seconds)}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
              {describeNotes(job.data.stages).map((note) => (
                <p key={note} className="job-fallback asset-note">
                  {note}
                </p>
              ))}
            </section>
          )}
        </aside>
      </div>
    </div>
  )
}
