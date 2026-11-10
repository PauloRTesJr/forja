import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useEffect, useRef, useState, type FormEvent } from 'react'
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
import { placeNote, plainProblem, priceNote, providerState } from '../lib/cloud'
import { keys } from '../lib/events'
import { countdown, pairingDigits, ruleFor } from '../lib/settings'
import { useToast } from '../lib/toast'
import { AddCloud } from './Cloud'
import { Choices, ErrorBlock, NumberInput, Pill, Progress, Row, Switch } from './Controls'
import { Dialog, DialogActions } from './Dialog'
import { ConnectHub } from './Hub'
import { AddIcon, CopyIcon } from './icons'
import { Menu, MenuItem, MoreMenu } from './Menu'
import { PriceTable } from './PriceTable'
import { SectionHead } from './ScreenHead'

type Save = (key: string, value: unknown) => Promise<unknown>
type Apply = (key: string, value: unknown) => void

const usd = (n: number): string => `$${n.toFixed(2)}`

/** Where models run, sharing this computer's GPU, and what paid providers may spend. */
export function Providers({ settings, onSave, onApply }: { settings: Map<string, Setting>; onSave: Save; onApply: Apply }) {
  const providers = useQuery({ queryKey: keys.providers, queryFn: () => api<ProviderRow[]>('GET', '/v1/providers') })
  const [adding, setAdding] = useState<'computer' | 'hub' | 'cloud'>()
  // The core announces when a check ends. A quick one (this computer's) can end before the
  // answer that says it runs is read, so a row left checking asks again once, after the core's limit.
  const checking = providers.data?.some((p) => p.checking) ?? false
  const ask = providers.refetch
  useEffect(() => {
    if (!checking) return
    const t = setTimeout(() => void ask(), 3500)
    return () => clearTimeout(t)
  }, [checking, providers.dataUpdatedAt, ask])
  const paid = providers.data?.some((p) => p.paid) ?? false
  const hub = providers.data?.some((p) => p.kind === 'api') ?? false
  return (
    <>
      <div className="section">
        <SectionHead>Where models run</SectionHead>
        <p className="section-note">
          In order of preference. Other computers and services run the models this computer cannot, or the ones you pick them for.
        </p>
        {providers.isPending && <div className="skeleton" style={{ height: 56 }} />}
        {providers.error && <ErrorBlock>{(providers.error as Error).message}</ErrorBlock>}
        {providers.data && (
          <ul className="provider-list group">
            {providers.data.map((p) => (
              <ProviderItem key={p.id} provider={p} />
            ))}
          </ul>
        )}
        <div className="provider-add">
          <AddMenu hub={hub} onPick={setAdding} />
        </div>
        <div className="settings group">
          <Row setting={settings.get('routing.policy')} label="Where a stage runs" stacked onReset={onApply}>
            {(s, locked) => (
              <Choices
                value={String(s.value)}
                disabled={locked}
                onChange={(v) => v !== s.value && onApply(s.key, v)}
                options={[
                  { value: 'prefer-local', label: 'This computer first', help: 'Others only for models it cannot run.' },
                  { value: 'fastest', label: 'Fastest', help: 'Where each step ran quickest before.' },
                  { value: 'cheapest', label: 'Cheapest', help: 'Free computers before paid ones, then the lowest price.' },
                  { value: 'offline-only', label: 'Only my own computers', help: 'This one and the engines you added. Never a hosted service.' },
                ]}
              />
            )}
          </Row>
        </div>
      </div>

      <div className="section">
        <SectionHead>Sharing</SectionHead>
        <div className="settings group">
          <ShareCard />
        </div>
      </div>

      {paid && <Spending settings={settings} onSave={onSave} onApply={onApply} />}
      {paid && <PriceTable />}

      {adding === 'computer' && <AddComputer onClose={() => setAdding(undefined)} />}
      {adding === 'hub' && <ConnectHub onClose={() => setAdding(undefined)} />}
      {adding === 'cloud' && <AddCloud onClose={() => setAdding(undefined)} />}
    </>
  )
}

