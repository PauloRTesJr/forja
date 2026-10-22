import { useQuery } from '@tanstack/react-query'
import { useState } from 'react'
import { api, artifactURL, type Asset, type Job } from '../lib/api'
import { keys } from '../lib/events'
import { count, relative, seconds, stageLabel } from '../lib/format'
import { transition, useNavigate } from '../lib/nav'
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
  const a = asset.data

  return (
    <div className="screen" style={{ display: 'flex', flexDirection: 'column' }}>
      <div className="screen-head">
        <div>
          <button className="btn ghost" style={{ marginLeft: -14 }} onClick={() => transition(() => navigate({ screen: 'library' }))}>
            ← Library
          </button>
          <h1>{a?.name ?? ' '}</h1>
        </div>
        {a?.export_path && (
          <button className="btn" onClick={() => window.forja.showItem(a.export_path!)}>
            Show in folder
          </button>
        )}
      </div>
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
              ].map(([k, v], i) => (
                <div className="stat" key={k} style={{ '--i': i } as React.CSSProperties}>
                  <b>{v}</b>
                  <span>{k}</span>
                </div>
              ))
            ) : (
              [0, 1, 2, 3].map((i) => <div key={i} className="skeleton" style={{ height: 58 }} />)
            )}
          </div>
          {a && (
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
          )}
          {job.data && (
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
          )}
        </aside>
      </div>
    </div>
  )
}
