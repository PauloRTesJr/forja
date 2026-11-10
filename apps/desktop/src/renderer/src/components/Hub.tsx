import { useInfiniteQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { useState, type FormEvent } from 'react'
import { addHubModel, connectHub, searchCatalog, thumbnailURL, type CatalogModel } from '../lib/api'
import { keys } from '../lib/events'
import { money } from '../lib/format'
import { useToast } from '../lib/toast'
import { ErrorBlock, Pill } from './Controls'
import { Dialog, DialogActions } from './Dialog'
import { Empty } from './Empty'
import { ImageIcon, NoResultsIcon } from './icons'

/** Connects fal.ai with an API key, which is tested and then kept in the keychain. */
export function ConnectHub({ onClose }: { onClose: () => void }) {
  const toast = useToast()
  const [key, setKey] = useState('')
  const connect = useMutation({
    mutationFn: () => connectHub('fal', key.trim()),
    onSuccess: () => {
      toast('fal.ai connected. Its recommended models are on the Models screen.')
      onClose()
    },
  })
  const submit = (e: FormEvent): void => {
    e.preventDefault()
    if (key.trim() && !connect.isPending) connect.mutate()
  }
  return (
    <Dialog title="Connect fal.ai" keepOpen={key !== ''} onClose={onClose}>
      <form onSubmit={submit} className="dialog-form">
        <p className="muted">
          One key reaches hosted 3D models such as Hunyuan3D 3.1 Pro, TRELLIS 2, Tripo, Meshy and Rodin. They are paid per image, run only when you
          choose one for a step, and receive your reference image.
        </p>
        <label className="field">
          <span>API key</span>
          <input
            className="input"
            type="password"
            autoComplete="off"
            spellCheck={false}
            value={key}
            onChange={(e) => setKey(e.target.value)}
            disabled={connect.isPending}
            aria-describedby="hub-key-help"
            autoFocus
            required
          />
        </label>
        <p className="muted small" id="hub-key-help">
          Create one in your{' '}
          <a href="https://fal.ai/dashboard/keys" target="_blank" rel="noreferrer">
            fal.ai dashboard
          </a>
          . It is checked with fal.ai, then kept in this computer's keychain, never in a file.
        </p>
        {connect.error && <ErrorBlock role="alert">{(connect.error as Error).message}</ErrorBlock>}
        <DialogActions>
          <button type="button" className="btn ghost" onClick={onClose}>
            Cancel
          </button>
          <button className="btn primary" disabled={connect.isPending || !key.trim()}>
            {connect.isPending ? 'Checking the key…' : 'Connect'}
          </button>
        </DialogActions>
      </form>
    </Dialog>
  )
}

function Thumb({ provider, url }: { provider: string; url?: string }) {
  const [failed, setFailed] = useState(false)
  return (
    <span className="thumb">
      {!url || failed ? <ImageIcon size={20} /> : <img src={thumbnailURL(provider, url)} alt="" loading="lazy" onError={() => setFailed(true)} />}
    </span>
  )
}

const doesLabel = (tasks: string[]): string =>
  tasks.includes('shape') && tasks.includes('texture') ? 'Shape and texture' : tasks.includes('texture') ? 'Texture' : tasks.includes('shape') ? 'Shape' : tasks.join(', ')

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
      toast(`${m.name} added. Choose it for a step on the Models screen.`)
      void qc.invalidateQueries({ queryKey: ['catalog', provider] })
      void qc.invalidateQueries({ queryKey: keys.models })
    },
    onError: (e: Error) => toast(e.message, true),
  })
  const models = pages.data?.pages.flatMap((p) => p.models) ?? []
  return (
    <Dialog title={`Add models from ${label}`} size="wide" onClose={onClose}>
      <form
        className="row"
        role="search"
        onSubmit={(e) => {
          e.preventDefault()
          setQ(text.trim())
        }}
      >
        <input
          className="input grow"
          type="search"
          aria-label={`Search ${label}'s catalog`}
          placeholder="Search: trellis, hunyuan, low poly…"
          value={text}
          onChange={(e) => setText(e.target.value)}
          autoFocus
        />
        <button className="btn">Search</button>
      </form>
      <p className="muted small">Every model here is paid per image by {label}. Adding one costs nothing: it is used only once you choose it for a step.</p>
      {pages.isPending && (
        <div className="catalog" aria-busy>
          {[0, 1, 2, 3].map((i) => (
            <div key={i} className="skeleton" style={{ height: 56, margin: '8px 0' }} />
          ))}
        </div>
      )}
      {pages.error && (
        <ErrorBlock>
          <strong className="block">The catalog could not be read.</strong>
          <span className="block">{(pages.error as Error).message}</span>
          <button className="btn sm" onClick={() => void pages.refetch()} disabled={pages.isFetching}>
            Try again
          </button>
        </ErrorBlock>
      )}
      {pages.isSuccess && (
        <ul className="catalog" aria-label="Models found">
          {models.map((m) => {
            const adding = add.isPending && add.variables?.id === m.id
            return (
              <li key={m.id} className={`catalog-item ${m.problem ? 'unusable' : ''}`}>
                <Thumb provider={provider} url={m.thumbnail} />
                <div className="catalog-text">
                  <div className="catalog-name">
                    <strong>{m.name}</strong>
                    {m.recommended && <Pill>Recommended</Pill>}
                    {!m.problem && <Pill>{doesLabel(m.tasks)}</Pill>}
                  </div>
                  <span className="mono dim">{m.id}</span>
                  <p className={m.problem ? 'problem' : ''}>{m.problem ? `Forja cannot use it: ${m.problem}.` : m.description}</p>
                </div>
                <div className="catalog-act">
                  <span className={`num ${m.price ? '' : 'dim'}`}>{m.price ? money(m.price) : 'No price listed'}</span>
                  {m.added ? (
                    <Pill tone="ok" dot>
                      Added
                    </Pill>
                  ) : (
                    <button className="btn sm" disabled={!!m.problem || add.isPending} onClick={() => add.mutate(m)} aria-label={`Add ${m.name}`}>
                      {adding ? 'Checking…' : 'Add'}
                    </button>
                  )}
                </div>
              </li>
            )
          })}
        </ul>
      )}
      {pages.isSuccess && models.length === 0 && (
        <Empty icon={NoResultsIcon} title="No model matches">
          {q ? `Nothing in ${label}'s image-to-3D catalog matches "${q}".` : `${label}'s catalog came back empty.`}
        </Empty>
      )}
      <DialogActions>
        {pages.hasNextPage && (
          <button className="btn ghost" onClick={() => void pages.fetchNextPage()} disabled={pages.isFetchingNextPage}>
            {pages.isFetchingNextPage ? 'Loading…' : 'Show more'}
          </button>
        )}
        <button className="btn" onClick={onClose}>
          Done
        </button>
      </DialogActions>
    </Dialog>
  )
}
