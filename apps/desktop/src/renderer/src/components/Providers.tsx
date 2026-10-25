import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useEffect, useState, type FormEvent } from 'react'
import {
  addProvider,
  api,
  pairingCode,
  removeProvider,
  replaceToken,
  setSharing,
  type ProviderRow,
  type Setting,
  type ShareView,
  type Spend,
} from '../lib/api'
import { placeNote, priceNote } from '../lib/cloud'
import { keys } from '../lib/events'
import { useToast } from '../lib/toast'
import { AddCloud } from './Cloud'
import { Choices, NumberInput, Row } from './Controls'
import { ConnectHub } from './Hub'
import { PriceTable } from './PriceTable'
import { SectionHead } from './ScreenHead'

/** Where models run: this computer, other computers, and sharing this one's GPU. */
export function Providers({ settings, onSet }: { settings: Map<string, Setting>; onSet: (key: string, value: unknown) => void }) {
  const providers = useQuery({ queryKey: keys.providers, queryFn: () => api<ProviderRow[]>('GET', '/v1/providers') })
  const [adding, setAdding] = useState(false)
  const [connecting, setConnecting] = useState(false)
  const [renting, setRenting] = useState(false)
  const paid = providers.data?.some((p) => p.paid) ?? false
  const hub = providers.data?.some((p) => p.kind === 'api') ?? false
  const spend = useQuery({ queryKey: keys.spend, queryFn: () => api<Spend>('GET', '/v1/spend'), enabled: paid })
  return (
    <div className="section">
      <SectionHead>Where models run</SectionHead>
      <div className="settings">
        {providers.isPending && <div className="skeleton" style={{ height: 52 }} />}
        {providers.data && (
          <ul className="provider-list">
            {providers.data.map((p) => (
              <ProviderItem key={p.id} provider={p} />
            ))}
          </ul>
        )}
        <div className="row">
          <button className="btn" onClick={() => setAdding(true)}>
            Add another computer
          </button>
          {!hub && (
            <button className="btn" onClick={() => setConnecting(true)}>
              Connect fal.ai
            </button>
          )}
          <button className="btn" onClick={() => setRenting(true)}>
            Add a cloud GPU or ComfyUI
          </button>
          <span className="muted">Stages use them for models this computer cannot run, or when you pick one.</span>
        </div>
        <ShareCard />
        <Row setting={settings.get('routing.policy')} label="Which computer a stage uses" onReset={onSet}>
          {(s, locked) => (
            <Choices
              value={String(s.value)}
              disabled={locked}
              onChange={(v) => onSet(s.key, v)}
              options={[
                { value: 'prefer-local', label: 'This computer first', help: 'Others only for models it cannot run.' },
                { value: 'fastest', label: 'Fastest', help: 'Where each step ran quickest before.' },
                { value: 'cheapest', label: 'Cheapest', help: 'Free computers before paid ones, then the lowest price.' },
                { value: 'offline-only', label: 'Never leave this network', help: 'Only this computer and your own engines.' },
              ]}
            />
          )}
        </Row>
        {paid && (
          <Row
            setting={settings.get('routing.allow_paid_fallback')}
            label="Let paid providers step in"
            help="When no free provider can run a stage, or the one it ran on is down or out of memory."
            onReset={onSet}
          >
            {(s, locked) => (
              <button className="switch" role="switch" aria-checked={s.value === true} disabled={locked} onClick={() => onSet(s.key, s.value !== true)}>
                <span className="switch-track" aria-hidden />
                {s.value === true ? 'On' : 'Off'}
              </button>
            )}
          </Row>
        )}
        {paid && (
          <>
            {(
              [
                ['routing.budget_usd.per_job', 'Spending limit per image', 'A job whose paid steps would cost more is refused, or stopped once it would.'],
                ['routing.budget_usd.per_day', 'Spending limit per day', spend.data ? `Spent today: $${spend.data.today.toFixed(2)}.` : ''],
                ['routing.budget_usd.per_month', 'Spending limit per month', spend.data ? `Spent this month: $${spend.data.month.toFixed(2)}.` : ''],
              ] as const
            ).map(([key, label, help]) => (
              <Row key={key} setting={settings.get(key)} label={label} help={`${help} 0 means no limit.`} onReset={onSet}>
                {(s, locked) => (
                  <NumberInput value={Number(s.value ?? 0)} unit="USD" min={0} step={0.5} disabled={locked} onCommit={(v) => onSet(s.key, v)} />
                )}
              </Row>
            ))}
            <PriceTable />
          </>
        )}
      </div>
      {adding && <AddComputer onClose={() => setAdding(false)} />}
      {connecting && <ConnectHub onClose={() => setConnecting(false)} />}
      {renting && <AddCloud onClose={() => setRenting(false)} />}
    </div>
  )
}

