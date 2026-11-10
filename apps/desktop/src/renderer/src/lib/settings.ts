// What the Settings screen decides without the core: what a typed number means, which values
// are locked and by what, and what to say after a save.
import type { Setting } from './api'

/** The values a number setting takes. The core's own `min` and `max` win over the app's. */
export interface NumberRule {
  min: number
  max?: number
  integer?: boolean
}

export type NumberDraft =
  /** Nothing was typed. An empty field is never a value: it does not mean 0. */
  | { kind: 'empty' }
  | { kind: 'unchanged' }
  | { kind: 'value'; value: number }
  | { kind: 'invalid'; message: string }

/** "Enter a whole number between 1 and 1440." */
export function rangeText(rule: NumberRule): string {
  const what = rule.integer ? 'a whole number' : 'a number'
  if (rule.max !== undefined) return `Enter ${what} between ${rule.min} and ${rule.max}.`
  return `Enter ${what}, ${rule.min} or more.`
}

/** What the text in a number field means, against the saved value and the values allowed. */
export function readNumber(draft: string, current: number, rule: NumberRule): NumberDraft {
  let text = draft.trim()
  if (text === '') return { kind: 'empty' }
  // A decimal comma, as many keyboards and languages write it.
  if (/^[-+]?\d*,\d+$/.test(text)) text = text.replace(',', '.')
  const v = Number(text)
  const bad = !Number.isFinite(v) || v < rule.min || (rule.max !== undefined && v > rule.max) || (rule.integer === true && !Number.isInteger(v))
  if (bad) return { kind: 'invalid', message: rangeText(rule) }
  return v === current ? { kind: 'unchanged' } : { kind: 'value', value: v }
}

/** The app's rule for a number setting, narrowed by the limits the core reports for it. */
export function ruleFor(setting: Pick<Setting, 'min' | 'max'> | undefined, fallback: NumberRule): NumberRule {
  const rule = { ...fallback }
  if (typeof setting?.min === 'number') rule.min = setting.min
  if (typeof setting?.max === 'number') rule.max = setting.max
  return rule
}

/** The environment variable that sets a key: FORJA_ROUTING__BUDGET_USD__PER_DAY. */
export const envName = (key: string): string => `FORJA_${key.toUpperCase().replaceAll('.', '__')}`

// Layers above "user" win over what the Settings screen writes.
const ABOVE_USER = new Set(['project', 'env', 'flag'])

/** Whether a higher layer set the value, so the screen cannot change it. */
export const isLocked = (s: Pick<Setting, 'source'>): boolean => ABOVE_USER.has(s.source)

/** Where a locked value comes from, in words; undefined for a value the screen can change. */
export function sourceNote(s: Pick<Setting, 'key' | 'source' | 'source_detail'>): string | undefined {
  switch (s.source) {
    case 'env':
      return `Set by the environment variable ${s.source_detail || envName(s.key)}.`
    case 'flag':
      return 'Set on the command line for this run.'
    case 'project':
      return s.source_detail ? `Set by the project file ${s.source_detail}.` : "Set by the project's forja.toml."
  }
  return undefined
}

/**
 * What to say after a save that touches the engine; undefined when the save needs no words.
 * "next-start" is an older core's answer for both an engine that is not running and a busy one.
 */
export function engineNote(engine: string | undefined): string | undefined {
  switch (engine) {
    case 'restarted':
      return 'Saved. The engine stopped to take the change: models load again on the next job.'
    case 'pending':
      return 'Saved. The engine restarts with the change when the running job ends.'
    case 'next-start':
      return 'Saved. The engine takes the change the next time it starts.'
  }
  return undefined
}

/** A setting's default as the reset button names it. */
export function defaultText(value: unknown, unit?: string): string {
  if (value === true) return 'on'
  if (value === false) return 'off'
  if (value === '' || value === null || value === undefined) return 'automatic'
  return unit ? `${String(value)} ${unit}` : String(value)
}

/** A six-digit pairing code as typed ("482 913", "482-913"): its digits, or undefined when it is not one. */
export function pairingDigits(typed: string): string | undefined {
  const digits = typed.replace(/[\s-]/g, '')
  return /^\d{6}$/.test(digits) ? digits : undefined
}

/** "9:41" until a moment; undefined once it has passed. */
export function countdown(untilIso: string, now = Date.now()): string | undefined {
  const left = Math.floor((new Date(untilIso).getTime() - now) / 1000)
  if (!(left > 0)) return undefined
  return `${Math.floor(left / 60)}:${String(left % 60).padStart(2, '0')}`
}
