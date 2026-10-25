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
