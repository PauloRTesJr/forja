import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { Asset } from '../lib/api'

// A plain stub, not vi.fn: vitest reports a spy's rejected promise even after the code catches it.
type Outcome = { load?: Error; blank?: boolean; lost?: boolean }
const made: FakeViewer[] = []
let script: Outcome[] = []
let fallback: Outcome = {}

class FakeViewer {
  loads: string[] = []
  disposed = false
  lost = false
  private outcome: Outcome
  constructor() {
    this.outcome = script.shift() ?? fallback
    made.push(this)
  }
  async load(url: string): Promise<void> {
    this.loads.push(url)
    if (this.outcome.load) throw this.outcome.load
  }
  async thumbnail(): Promise<Blob> {
    if (this.outcome.lost) this.lost = true
    if (this.outcome.blank || this.outcome.lost) throw Object.assign(new Error('nothing drawn'), { name: 'BlankFrameError' })
    return new Blob(['picture'])
  }
  dispose(): void {
    this.disposed = true
  }
}
vi.mock('./scene', () => ({
  Viewer: FakeViewer,
  isBlank: (rgba: ArrayLike<number>) => Array.from(rgba).every((v, i) => i % 4 !== 3 || v === 0),
}))
vi.mock('../lib/api', () => ({
  CORE: 'forja://core',
  artifactURL: (hash: string) => `forja://core/v1/artifacts/${hash}`,
}))
vi.stubGlobal('document', { createElement: () => ({ style: {}, remove: () => undefined }), body: { appendChild: () => undefined } })
vi.spyOn(console, 'warn').mockImplementation(() => undefined)

const puts: string[] = []
let stored = { length: 0, alpha: 0 }
vi.stubGlobal('fetch', async (url: string, init?: RequestInit) => {
  if (init?.method === 'PUT') {
    puts.push(url)
    const id = /assets\/([^/]+)\//.exec(url)![1]
    return { ok: true, status: 200, json: async () => ({ id, glb: 'blake3:glb', thumbnail: `blake3:thumb-${puts.length}` }) }
  }
  if (init?.method === 'HEAD') return { ok: true, headers: { get: () => String(stored.length) } }
  return { ok: true, blob: async () => new Blob([]) }
})
vi.stubGlobal('createImageBitmap', async () => ({ close: () => undefined }))
vi.stubGlobal(
  'OffscreenCanvas',
  class {
    getContext() {
      return { drawImage: () => undefined, getImageData: () => ({ data: [0, 0, 0, stored.alpha] }) }
    }
  },
)

const { ensureThumbnail, remakeThumbnail, subscribeThumbnailErrors, thumbnailError, thumbnailSize, verifyThumbnail } = await import('./thumbnails')

let serial = 0
/** A new asset each time: the module remembers failures and checks per asset and model. */
const asset = (over: Partial<Asset> = {}): Asset => ({ id: `a${++serial}`, glb: `blake3:glb-${serial}`, thumbnail: '', ...over }) as Asset
const settle = async (): Promise<void> => {
  await vi.advanceTimersByTimeAsync(50)
}

