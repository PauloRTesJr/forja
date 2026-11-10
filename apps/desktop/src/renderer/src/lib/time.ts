// Time as the app says it, and the one clock every ticking label shares.
import { useCallback, useSyncExternalStore } from 'react'

/** A length of time: "850 ms", "4.2 s", "42 s", "2 min 5 s", "1 h 4 min". */
export function duration(s: number): string {
  if (!Number.isFinite(s) || s <= 0) return '—'
  if (s < 0.9995) return `${Math.max(1, Math.round(s * 1000))} ms`
  if (s < 9.95) return `${s.toFixed(1)} s`
  const whole = Math.round(s)
  if (whole < 60) return `${whole} s`
  if (whole < 3600) {
    const rest = whole % 60
    return rest ? `${Math.floor(whole / 60)} min ${rest} s` : `${whole / 60} min`
  }
  const minutes = Math.round(whole / 60)
  const rest = minutes % 60
  return rest ? `${Math.floor(minutes / 60)} h ${rest} min` : `${minutes / 60} h`
}

/** Time running, as a stopwatch shows it: "0:42", "12:05", "1:02:03". */
export function stopwatch(s: number): string {
  const whole = Math.max(0, Math.floor(Number.isFinite(s) ? s : 0))
  const two = (n: number): string => String(n).padStart(2, '0')
  const h = Math.floor(whole / 3600)
  const m = Math.floor((whole % 3600) / 60)
  return h > 0 ? `${h}:${two(m)}:${two(whole % 60)}` : `${m}:${two(whole % 60)}`
}

/** Time still to go, rounded to what an estimate can honestly say: "about 40 s", "about 3 min". */
export function about(s: number): string {
  if (s < 55) return `about ${Math.max(5, Math.round(s / 5) * 5)} s`
  if (s < 3570) return `about ${Math.round(s / 60)} min`
  return `about ${duration(Math.round(s / 300) * 300)}`
}

/** How long ago a moment was: "just now", "3 min ago", "2 h ago", "yesterday", then the date. */
export function ago(iso: string, now: number): string {
  const then = new Date(iso)
  const diff = (now - then.getTime()) / 1000
  if (!Number.isFinite(diff)) return ''
  if (diff < 45) return 'just now'
  if (diff < 3570) return `${Math.max(1, Math.round(diff / 60))} min ago`
  if (diff < 23.5 * 3600) return `${Math.round(diff / 3600)} h ago`
  const midnight = new Date(now)
  midnight.setHours(0, 0, 0, 0)
  if (then.getTime() >= midnight.getTime() - 86_400_000 && then.getTime() < midnight.getTime()) return 'yesterday'
  return then.toLocaleDateString(undefined, { day: 'numeric', month: 'short', year: then.getFullYear() === midnight.getFullYear() ? undefined : 'numeric' })
}

/** Seconds from one moment to another (`to` in milliseconds); undefined when the start is not known. */
export function secondsSince(iso: string | undefined, to: number): number | undefined {
  if (!iso) return undefined
  const from = Date.parse(iso)
  return Number.isNaN(from) ? undefined : Math.max(0, (to - from) / 1000)
}

// One interval for the whole page, as fast as the fastest label that is on screen needs.
const watchers = new Map<() => void, number>()
let timer: ReturnType<typeof setInterval> | undefined
let period = Infinity
let current = Date.now()

function tick(): void {
  current = Date.now()
  for (const notify of [...watchers.keys()]) notify()
}

function retime(): void {
  const want = Math.min(Infinity, ...watchers.values())
  if (want === period) return
  if (timer !== undefined) clearInterval(timer)
  period = want
  timer = Number.isFinite(want) ? setInterval(tick, want) : undefined
}

/** Calls `notify` every `periodMs` on the shared clock until the returned function is called. */
export function watchClock(notify: () => void, periodMs: number): () => void {
  // Nothing kept the time while no label was watching.
  if (watchers.size === 0) current = Date.now()
  watchers.set(notify, periodMs)
  retime()
  return () => {
    watchers.delete(notify)
    retime()
  }
}

/** The time as of the clock's last tick, in milliseconds. */
export const clockNow = (): number => current

/** How many intervals the shared clock runs: one or none. For tests. */
export const clockTimers = (): number => (timer === undefined ? 0 : 1)

/**
 * The time now, read again every `periodMs` (a second for a stopwatch, half a minute for "3 min
 * ago"). Every caller shares one timer; it measures the screen's own time and asks the core nothing.
 */
export function useNow(periodMs = 30_000): number {
  const subscribe = useCallback((notify: () => void) => watchClock(notify, periodMs), [periodMs])
  return useSyncExternalStore(subscribe, clockNow)
}
