import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useDeferredValue, useEffect, useState, useSyncExternalStore } from 'react'
import { Empty } from '../components/Empty'
import { Search as SearchIcon, Sparkle } from '../components/icons'
import { ScreenHead } from '../components/ScreenHead'
import { api, artifactURL, type Asset } from '../lib/api'
import { keys } from '../lib/events'
import { count, relative } from '../lib/format'
import { transition, useNavigate } from '../lib/nav'
import { ensureThumbnail, subscribeThumbnailErrors, thumbnailError, thumbnailErrorsVersion } from '../viewer/thumbnails'

export function Library() {
  const [q, setQ] = useState('')
  const query = useDeferredValue(q.trim())
  const qc = useQueryClient()
  useSyncExternalStore(subscribeThumbnailErrors, thumbnailErrorsVersion)
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
      <ScreenHead eyebrow={['Arcanum III', 'The Grimoire']} title="Library" sub={assets.data ? `${assets.data.length} assets` : ' '}>
        <label className="search">
          <SearchIcon />
          <span className="sr-only">Search assets</span>
          <input type="search" placeholder="Search by name or preset" value={q} onChange={(e) => setQ(e.target.value)} />
        </label>
      </ScreenHead>
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
  const error = asset.thumbnail ? undefined : thumbnailError(asset)
  return (
    <button
      className="card enter"
      style={{ '--i': Math.min(index, 12) } as React.CSSProperties}
      onClick={() => transition(() => navigate({ screen: 'asset', id: asset.id }))}
    >
      <div className="card-img">
        <Sparkle size={11} className="dust a" />
        <Sparkle size={8} className="dust b" />
        {asset.thumbnail ? (
          <img src={artifactURL(asset.thumbnail, 'image/webp')} alt="" decoding="async" loading="lazy" />
        ) : error ? (
          <div className="card-failed" title={error}>
            Could not load this model
          </div>
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