function ProviderItem({ provider: p }: { provider: ProviderRow }) {
  const toast = useToast()
  const qc = useQueryClient()
  const [confirm, setConfirm] = useState(false)
  const [newKey, setNewKey] = useState<string | null>(null)
  const done = (): void => void qc.invalidateQueries({ queryKey: keys.providers })
  const remove = useMutation({ mutationFn: () => removeProvider(p.id), onSuccess: done, onError: (e: Error) => toast(e.message, true) })
  const replace = useMutation({
    mutationFn: (token: string) => replaceToken(p.id, token),
    onSuccess: () => {
      setNewKey(null)
      toast('Key replaced')
      done()
    },
    onError: (e: Error) => toast(e.message, true),
  })
  const local = p.id === 'local'
  const status = p.healthy ? `${p.latency_ms} ms` : p.error === 'idle' ? 'idle, starts when needed' : p.error
  return (
    <li className="provider provider-item">
      <span className={`dot ${p.healthy ? 'ok' : ''}`} />
      <div className="grow">
        <strong>{local ? 'This computer' : p.label}</strong>
        <div className="muted provider-meta">
          {!local && placeNote(p) && <span className="mono">{placeNote(p)}</span>}
          <span>{status}</span>
          {priceNote(p) && <span>{priceNote(p)}</span>}
          {p.fingerprint && <span className="mono">certificate {p.fingerprint}</span>}
        </div>
        {p.key && (
          <div className="provider-key">
            {p.key.stored ? `Key in ${p.key.store}, ends in ${p.key.hint}` : `No key in ${p.key.store}`}
          </div>
        )}
        {newKey !== null && (
          <form
            className="row key-form"
            onSubmit={(e) => {
              e.preventDefault()
              if (newKey.trim()) replace.mutate(newKey.trim())
            }}
          >
            <input className="input" type="password" autoComplete="off" placeholder="New access token" value={newKey} onChange={(e) => setNewKey(e.target.value)} autoFocus />
            <button className="btn primary" disabled={replace.isPending || !newKey.trim()}>
              {replace.isPending ? 'Testing…' : 'Test and save'}
            </button>
            <button type="button" className="btn ghost" onClick={() => setNewKey(null)}>
              Cancel
            </button>
          </form>
        )}
      </div>
      {p.editable && newKey === null && (
        <div className="row">
          {p.key && (
            <button className="btn ghost" onClick={() => setNewKey('')}>
              Replace key
            </button>
          )}
          <button
            className={`btn ghost ${confirm ? 'danger' : ''}`}
            onClick={() => (confirm ? remove.mutate() : setConfirm(true))}
            onBlur={() => setConfirm(false)}
            disabled={remove.isPending}
          >
            {confirm ? 'Remove it?' : 'Remove'}
          </button>
        </div>
      )}
    </li>
  )
}