/** One button for the three kinds of provider that can be added. */
function AddMenu({ hub, onPick }: { hub: boolean; onPick: (kind: 'computer' | 'hub' | 'cloud') => void }) {
  return (
    <Menu
      label="Add a provider"
      menuClass="provider-add-menu"
      trigger={
        <>
          <AddIcon />
          Add a provider
        </>
      }
    >
      {(id) => (
        <>
          <MenuItem menu={id} help="A PC with a GPU on your network that runs Forja." onClick={() => onPick('computer')}>
            Another computer
          </MenuItem>
          {!hub && (
            <MenuItem menu={id} help="Hosted 3D models with one API key, paid per image." onClick={() => onPick('hub')}>
              fal.ai
            </MenuItem>
          )}
          <MenuItem menu={id} help="RunPod, Modal, or your own ComfyUI workflows." onClick={() => onPick('cloud')}>
            A cloud GPU or ComfyUI
          </MenuItem>
        </>
      )}
    </Menu>
  )
}

function ProviderItem({ provider: p }: { provider: ProviderRow }) {
  const [asking, setAsking] = useState<'remove' | 'key'>()
  const local = p.id === 'local'
  const name = local ? 'This computer' : p.label
  const state = providerState(p)
  const place = !local ? placeNote(p) : undefined
  return (
    <li className="provider provider-item">
      <div className="grow">
        <div className="provider-head">
          <strong>{name}</strong>
          <Pill tone={state.tone} dot={!state.live} live={state.live}>
            {state.label}
          </Pill>
        </div>
        <div className="muted provider-meta">
          {place && <span className="mono">{place}</span>}
          {state.detail && <span>{state.detail}</span>}
          {priceNote(p) && <span>{priceNote(p)}</span>}
          {p.fingerprint && <span className="mono">certificate {p.fingerprint}</span>}
        </div>
        {state.problem && (
          <div className="provider-problem" title={p.error_detail}>
            {state.problem}
          </div>
        )}
        {p.key && (
          <div className="provider-key">{p.key.stored ? `Key ends in ${p.key.hint} · ${p.key.store}` : `No key in ${p.key.store}: replace it to connect`}</div>
        )}
      </div>
      {p.editable && (
        <MoreMenu label={`More for ${name}`}>
          {(menu) => (
            <>
              {p.key && !p.key.fixed && (
                <MenuItem menu={menu} onClick={() => setAsking('key')}>
                  Replace key…
                </MenuItem>
              )}
              <MenuItem menu={menu} danger onClick={() => setAsking('remove')}>
                Remove…
              </MenuItem>
            </>
          )}
        </MoreMenu>
      )}
      {!p.editable && !local && <span className="provider-key">Set in a configuration file</span>}
      {asking === 'remove' && <RemoveProvider provider={p} onClose={() => setAsking(undefined)} />}
      {asking === 'key' && <ReplaceKey provider={p} onClose={() => setAsking(undefined)} />}
    </li>
  )
}

function RemoveProvider({ provider: p, onClose }: { provider: ProviderRow; onClose: () => void }) {
  const toast = useToast()
  const qc = useQueryClient()
  const remove = useMutation({
    mutationFn: () => removeProvider(p.id),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: keys.providers })
      toast(`Removed ${p.label}`)
      onClose()
    },
  })
  return (
    <Dialog title={`Remove ${p.label}?`} size="narrow" alert onClose={onClose}>
      <p className="muted">
        Forja forgets it{p.key ? ' and deletes its key from the keychain' : ''}. Stages that were sent to it go where the others go.
      </p>
      {remove.error && <ErrorBlock role="alert">{(remove.error as Error).message}</ErrorBlock>}
      <DialogActions>
        <button className="btn ghost" onClick={onClose} autoFocus>
          Cancel
        </button>
        <button className="btn danger" onClick={() => remove.mutate()} disabled={remove.isPending}>
          {remove.isPending ? 'Removing…' : 'Remove'}
        </button>
      </DialogActions>
    </Dialog>
  )
}

