// forja://core/<path> proxies to the core's HTTP API over the local socket. The renderer
// uses plain fetch() and three.js loaders against it; bytes stream, never buffer in main.
import { Readable } from 'node:stream'
import { protocol } from 'electron'
import { CoreUnavailable, type Core } from './core'
import { unavailable } from './state'

export const SCHEME = 'forja'
/** The one host the scheme has: the core. */
export const HOST = 'core'

export function registerScheme(): void {
  protocol.registerSchemesAsPrivileged([
    { scheme: SCHEME, privileges: { standard: true, secure: true, supportFetchAPI: true, stream: true, corsEnabled: true } },
  ])
}

const hopByHop = new Set(['connection', 'keep-alive', 'transfer-encoding', 'upgrade'])

export function handleScheme(core: Core): void {
  protocol.handle(SCHEME, async (request) => {
    const url = new URL(request.url)
    if (url.host !== HOST) return Response.json({ title: 'Not Found', status: 404, detail: `no such host: ${url.host}` }, { status: 404 })
    const headers: Record<string, string> = {}
    request.headers.forEach((v, k) => {
      if (!hopByHop.has(k)) headers[k] = v
    })
    const body = request.body ? Readable.fromWeb(request.body as import('node:stream/web').ReadableStream) : undefined
    try {
      const res = await core.request(request.method, url.pathname + url.search, headers, body)
      const out = new Headers()
      for (const [k, v] of Object.entries(res.headers)) {
        if (v !== undefined && !hopByHop.has(k)) out.set(k, Array.isArray(v) ? v.join(', ') : v)
      }
      const status = res.statusCode ?? 502
      const stream = status === 204 || status === 304 ? null : (Readable.toWeb(res) as ReadableStream)
      return new Response(stream, { status, headers: out })
    } catch (err) {
      // The page shows `detail` as it is: it says what state the core is in, never a pipe's name.
      const problem = err instanceof CoreUnavailable ? err.problem : unavailable(core.current().phase, core.current().message)
      return new Response(JSON.stringify(problem), { status: 503, headers: { 'content-type': 'application/problem+json' } })
    }
  })
}
