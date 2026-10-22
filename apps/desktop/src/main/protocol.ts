// forja://core/<path> proxies to the core's HTTP API over the local socket. The renderer
// uses plain fetch() and three.js loaders against it; bytes stream, never buffer in main.
import { Readable } from 'node:stream'
import { protocol } from 'electron'
import type { Core } from './core'

export const SCHEME = 'forja'

export function registerScheme(): void {
  protocol.registerSchemesAsPrivileged([
    { scheme: SCHEME, privileges: { standard: true, secure: true, supportFetchAPI: true, stream: true, corsEnabled: true } },
  ])
}

const hopByHop = new Set(['connection', 'keep-alive', 'transfer-encoding', 'upgrade'])

export function handleScheme(core: Core): void {
  protocol.handle(SCHEME, async (request) => {
    const url = new URL(request.url)
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
      const detail = err instanceof Error ? err.message : String(err)
      return Response.json({ title: 'Core unreachable', status: 503, detail }, { status: 503 })
    }
  })
}