/** Adds a computer by the pairing code it shows, or by its access token. */
function AddComputer({ onClose }: { onClose: () => void }) {
  const toast = useToast()
  const [mode, setMode] = useState<'code' | 'token'>('code')
  const [url, setUrl] = useState('')
  const [label, setLabel] = useState('')
  const [secret, setSecret] = useState('')
  const add = useMutation({
    mutationFn: () => addProvider({ url, label: label || undefined, ...(mode === 'code' ? { code: secret } : { token: secret }) }),
    onSuccess: (p) => {
      toast(`Connected to ${p.label}`)
      onClose()
    },
  })
  useEffect(() => {
    const onKey = (e: KeyboardEvent): void => {
      if (e.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose])
  const submit = (e: FormEvent): void => {
    e.preventDefault()
    add.mutate()
  }
  return (
    <div className="dialog-backdrop" onClick={onClose}>
      <form className="dialog" role="dialog" aria-modal aria-label="Add another computer" onClick={(e) => e.stopPropagation()} onSubmit={submit}>
        <h2>Add another computer</h2>
        <p className="muted">
          On the other computer, open Settings and turn on <strong>Share this computer's GPU</strong>. It shows its address and a pairing code.
        </p>
        <label className="field">
          <span>Address</span>
          <input className="input" value={url} onChange={(e) => setUrl(e.target.value)} placeholder="192.168.1.20 or studio.local" autoFocus required />
        </label>
        <Choices
          value={mode}
          onChange={(v) => {
            setMode(v as 'code' | 'token')
            setSecret('')
          }}
          options={[
            { value: 'code', label: 'Pairing code', help: 'Six digits, valid for ten minutes.' },
            { value: 'token', label: 'Access token', help: 'For an engine you started by hand.' },
          ]}
        />
        <label className="field">
          <span>{mode === 'code' ? 'Pairing code' : 'Access token'}</span>
          <input
            className="input"
            type={mode === 'code' ? 'text' : 'password'}
            inputMode={mode === 'code' ? 'numeric' : undefined}
            autoComplete="off"
            value={secret}
            onChange={(e) => setSecret(e.target.value)}
            placeholder={mode === 'code' ? '482 913' : ''}
            required
          />
        </label>
        <label className="field">
          <span>Name (optional)</span>
          <input className="input" value={label} onChange={(e) => setLabel(e.target.value)} placeholder="Studio PC" />
        </label>
        {add.error && <div className="error">{(add.error as Error).message}</div>}
        <p className="muted small">The token goes to this computer's keychain, never to a file. The connection is encrypted and pinned to that computer's certificate.</p>
        <div className="row dialog-actions">
          <button type="button" className="btn ghost" onClick={onClose}>
            Cancel
          </button>
          <button className="btn primary" disabled={add.isPending || !url || !secret}>
            {add.isPending ? 'Connecting…' : 'Connect'}
          </button>
        </div>
      </form>
    </div>
  )
}

/** Serving this computer's engine to other computers on the network. */
function ShareCard() {
  const toast = useToast()
  const qc = useQueryClient()
  const share = useQuery({ queryKey: keys.share, queryFn: () => api<ShareView>('GET', '/v1/share') })
  const [code, setCode] = useState<{ code: string; expires_at: string }>()
  const toggle = useMutation({
    mutationFn: setSharing,
    onSuccess: (view) => {
      qc.setQueryData(keys.share, view)
      setCode(undefined)
      if (view.pending === 'restart') toast('Applies once the running jobs finish.')
    },
    onError: (e: Error) => toast(e.message, true),
  })
  const newCode = useMutation({ mutationFn: pairingCode, onSuccess: setCode, onError: (e: Error) => toast(e.message, true) })
  const s = share.data
  return (
    <div className="setting share">
      <div className="setting-text">
        <strong>Share this computer's GPU</strong>
        <span>Other computers on your network can run models here, after pairing with a code.</span>
        {s?.enabled && !s.active && <span className="setting-source">Starting the engine for the network…</span>}
        {s?.active && (
          <div className="share-details">
            <span>
              Address: <span className="mono">{s.addresses.join(' · ') || `this computer, port ${s.port}`}</span>
            </span>
            {s.fingerprint && (
              <span>
                Certificate: <span className="mono">{s.fingerprint}</span>
              </span>
            )}
            {code ? (
              <span className="pairing-code">
                <b className="mono">{code.code}</b>
                <span className="muted">valid until {new Date(code.expires_at).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })}, once</span>
              </span>
            ) : (
              <button className="btn" onClick={() => newCode.mutate()} disabled={newCode.isPending}>
                Show a pairing code
              </button>
            )}
            <span className="muted small">If Windows asks, allow Forja's engine on private networks.</span>
          </div>
        )}
      </div>
      <div className="setting-control">
        <button className="switch" role="switch" aria-checked={s?.enabled ?? false} disabled={!s || toggle.isPending} onClick={() => toggle.mutate(!s?.enabled)}>
          <span className="switch-track" aria-hidden />
          {s?.enabled ? 'On' : 'Off'}
        </button>
      </div>
    </div>
  )
}
