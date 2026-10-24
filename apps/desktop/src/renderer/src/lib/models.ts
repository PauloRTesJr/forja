// What the Models screen says about one model, column by column, so models of a stage compare at a glance.
import type { Fit, License, Model } from './api'
import { money } from './format'

/** Good news, a caveat, or something that rules the model out on this machine. */
export type Tone = 'ok' | 'warn' | 'dim'

export interface Fact {
  value: string
  detail?: string
  tone?: Tone
}

export const fitText: Record<Fit, string> = {
  fits: 'Fits your GPU',
  offload: 'Runs with offload, slower',
  'too-big': 'Needs more VRAM',
  unsupported: 'Cannot run here',
  hosted: 'Hosted',
}

export const isInstalled = (m: Model): boolean => m.status === 'ready' || m.status === 'loaded'

export const isHosted = (m: Model): boolean => m.fit === 'hosted'

/** Where a model runs, or would once installed, and how well it fits there. */
export function runsOn(m: Model): Fact {
  if (isHosted(m)) return { value: m.provider_label, detail: 'In the cloud' }
  const where = m.provider === 'local' ? 'This computer' : m.provider_label
  if (isInstalled(m)) {
    const notes = [m.device, m.status === 'loaded' && 'in VRAM', m.fit === 'offload' && 'with offload, slower']
    return { value: where, detail: notes.filter(Boolean).join(' · ') || undefined, tone: m.fit === 'offload' ? 'warn' : undefined }
  }
  switch (m.fit) {
    case 'fits':
      return { value: where, detail: fitText.fits, tone: 'ok' }
    case 'offload':
      return { value: where, detail: fitText.offload, tone: 'warn' }
    case 'too-big':
    case 'unsupported':
      return { value: 'Another computer', detail: fitText[m.fit], tone: 'dim' }
    default:
      return { value: where }
  }
}

/** What a run costs: nothing locally, the provider's price when hosted. */
export function cost(m: Model): Fact {
  if (isHosted(m)) return { value: money(m.price), detail: `Billed by ${m.provider_label}` }
  return { value: 'Free', detail: m.disk_gb ? `${m.disk_gb.toFixed(1)} GB on disk` : undefined }
}

/** A license's name for people: SPDX ids stay, a custom "LicenseRef-" id reads as words. */
export function licenseName(spdx: string): string {
  const custom = /^LicenseRef-(.+)$/.exec(spdx)
  return custom ? custom[1]!.replaceAll('-', ' ') : spdx
}

/** The license and whether a game made with the model can be sold. */
export function licenseFact(l: License): Fact {
  const value = licenseName(l.spdx)
  if (l.restrictions.length > 0) return { value, detail: l.commercial ? 'Commercial, with limits' : 'Non-commercial, with limits', tone: 'warn' }
  return l.commercial ? { value, detail: 'Commercial use', tone: 'ok' } : { value, detail: 'Non-commercial', tone: 'warn' }
}

/** A license's restrictions as one sentence. */
export function limits(l: License): string | undefined {
  if (l.restrictions.length === 0) return undefined
  const text = l.restrictions.join('; ')
  return `${text[0]!.toUpperCase()}${text.slice(1)}.`
}
