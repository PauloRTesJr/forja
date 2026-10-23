import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { Asset } from '../lib/api'

// A plain stub, not vi.fn: vitest reports a spy's rejected promise even after the code catches it.
const loads: string[] = []
let failure = new Error('unset')
vi.mock('./scene', () => ({
  Viewer: class {
    async load(url: string): Promise<never> {
      loads.push(url)
      throw failure
    }
  },
}))
vi.mock('../lib/api', () => ({ CORE: 'forja://core', artifactURL: (hash: string) => `forja://core/v1/artifacts/${hash}` }))
vi.stubGlobal('document', { createElement: () => ({ style: {} }), body: { appendChild: () => undefined } })
vi.spyOn(console, 'warn').mockImplementation(() => undefined)

const { ensureThumbnail, subscribeThumbnailErrors, thumbnailError } = await import('./thumbnails')

const asset = (glb: string): Asset => ({ id: 'a1', glb, thumbnail: '' }) as Asset
const settle = () => new Promise((done) => setTimeout(done, 0))

describe('thumbnails', () => {
  beforeEach(() => {
    loads.length = 0
  })

  it('remembers a model that fails to load instead of retrying it on every refresh', async () => {
    failure = new SyntaxError('Unexpected token N in JSON')
    const changed = vi.fn()
    const unsubscribe = subscribeThumbnailErrors(changed)
    ensureThumbnail(asset('blake3:bad'), () => undefined)
    await settle()
    expect(thumbnailError(asset('blake3:bad'))).toBe('Unexpected token N in JSON')
    expect(changed).toHaveBeenCalledOnce()
    ensureThumbnail(asset('blake3:bad'), () => undefined)
    await settle()
    expect(loads).toHaveLength(1)
    unsubscribe()
  })

  it('tries again once the asset points at a different model', async () => {
    failure = new Error('still broken')
    ensureThumbnail(asset('blake3:repaired'), () => undefined)
    await settle()
    expect(loads).toEqual(['forja://core/v1/artifacts/blake3:repaired'])
    expect(thumbnailError(asset('blake3:repaired'))).toBe('still broken')
  })
})
