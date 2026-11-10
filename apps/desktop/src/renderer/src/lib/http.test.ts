import { afterEach, describe, expect, it, vi } from 'vitest'
import { api, ApiError, errorFrom, request, shouldRetry } from './http'

afterEach(() => vi.unstubAllGlobals())

describe('errorFrom', () => {
  it("uses the problem document's detail", () => {
    const err = errorFrom(422, 'Unprocessable Entity', '{"type":"about:blank","title":"Unprocessable Entity","status":422,"detail":"no model for the shape stage","license":"mit"}')
    expect(err).toMatchObject({ status: 422, kind: 'http', message: 'no model for the shape stage' })
    expect(err.problem?.license).toBe('mit')
  })

  it('falls back to the title, then to a plain-text body, then to the status', () => {
    expect(errorFrom(409, 'Conflict', '{"title":"Conflict"}').message).toBe('Conflict')
    expect(errorFrom(404, 'Not Found', '404 page not found\n').message).toBe('404 page not found')
    expect(errorFrom(500, 'Internal Server Error', '').message).toBe('Internal Server Error')
    expect(errorFrom(500, '', '').message).toBe('The request failed (500).')
  })

  it('never shows a page of HTML', () => {
    expect(errorFrom(502, 'Bad Gateway', '<html><body>nginx</body></html>').message).toBe('Bad Gateway')
  })

  it("knows the main process's answer for a core that is away", () => {
    const err = errorFrom(503, 'Service Unavailable', '{"title":"Core unavailable","status":503,"detail":"Forja\'s core is still starting. Try again in a moment.","code":"core-starting"}')
    expect(err.kind).toBe('offline')
    expect(err.message).toMatch(/still starting/)
    // A 503 the core itself sent is an answer, not an absence.
    expect(errorFrom(503, 'Service Unavailable', '{"detail":"the engine is busy"}').kind).toBe('http')
  })
})

describe('request', () => {
  it('turns a failed fetch into an offline error with words for a person', async () => {
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new TypeError('Failed to fetch')))
    const err = await api('GET', '/v1/jobs').catch((e: unknown) => e)
    expect(err).toBeInstanceOf(ApiError)
    expect(err).toMatchObject({ kind: 'offline', status: 0 })
    expect((err as ApiError).message).not.toMatch(/fetch|ENOENT|pipe/)
  })

  it('gives up after its timeout', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn((_url: string, init: RequestInit) => new Promise((_ok, fail) => init.signal!.addEventListener('abort', () => fail(init.signal!.reason)))),
    )
    const err = await request('GET', '/v1/models', {}, { timeoutMs: 20 }).catch((e: unknown) => e)
    expect(err).toMatchObject({ kind: 'timeout' })
  })

  it("passes the caller's own abort through untouched", async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn((_url: string, init: RequestInit) => new Promise((_ok, fail) => init.signal!.addEventListener('abort', () => fail(init.signal!.reason)))),
    )
    const ctl = new AbortController()
    const pending = request('GET', '/v1/assets', {}, { signal: ctl.signal }).catch((e: unknown) => e)
    ctl.abort()
    const err = await pending
    expect(err).not.toBeInstanceOf(ApiError)
    expect((err as Error).name).toBe('AbortError')
  })

  it('sets no timeout on a change unless asked', async () => {
    const fetched = vi.fn(async (_url: string, _init: RequestInit) => new Response('{"ok":true}', { status: 200 }))
    vi.stubGlobal('fetch', fetched)
    await api('POST', '/v1/export', { ids: [] })
    expect(fetched.mock.calls[0]![1].signal).toBeUndefined()
    await api('GET', '/v1/jobs')
    expect(fetched.mock.calls[1]![1].signal).toBeInstanceOf(AbortSignal)
  })

  it('decodes JSON and takes an empty body as nothing', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValueOnce(new Response('[1,2]', { status: 200 })).mockResolvedValueOnce(new Response(null, { status: 204 })).mockResolvedValueOnce(new Response('', { status: 200 })))
    expect(await api('GET', '/a')).toEqual([1, 2])
    expect(await api('DELETE', '/b')).toBeUndefined()
    expect(await api('GET', '/c')).toBeUndefined()
  })

  it('throws what the core said for an error status', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response('{"detail":"job is not running"}', { status: 409, statusText: 'Conflict' })))
    await expect(api('DELETE', '/v1/jobs/x')).rejects.toMatchObject({ status: 409, message: 'job is not running' })
  })
})

describe('shouldRetry', () => {
  it('retries only when no core answered, and not for ever', () => {
    const offline = new ApiError(0, 'x', 'offline')
    expect(shouldRetry(0, offline)).toBe(true)
    expect(shouldRetry(2, offline)).toBe(true)
    expect(shouldRetry(3, offline)).toBe(false)
    expect(shouldRetry(0, new ApiError(404, 'no asset'))).toBe(false)
    expect(shouldRetry(0, new ApiError(0, 'slow', 'timeout'))).toBe(false)
    expect(shouldRetry(0, new Error('bug'))).toBe(false)
  })
})
