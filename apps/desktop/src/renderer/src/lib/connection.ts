// The page's view of the app's connection to its core: what to read again when it changes, and
// which queries an event has made doubtful.
import type { QueryClient, QueryKey } from '@tanstack/react-query'
import type { CoreState } from '../../../preload/index'

export type { CoreState }

/** Before the main process has answered: the page never assumes a core it has not heard of. */
export const STARTING: CoreState = { phase: 'starting', generation: 0 }

/** Whether the core can be talked to: work shown as live is live only then. */
export const isConnected = (state: CoreState): boolean => state.phase === 'connected'

/**
 * What a connected event stream means for what the page holds. `seen` is the stream the page
 * was on before, if any.
 * - `all`: another stream than before, so events in between were missed: read everything again.
 * - `failed`: the page's first stream: only what failed while there was no core needs asking again.
 * - `none`: the stream the page already knew.
 */
export function onConnected(seen: number | undefined, generation: number): 'all' | 'failed' | 'none' {
  if (seen === undefined) return 'failed'
  return seen === generation ? 'none' : 'all'
}

/** The sidebar's line while there is no core to talk to; undefined when connected. */
export function connectionLabel(state: CoreState): string | undefined {
  switch (state.phase) {
    case 'connected':
      return undefined
    case 'starting':
      return 'Starting…'
    case 'reconnecting':
      return 'Reconnecting to the core…'
    case 'failed':
      return 'The core is not running'
  }
}

/**
 * Keeps a list from going back in time. A response that was on its way while an event changed
 * the same query may be older than the event, and would overwrite it for good (a finished job
 * shown as running). `touched(key)` is called after an event is applied: if that query is being
 * fetched right now, it is read once more as soon as the fetch settles.
 */
export function guardFreshness(qc: QueryClient): { touched: (key: QueryKey) => void; stop: () => void } {
  const doubtful = new Set<string>()
  const stop = qc.getQueryCache().subscribe((ev) => {
    if (ev.type !== 'updated' || !doubtful.has(ev.query.queryHash)) return
    const a = ev.action
    // Data set by hand (an event applied) is not the fetch settling.
    const settled = (a.type === 'success' && !a.manual) || a.type === 'error'
    if (!settled) return
    doubtful.delete(ev.query.queryHash)
    const queryKey = ev.query.queryKey
    queueMicrotask(() => void qc.invalidateQueries({ queryKey, exact: true }))
  })
  return {
    touched(key) {
      for (const q of qc.getQueryCache().findAll({ queryKey: key })) {
        if (q.state.fetchStatus === 'fetching') doubtful.add(q.queryHash)
      }
    },
    stop,
  }
}