function ReplaceKey({ provider: p, onClose }: { provider: ProviderRow; onClose: () => void }) {
  const toast = useToast()
  const qc = useQueryClient()
  const [key, setKey] = useState('')
  const replace = useMutation({
    mutationFn: () => replaceToken(p.id, key.trim()),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: keys.providers })
      toast(`Key replaced for ${p.label}`)
      onClose()
    },
  })
  const submit = (e: FormEvent): void => {
    e.preventDefault()
    if (key.trim() && !replace.isPending) replace.mutate()
  }
  return (
    <Dialog title={`Replace the key for ${p.label}`} size="narrow" keepOpen={key !== '' || replace.isPending} onClose={onClose}>
      <form className="dialog-form" onSubmit={submit}>
        <label className="field">
          <span>New key or access token</span>
          <input className="input" type="password" autoComplete="off" value={key} onChange={(e) => setKey(e.target.value)} readOnly={replace.isPending} autoFocus />
          <span className="muted small">It is tried first, then stored in {p.key?.store ?? 'the keychain'}. The old one is replaced.</span>
        </label>
        {replace.error && <ErrorBlock role="alert">{(replace.error as Error).message}</ErrorBlock>}
        <DialogActions>
          <button type="button" className="btn ghost" onClick={onClose}>
            Cancel
          </button>
          <button className="btn primary" disabled={replace.isPending || !key.trim()}>
            {replace.isPending ? 'Testing…' : 'Test and save'}
          </button>
        </DialogActions>
      </form>
    </Dialog>
  )
}

/** Adds a computer by the pairing code it shows, or by its access token. */
function AddComputer({ onClose }: { onClose: () => void }) {
  const toast = useToast()
  const qc = useQueryClient()
  const [mode, setMode] = useState<'code' | 'token'>('code')
  const [url, setUrl] = useState('')
  const [label, setLabel] = useState('')
  const [code, setCode] = useState('')
  const [token, setToken] = useState('')
  const [problem, setProblem] = useState<string>()
  // The attempt under way, so Cancel and Escape call it off instead of leaving it to finish unseen.
  const attempt = useRef<AbortController>(undefined)
  const close = (): void => {
    attempt.current?.abort()
    onClose()
  }
  const add = useMutation({
    mutationFn: (secret: { code: string } | { token: string }) => {
      attempt.current = new AbortController()
      return addProvider({ url: url.trim(), label: label.trim() || undefined, ...secret }, attempt.current.signal)
    },
    onSuccess: (p) => {
      void qc.invalidateQueries({ queryKey: keys.providers })
      toast(`Connected to ${p.label}`)
      onClose()
    },
    onError: (e: Error) => {
      if (!attempt.current?.signal.aborted) setProblem(plainProblem(e.message))
    },
  })
  const secret = mode === 'code' ? code : token
  const submit = (e: FormEvent): void => {
    e.preventDefault()
    if (add.isPending || !url.trim() || !secret.trim()) return
    setProblem(undefined)
    if (mode === 'token') return add.mutate({ token: token.trim() })
    const digits = pairingDigits(code)
    // A wrong guess uses up one of the code's five tries on the other computer.
    if (!digits) return setProblem('A pairing code is six digits, such as 482 913.')
    add.mutate({ code: digits })
  }
  const edited = (): void => setProblem(undefined)
  return (
    <Dialog title="Add another computer" keepOpen={url !== '' || code !== '' || token !== '' || label !== ''} onClose={close}>
      <form className="dialog-form" onSubmit={submit}>
        <p className="muted">
          On the other computer, open Settings and turn on <strong>Share this computer's GPU</strong>. It shows its address and a pairing code.
        </p>
        <label className="field">
          <span>Address</span>
          <input
            className="input"
            value={url}
            onChange={(e) => {
              setUrl(e.target.value)
              edited()
            }}
            placeholder="192.168.1.20 or studio.local"
            readOnly={add.isPending}
            spellCheck={false}
            autoComplete="off"
            autoFocus
          />
        </label>
        <Choices
          label="How to connect"
          value={mode}
          disabled={add.isPending}
          onChange={(v) => {
            setMode(v as 'code' | 'token')
            edited()
          }}
          options={[
            { value: 'code', label: 'Pairing code', help: 'Six digits, valid for ten minutes.' },
            { value: 'token', label: 'Access token', help: 'For an engine you started by hand.' },
          ]}
        />
        {mode === 'code' ? (
          <label className="field">
            <span>Pairing code</span>
            <input
              className="input pairing-input"
              inputMode="numeric"
              autoComplete="off"
              value={code}
              onChange={(e) => {
                setCode(e.target.value)
                edited()
              }}
              placeholder="482 913"
              readOnly={add.isPending}
            />
          </label>
        ) : (
          <label className="field">
            <span>Access token</span>
            <input
              className="input"
              type="password"
              autoComplete="off"
              value={token}
              onChange={(e) => {
                setToken(e.target.value)
                edited()
              }}
              readOnly={add.isPending}
            />
          </label>
        )}
        <label className="field">
          <span>Name (optional)</span>
          <input className="input" value={label} onChange={(e) => setLabel(e.target.value)} placeholder="Studio PC" readOnly={add.isPending} />
        </label>
        {problem && <ErrorBlock role="alert">{problem}</ErrorBlock>}
        <p className="muted small">The token goes to this computer's keychain, never to a file. The connection is encrypted and pinned to that computer's certificate.</p>
        <DialogActions>
          <button type="button" className="btn ghost" onClick={close}>
            Cancel
          </button>
          <button className="btn primary" disabled={add.isPending || !url.trim() || !secret.trim()}>
            {add.isPending ? 'Connecting…' : 'Connect'}
          </button>
        </DialogActions>
      </form>
    </Dialog>
  )
}