describe('thumbnails', () => {
  beforeEach(() => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    made.length = 0
    puts.length = 0
    script = []
    fallback = {}
    stored = { length: 0, alpha: 0 }
  })
  // Long enough for the renderer to give its context back, so each test starts without one.
  afterEach(async () => {
    await vi.advanceTimersByTimeAsync(10_000)
    vi.useRealTimers()
  })

  it('remembers a model that fails to load instead of retrying it on every refresh', async () => {
    fallback = { load: new SyntaxError('Unexpected token N in JSON') }
    const a = asset()
    const changed = vi.fn()
    const unsubscribe = subscribeThumbnailErrors(changed)
    ensureThumbnail(a, () => undefined)
    await settle()
    expect(thumbnailError(a)).toBe('Unexpected token N in JSON')
    expect(changed).toHaveBeenCalledOnce()
    ensureThumbnail(a, () => undefined)
    await settle()
    expect(made.flatMap((v) => v.loads)).toHaveLength(1)
    expect(puts).toHaveLength(0)
    unsubscribe()
  })

  it('tries again once the asset points at a different model', async () => {
    fallback = { load: new Error('still broken') }
    const a = asset()
    ensureThumbnail(a, () => undefined)
    await settle()
    const repaired = { ...a, glb: 'blake3:repaired' }
    ensureThumbnail(repaired, () => undefined)
    await settle()
    expect(made.flatMap((v) => v.loads).at(-1)).toBe('forja://core/v1/artifacts/blake3:repaired')
    expect(thumbnailError(repaired)).toBe('still broken')
  })

  it('draws again on a new context when the browser took the first one away', async () => {
    script = [{ lost: true }, {}]
    const done = vi.fn()
    const a = asset()
    ensureThumbnail(a, done)
    await settle()
    expect(made).toHaveLength(2)
    expect(made[0]!.disposed).toBe(true)
    expect(puts).toEqual([`forja://core/v1/assets/${a.id}/thumbnail`])
    expect(done).toHaveBeenCalledWith(expect.objectContaining({ id: a.id, thumbnail: 'blake3:thumb-1' }))
    expect(thumbnailError(a)).toBeUndefined()
  })

  it('never stores an empty picture', async () => {
    fallback = { blank: true }
    const a = asset()
    ensureThumbnail(a, () => undefined)
    await settle()
    // One more try on a fresh context, then it is the model's failure.
    expect(made).toHaveLength(2)
    expect(puts).toHaveLength(0)
    expect(thumbnailError(a)).toBe('nothing drawn')
  })

  it('gives its context back once there is no more work', async () => {
    ensureThumbnail(asset(), () => undefined)
    await vi.advanceTimersByTimeAsync(100)
    expect(made).toHaveLength(1)
    expect(made[0]!.disposed).toBe(false)
    // More work before the wait is over keeps the same context.
    ensureThumbnail(asset(), () => undefined)
    await vi.advanceTimersByTimeAsync(1900)
    expect(made).toHaveLength(1)
    expect(made[0]!.disposed).toBe(false)
    await vi.advanceTimersByTimeAsync(2100)
    expect(made[0]!.disposed).toBe(true)
  })

  it('makes a stored thumbnail again when it shows nothing, once', async () => {
    stored = { length: 238, alpha: 0 }
    const a = asset({ thumbnail: 'blake3:blank' })
    const done = vi.fn()
    verifyThumbnail(a, done)
    await settle()
    expect(puts).toEqual([`forja://core/v1/assets/${a.id}/thumbnail`])
    expect(done).toHaveBeenCalledOnce()
    verifyThumbnail(a, done)
    verifyThumbnail({ ...a, thumbnail: 'blake3:blank-again' }, done)
    await settle()
    expect(puts).toHaveLength(1)
  })

  it('leaves a stored thumbnail that shows something alone', async () => {
    stored = { length: 238, alpha: 255 }
    verifyThumbnail(asset({ thumbnail: 'blake3:small-but-real' }), () => undefined)
    stored = { length: 30_000, alpha: 0 }
    verifyThumbnail(asset({ thumbnail: 'blake3:big' }), () => undefined)
    await settle()
    expect(made).toHaveLength(0)
    expect(puts).toHaveLength(0)
  })

  it('makes a thumbnail again when asked, whatever the asset has', async () => {
    const a = asset({ thumbnail: 'blake3:old' })
    const done = vi.fn()
    remakeThumbnail(a, done)
    await settle()
    expect(puts).toHaveLength(1)
    expect(done).toHaveBeenCalledOnce()
  })
})

describe('thumbnailSize', () => {
  it('follows the pixel ratio of the display, in steps of 64, within limits', () => {
    expect(thumbnailSize(1)).toBe(256)
    expect(thumbnailSize(1.5)).toBe(384)
    expect(thumbnailSize(2)).toBe(512)
    expect(thumbnailSize(4)).toBe(512)
    expect(thumbnailSize(0.5)).toBe(256)
    expect(thumbnailSize(NaN)).toBe(256)
    expect(thumbnailSize(undefined as unknown as number)).toBe(256)
  })
})
