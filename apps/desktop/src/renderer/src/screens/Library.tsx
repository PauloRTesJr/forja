import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useDeferredValue, useEffect, useState } from 'react'
import { Empty } from '../components/Empty'
import { api, artifactURL, type Asset } from '../lib/api'
import { keys } from '../lib/events'
import { count, relative } from '../lib/format'
import { transition, useNavigate } from '../lib/nav'
import { ensureThumbnail } from '../viewer/thumbnails'

export function Library() {
  const [q, setQ] = useState('')
  const query = useDeferredValue(q.trim())
  const qc = useQueryClient()
  const assets = useQuery({
    queryKey: keys.assets(query),
    queryFn: () => api<Asset[]>('GET', `/v1/assets?limit=200&q=${encodeURIComponent(query)}`),
    placeholderData: (prev) => prev,
  })

  useEffect(() => {
    for (const a of assets.data ?? []) {
      ensureThumbnail(a, () => void qc.invalidateQueries({ queryKey: ['assets'] }))
    }
  }, [assets.data, qc])

  return (
    <div className="screen">
      <div className="screen-head">
        <div>
          <h1>Library</h1>
          <p className="sub">{assets.data ? `${assets.data.length} assets` : ' '}</p>
        </div>
        <input className="search" placeholder="Search by name or preset" value={q} onChange={(e) => setQ(e.target.value)} />
      </div>
      {assets.data?.length === 0 && <Empty title={query ? 'No matches' : 'No assets yet'}>Finished jobs land here.</Empty>}
      <div className="grid">
        {assets.isPending && [0, 1, 2, 3, 4, 5].map((i) => <div key={i} className="skeleton" style={{ aspectRatio: '1 / 1.2' }} />)}
        {assets.data?.map((a, i) => <AssetCard key={a.id} asset={a} index={i} />)}
      </div>
    </div>
  )
}

function AssetCard({ asset, index }: { asset: Asset; index: number }) {
  const navigate = useNavigate()
  const tris = asset.stats?.triangles
  return (
    <button
      className="card enter"
      style={{ '--i': Math.min(index, 12) } as React.CSSProperties}
      onClick={() => transition(() => navigate({ screen: 'asset', id: asset.id }))}
    >
      <div className="card-img">
        {asset.thumbnail ? (
          <img src={artifactURL(asset.thumbnail, 'image/webp')} alt="" decoding="async" loading="lazy" />
        ) : (
          <div className="skeleton" style={{ width: '60%', height: '60%', borderRadius: 12 }} />
        )}
      </div>
      <div className="card-body">
        <strong>{asset.name}</strong>
        <span className="muted">
          {asset.preset}
          {typeof tris === 'number' && ` · ${count(tris)} tris`} · {relative(asset.created_at)}
        </span>
      </div>
    </button>
  )
}