/** Serving this computer's engine to other computers on the network. */
function ShareCard() {
  const toast = useToast()
  const qc = useQueryClient()
  const share = useQuery({ queryKey: keys.share, queryFn: () => api<ShareView>('GET', '/v1/share') })
  const [code, setCode] = useState<{ code: string; expires_at: string; tracked: boolean }>()
  const [, tick] = useState(0)
  const failed = (e: Error): void => void toast(e.message, true)
  const toggle = useMutation({
    mutationFn: setSharing,
    onSuccess: (view) => {
      qc.setQueryData(keys.share, view)
      setCode(undefined)
    },
    onError: failed,
  })
  const newCode = useMutation({
    mutationFn: async () => {
      const made = await pairingCode()
      // A newer core says while the code can still be used; then the app knows when it was.
      const view = await api<ShareView>('GET', '/v1/share')
      qc.setQueryData(keys.share, view)
      return { ...made, tracked: !!view.code_expires_at }
    },
    onSuccess: setCode,
    onError: failed,
  })
  const s = share.data
  const left = code ? countdown(code.expires_at) : undefined
  // A code is shown only while it works: until its time is up, it was used (a newer core then
  // stops reporting it), or the engine that held it stopped.
  const usable = !!code && !!left && !!s?.active && !(code.tracked && !s.code_expires_at)
  useEffect(() => {
    if (!code) return
    const t = setInterval(() => tick((n) => n + 1), 1000)
    return () => clearInterval(t)
  }, [code])
  const address = s?.addresses.join(' · ') || (s ? `this computer, port ${s.port}` : '')
  return (
    <div className="setting share">
      <div className="setting-text">
        <strong id="share-label">Share this computer's GPU</strong>
        <div id="share-help" className="setting-help">
          <div>Other computers on your network can run models here, after pairing with a code.</div>
        </div>
        {share.error && <ErrorBlock>{(share.error as Error).message}</ErrorBlock>}
        {s?.enabled && s.error && <ErrorBlock role="alert">{s.error}</ErrorBlock>}
        {s?.enabled && !s.active && !s.error && (
          <span className="setting-note" role="status">
            {s.pending === 'restart' ? 'Starts when the running job ends: the engine restarts to open the network port.' : 'Starting the engine for the network…'}
          </span>
        )}
        {s?.active && (
          <div className="share-details">
            <dl className="kv">
              <div className="kv-row">
                <dt>Address</dt>
                <dd>
                  <span className="mono">{address}</span>
                  {s.addresses[0] && (
                    <button
                      className="btn ghost icon sm"
                      aria-label="Copy the address"
                      onClick={() => void window.forja.copyText(s.addresses[0]!).then(() => toast('Address copied'), failed)}
                    >
                      <CopyIcon />
                    </button>
                  )}
                </dd>
              </div>
              {s.fingerprint && (
                <div className="kv-row">
                  <dt>Certificate</dt>
                  <dd className="mono">{s.fingerprint}</dd>
                </div>
              )}
            </dl>
            {code && usable ? (
              <span className="pairing-code" role="status">
                <b className="mono">{code.code}</b>
                <span className="muted">
                  Valid for <span className="num">{left}</span>, for one computer
                </span>
              </span>
            ) : (
              <button className="btn sm" onClick={() => newCode.mutate()} disabled={newCode.isPending}>
                {newCode.isPending ? 'Making a code…' : 'Show a pairing code'}
              </button>
            )}
            <span className="muted small">If Windows asks, allow Forja's engine on private networks.</span>
          </div>
        )}
      </div>
      <div className="setting-control">
        <Switch
          checked={s?.enabled ?? false}
          disabled={!s || toggle.isPending}
          label="Share this computer's GPU"
          onChange={(on) => toggle.mutate(on)}
        />
        <span className="setting-slot" />
      </div>
    </div>
  )
}

