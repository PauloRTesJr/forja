import { QueryClient, QueryObserver } from '@tanstack/react-query'
import { describe, expect, it } from 'vitest'
import { connectionLabel, guardFreshness, isConnected, onConnected } from './connection'
import { guardDrops } from './dropguard'

describe('onConnected', () => {
  it("asks again only for what failed on the page's first stream", () => {
    expect(onConnected(undefined, 1)).toBe('failed')
    expect(onConnected(undefined, 7)).toBe('failed')
  })
  it('reads everything again on another stream', () => {
    expect(onConnected(1, 2)).toBe('all')
  })
  it('does nothing for the stream it already knew', () => {
    expect(onConnected(2, 2)).toBe('none')
  })
})

describe('connectionLabel', () => {
  it('is silent when connected and plain otherwise', () => {
    expect(connectionLabel({ phase: 'connected', generation: 1 })).toBeUndefined()
    expect(connectionLabel({ phase: 'starting', generation: 0 })).toBe('Starting…')
    expect(connectionLabel({ phase: 'reconnecting', generation: 1 })).toMatch(/Reconnecting/)
    expect(connectionLabel({ phase: 'failed', generation: 1 })).toMatch(/not running/)
  })
})

describe('isConnected', () => {
  it('is true only for a connected core', () => {
    expect(isConnected({ phase: 'connected', generation: 1 })).toBe(true)
    for (const phase of ['starting', 'reconnecting', 'failed'] as const) expect(isConnected({ phase, generation: 1 })).toBe(false)
  })
})

describe('guardFreshness', () => {
  const tick = (ms = 0): Promise<void> => new Promise((r) => setTimeout(r, ms))

  it('reads a list again when an event changed it while a response was on its way', async () => {
    const qc = new QueryClient({ defaultOptions: { queries: { staleTime: Infinity, retry: false } } })
    const fresh = guardFreshness(qc)
    let calls = 0
    let release: (v: string[]) => void = () => {}
    const queryFn = (): Promise<string[]> => {
      calls++
      // The first response is slow and already out of date when it lands.
      return calls === 1 ? new Promise((ok) => (release = ok)) : Promise.resolve(['finished'])
    }
    const first = qc.fetchQuery({ queryKey: ['jobs'], queryFn })
    // Something watches the list, as a screen would: invalidating refetches what is watched.
    const stop = new QueryObserver(qc, { queryKey: ['jobs'], queryFn }).subscribe(() => {})
    await tick()
    qc.setQueryData(['jobs'], ['finished'])
    fresh.touched(['jobs'])
    release(['running'])
    await first
    await tick(20)
    expect(calls).toBe(2)
    expect(qc.getQueryData(['jobs'])).toEqual(['finished'])
    expect(qc.getQueryState(['jobs'])?.fetchStatus).toBe('idle')
    stop()
    fresh.stop()
  })

  it('leaves a query alone when nothing was being fetched', async () => {
    const qc = new QueryClient({ defaultOptions: { queries: { staleTime: Infinity, retry: false } } })
    const fresh = guardFreshness(qc)
    let calls = 0
    await qc.fetchQuery({ queryKey: ['jobs'], queryFn: () => (calls++, Promise.resolve(['a'])) })
    qc.setQueryData(['jobs'], ['b'])
    fresh.touched(['jobs'])
    await tick(20)
    expect(calls).toBe(1)
    expect(qc.getQueryData(['jobs'])).toEqual(['b'])
    fresh.stop()
  })
})

describe('guardDrops', () => {
  type Handler = (e: { defaultPrevented: boolean; preventDefault(): void; dataTransfer: { dropEffect: string } | null }) => void
  function fakeWindow(): { handlers: Record<string, Handler>; addEventListener(t: string, f: Handler): void; removeEventListener(t: string): void } {
    const handlers: Record<string, Handler> = {}
    return { handlers, addEventListener: (t, f) => void (handlers[t] = f), removeEventListener: (t) => void delete handlers[t] }
  }
  const event = (taken: boolean) => {
    const e = { defaultPrevented: taken, dataTransfer: { dropEffect: 'copy' }, preventDefault: () => void (e.defaultPrevented = true) }
    return e
  }

  it('refuses a drag nobody took, so a dropped file cannot replace the app', () => {
    const w = fakeWindow()
    guardDrops(w)
    const over = event(false)
    w.handlers.dragover!(over)
    expect(over.defaultPrevented).toBe(true)
    expect(over.dataTransfer.dropEffect).toBe('none')
    const drop = event(false)
    w.handlers.drop!(drop)
    expect(drop.defaultPrevented).toBe(true)
  })

  it('leaves a drag that a drop target took as it is', () => {
    const w = fakeWindow()
    guardDrops(w)
    const over = event(true)
    w.handlers.dragover!(over)
    expect(over.dataTransfer.dropEffect).toBe('copy')
  })

  it('stops guarding when asked', () => {
    const w = fakeWindow()
    guardDrops(w)()
    expect(w.handlers).toEqual({})
  })
})
