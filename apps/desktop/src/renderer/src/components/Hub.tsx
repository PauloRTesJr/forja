import { useInfiniteQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { useEffect, useState, type FormEvent, type ReactNode } from 'react'
import { addHubModel, connectHub, searchCatalog, type CatalogModel } from '../lib/api'
import { keys } from '../lib/events'
import { money } from '../lib/format'
import { useToast } from '../lib/toast'
import { D20 } from './icons'

function Dialog({ label, onClose, children }: { label: string; onClose: () => void; children: ReactNode }) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent): void => {
      if (e.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose])
  return (
    <div className="dialog-backdrop" onClick={onClose}>
      <div className="dialog" role="dialog" aria-modal aria-label={label} onClick={(e) => e.stopPropagation()}>
        {children}
      </div>
    </div>
  )
}

/** Connects fal.ai with an API key, which is tested and then kept in the keychain. */
export function ConnectHub({ onClose }: { onClose: () => void }) {
  const toast = useToast()
  const [key, setKey] = useState('')
  const connect = useMutation({
    mutationFn: () => connectHub('fal', key.trim()),
    onSuccess: () => {
      toast('fal.ai connected: its recommended models are on the Models screen')
      onClose()
    },
  })
  const submit = (e: FormEvent): void => {
    e.preventDefault()
    connect.mutate()
  }
  return (
    <Dialog label="Connect fal.ai" onClose={onClose}>
      <form onSubmit={submit} className="dialog-form">
        <h2>Connect fal.ai</h2>
        <p className="muted">
          One key reaches hosted 3D models such as Hunyuan3D 3.1 Pro, TRELLIS 2, Tripo, Meshy and Rodin. They are paid per
          generation, run only when you pick them, and receive your reference image.
        </p>
        <label className="field">
          <span>
            API key ·{' '}
            <a href="https://fal.ai/dashboard/keys" target="_blank" rel="noreferrer">
              create one in your fal.ai dashboard
            </a>
          </span>
          <input className="input" type="password" autoComplete="off" value={key} onChange={(e) => setKey(e.target.value)} autoFocus required />
        </label>
        {connect.error && <div className="error">{(connect.error as Error).message}</div>}
        <p className="muted small">The key is checked with fal.ai, then kept in this computer's keychain, never in a file.</p>
        <div className="row dialog-actions">
          <button type="button" className="btn ghost" onClick={onClose}>
            Cancel
          </button>
          <button className="btn primary" disabled={connect.isPending || !key.trim()}>
            {connect.isPending ? 'Checking the key…' : 'Connect'}
          </button>
        </div>
      </form>
    </Dialog>
  )
}

const doesLabel = (tasks: string[]): string =>
  tasks.includes('shape') && tasks.includes('texture') ? 'Shape and texture' : tasks.includes('texture') ? 'Texture' : tasks.join(', ')

/** Browses a hub's image-to-3D catalog and adds models Forja can call. */
export function Catalog({ provider, label, onClose }: { provider: string; label: string; onClose: () => void }) {
  const toast = useToast()
  const qc = useQueryClient()
  const [text, setText] = useState('')
  const [q, setQ] = useState('')
  const pages = useInfiniteQuery({
    queryKey: keys.catalog(provider, q),
    queryFn: ({ pageParam }) => searchCatalog(provider, q, pageParam || undefined),
    initialPageParam: '',
    getNextPageParam: (last) => last.next_cursor || undefined,
  })
  const add = useMutation({
    mutationFn: (m: CatalogModel) => addHubModel(provider, m.id),
    onSuccess: (_, m) => {
      toast(`${m.name} added`)
      void qc.invalidateQueries({ queryKey: ['catalog', provider] })
    },
    onError: (e: Error) => toast(e.message, true),
  })
  const models = pages.data?.pages.flatMap((p) => p.models) ?? []
  return (
    <Dialog label={`Add models from ${label}`} onClose={onClose}>
      <h2>Add models from {label}</h2>
      <form
        className="row"
        onSubmit={(e) => {
          e.preventDefault()
          setQ(text.trim())
        }}
      >
        <input className="input grow" placeholder="Search: trellis, hunyuan, low poly…" value={text} onChange={(e) => setText(e.target.value)} />
        <button className="btn">Search</button>
      </form>
      {pages.isPending && <div className="skeleton" style={{ height: 160 }} />}
      {pages.error && <div className="error">{(pages.error as Error).message}</div>}
      <div className="catalog">
        {models.map((m) => (
          <div key={m.id} className="catalog-item">
            {m.thumbnail ? (
              <img src={m.thumbnail} alt="" loading="lazy" referrerPolicy="no-referrer" />
            ) : (
              <span className="catalog-thumb">
                <D20 size={40} />
              </span>
            )}
            <div>
              <strong>{m.name}</strong>
              <div className="chips">
                {m.recommended && <span className="chip recommended">Recommended</span>}
                {!m.problem && <span className="chip">{doesLabel(m.tasks)}</span>}
                <span className="chip paid">{money(m.price)}</span>
              </div>
              <p className={m.problem ? 'problem' : ''}>{m.problem ? `Forja cannot use it: ${m.problem}.` : m.description}</p>
            </div>
            <button className="btn" disabled={m.added || !!m.problem || add.isPending} onClick={() => add.mutate(m)}>
              {m.added ? 'Added' : 'Add'}
            </button>
          </div>
        ))}
        {models.length === 0 && pages.isSuccess && <p className="muted">Nothing matches.</p>}
      </div>
      <div className="row dialog-actions">
        {pages.hasNextPage && (
          <button className="btn ghost" onClick={() => void pages.fetchNextPage()} disabled={pages.isFetchingNextPage}>
            {pages.isFetchingNextPage ? 'Loading…' : 'More'}
          </button>
        )}
        <button className="btn ghost" onClick={onClose}>
          Done
        </button>
      </div>
    </Dialog>
  )
}