/** What paid providers may spend, and whether they may step in by themselves. */
function Spending({ settings, onSave, onApply }: { settings: Map<string, Setting>; onSave: Save; onApply: Apply }) {
  const spend = useQuery({ queryKey: keys.spend, queryFn: () => api<Spend>('GET', '/v1/spend') })
  const spent = (so: number | undefined, limit: number, when: string, until: string) => {
    if (so === undefined) return undefined
    if (!(limit > 0)) return `${usd(so)} spent ${when}.`
    const reached = so >= limit
    return (
      <span className="limit-spent">
        <Progress value={so / limit} />
        <span className={reached ? 'limit-reached' : undefined}>
          {usd(so)} of {usd(limit)} spent {when}.{reached && ` Limit reached: paid stages are refused until ${until}.`}
        </span>
      </span>
    )
  }
  const limits = [
    { key: 'routing.budget_usd.per_job', label: 'Limit per image', help: 'A job whose paid steps would cost more is refused, or stopped once it would.' },
    { key: 'routing.budget_usd.per_day', label: 'Limit per day', help: spent(spend.data?.today, Number(settings.get('routing.budget_usd.per_day')?.value ?? 0), 'today', 'tomorrow') },
    {
      key: 'routing.budget_usd.per_month',
      label: 'Limit per month',
      help: spent(spend.data?.month, Number(settings.get('routing.budget_usd.per_month')?.value ?? 0), 'this month', 'next month'),
    },
  ]
  return (
    <div className="section">
      <SectionHead>Spending</SectionHead>
      <p className="section-note">For the providers that cost money. A limit stops paid work the moment it would be passed.</p>
      <div className="settings group">
        <Row
          setting={settings.get('routing.allow_paid_fallback')}
          label="Use paid providers as a fallback"
          help="When no free one can run a stage, or the one it ran on is down or out of memory. Off, a paid provider runs only the models you pick it for."
          onReset={onApply}
        >
          {(s, locked) => <Switch checked={s.value === true} disabled={locked} onChange={(on) => onApply(s.key, on)} />}
        </Row>
        {limits.map(({ key, label, help }) => (
          <Row key={key} setting={settings.get(key)} label={label} help={help} defaultAs="no limit" onReset={onApply}>
            {(s, locked) => (
              <NumberInput
                value={Number(s.value ?? 0)}
                unit="USD"
                {...ruleFor(s, { min: 0, max: 100000 })}
                noneText="No limit"
                disabled={locked}
                onCommit={(v) => onSave(s.key, v)}
                onClear={() => onSave(s.key, null)}
              />
            )}
          </Row>
        ))}
      </div>
    </div>
  )
}
