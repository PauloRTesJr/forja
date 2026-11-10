import type { ProviderField, ProviderKind, ProviderRow } from './api'

/** The stage types a ComfyUI workflow can do, as the app names them. */
export const WORKFLOW_STAGES = [
  { use: 'background-removal', label: 'Background removal' },
  { use: 'shape', label: 'Shape' },
  { use: 'texture', label: 'Texture' },
] as const

/** The kinds the cloud dialog adds: rented GPUs and ComfyUI, not another computer or a hub. */
export function cloudKinds(kinds: ProviderKind[]): ProviderKind[] {
  return kinds.filter((k) => k.kind === 'serverless' || k.kind === 'comfyui' || (k.kind === 'engine' && !!k.vendor))
}

export const kindKey = (k: ProviderKind): string => `${k.kind}/${k.vendor ?? ''}`

/** Fields to fill before the core can try the settings; a volume asks for all of its own. */
export function missing(kind: ProviderKind, values: Record<string, string>, workflows: Record<string, string>): ProviderField[] {
  const filled = (f: ProviderField): boolean => (f.type === 'workflows' ? Object.values(workflows).some(Boolean) : !!values[f.key]?.trim())
  const volume = !!values.volume?.trim()
  return kind.fields.filter((f) => {
    if (filled(f)) return false
    if (volume && ['region', 'volume_key_id', 'volume_secret'].includes(f.key)) return true
    return !f.optional
  })
}

/** The request POST /v1/providers (and its test) takes for a kind, from the dialog's fields. */
export function bodyFor(kind: ProviderKind, values: Record<string, string>, workflows: Record<string, string>): Record<string, unknown> {
  const body: Record<string, unknown> = { kind: kind.kind }
  if (kind.vendor) body.vendor = kind.vendor
  for (const f of kind.fields) {
    if (f.type === 'workflows') {
      const chosen = Object.fromEntries(Object.entries(workflows).filter(([, path]) => !!path))
      if (Object.keys(chosen).length) body.workflows = chosen
      continue
    }
    const v = values[f.key]?.trim()
    if (!v) continue
    body[f.key] = f.type === 'number' ? Number(v) : v
  }
  if (values.label?.trim()) body.label = values.label.trim()
  return body
}

/** A number field holds a non-negative number or nothing. */
export const badNumber = (kind: ProviderKind, values: Record<string, string>): ProviderField | undefined =>
  kind.fields.find((f) => f.type === 'number' && values[f.key]?.trim() && !(Number(values[f.key]) >= 0))

/** What a rented GPU costs, as the provider list shows it. */
export function priceNote(p: ProviderRow): string | undefined {
  const s = p.price_usd_per_second
  if (!s) return undefined
  const hour = s * 3600
  return `$${hour < 10 ? hour.toFixed(2) : hour.toFixed(0)} per GPU hour`
}

/** Where a provider is, as the provider list shows it. */
export function placeNote(p: ProviderRow): string | undefined {
  if (p.endpoint_id) return `RunPod endpoint ${p.endpoint_id}`
  return p.url
}

/** How a provider stands, as its row says it: a word, its colour, and what to add to it. */
export interface ProviderState {
  label: string
  tone?: 'ok' | 'bad' | 'accent'
  /** Work in progress: the pill's dot pulses. */
  live?: boolean
  /** A fact beside the address, such as the answer time. */
  detail?: string
  /** What is wrong and what to do, in a sentence. */
  problem?: string
}

/**
 * A connection error in a sentence. Newer cores send one already; an older core sends the
 * transport's own words ("dial tcp ...: connectex: ..."), in the system's language. `service`
 * is a hosted service or a ComfyUI address, where there is no sharing to switch on.
 */
export function plainProblem(error: string | undefined, service = false): string | undefined {
  const e = error?.trim()
  if (!e) return undefined
  if (/deadline exceeded|Client\.Timeout|i\/o timeout|timed out/i.test(e)) return service ? 'It did not answer in time. Check the address and your connection.' : 'It did not answer in time. Check that it is switched on and connected.'
  if (/connectex|connection refused|actively refused|dial tcp/i.test(e)) return service ? 'Nothing answers at that address. Check the address and your connection.' : 'Nothing answers at its address. Check that it is switched on, and that sharing is on there.'
  if (/no such host/i.test(e)) return 'Its name was not found on the network. Check the address.'
  if (/\b401\b|\b403\b|refused the (token|key)/i.test(e)) return 'It refused the key. Replace the key.'
  return e
}

export function providerState(p: Pick<ProviderRow, 'healthy' | 'latency_ms' | 'error' | 'checking'> & Partial<Pick<ProviderRow, 'kind' | 'vendor'>>): ProviderState {
  if (p.healthy) return { label: 'Ready', tone: 'ok', detail: `${p.latency_ms} ms` }
  // The local engine starts by itself when a job needs it: not running is not a fault.
  if (p.error === 'idle') return { label: 'Idle', detail: 'Starts when a job needs it' }
  // Only a provider with no last answer at all shows as checking; one with a known fault keeps saying it.
  if (p.checking && !p.error) return { label: 'Checking', tone: 'accent', live: true }
  return { label: 'Not reachable', tone: 'bad', problem: plainProblem(p.error, (!!p.kind && p.kind !== 'engine') || !!p.vendor) }
}

/**
 * The fields two kinds share keep what was typed when the kind changes (a name, an address).
 * Never a key: one vendor's key means nothing to another.
 */
export function carryValues(to: ProviderKind, values: Record<string, string>): Record<string, string> {
  const keep = new Set(to.fields.filter((f) => f.type !== 'workflows' && f.type !== 'secret').map((f) => f.key))
  keep.add('label')
  return Object.fromEntries(Object.entries(values).filter(([k, v]) => keep.has(k) && v !== ''))
}

/** The fields that only matter together with a RunPod network volume. */
export const VOLUME_FIELDS = ['volume', 'region', 'volume_key_id', 'volume_secret']
