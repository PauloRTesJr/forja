// The one way requests reach the core: forja://core, proxied by the main process. Every failure
// becomes an ApiError whose message can be shown to a person as it is.
export const CORE = 'forja://core'

/**
 * - `http`: the core answered with an error; `status` and its own words.
 * - `offline`: no core answered (it is starting, restarting, or gone).
 * - `timeout`: the core did not answer in time.
 */
export type ApiErrorKind = 'http' | 'offline' | 'timeout'

export class ApiError extends Error {
  constructor(
    readonly status: number,
    message: string,
    readonly kind: ApiErrorKind = 'http',
    /** The problem document's other members, such as the license a request must accept. */
    readonly problem?: Record<string, unknown>,
  ) {
    super(message)
    this.name = 'ApiError'
  }
}

const OFFLINE = "Forja's core is not answering. It restarts by itself; try again in a moment."

/** An error answer as an ApiError: the problem document's `detail`, else what the body or the status says. */
export function errorFrom(status: number, statusText: string, body: string): ApiError {
  let doc: Record<string, unknown> | undefined
  try {
    const v: unknown = JSON.parse(body)
    if (v && typeof v === 'object' && !Array.isArray(v)) doc = v as Record<string, unknown>
  } catch {
    /* not a problem document */
  }
  const text = (v: unknown): string | undefined => (typeof v === 'string' && v.trim() ? v.trim() : undefined)
  // The main process answers 503 with a `code` when no core took the request.
  const offline = status === 503 && typeof doc?.code === 'string' && doc.code.startsWith('core-')
  const plain = doc || /^\s*</.test(body) ? undefined : text(body.slice(0, 300))
  const message =
    text(doc?.detail) ?? text(doc?.title) ?? plain ?? text(statusText) ?? (offline ? OFFLINE : `The request failed (${status}).`)
  return new ApiError(status, message, offline ? 'offline' : 'http', doc)
}

export interface RequestOptions {
  /** Ends the request when its caller no longer wants the answer (a query's `signal`). */
  signal?: AbortSignal
  /** Gives up after this long; 0 waits for as long as it takes. */
  timeoutMs?: number
}

/** Reads wait this long by default. Changes run as long as their work does (an export, an install). */
export const READ_TIMEOUT_MS = 90_000

/** fetch() against the core that only ever fails with an ApiError, or with the caller's own abort. */
export async function request(method: string, path: string, init: { headers?: Record<string, string>; body?: BodyInit }, opts: RequestOptions = {}): Promise<Response> {
  const ms = opts.timeoutMs ?? (method === 'GET' ? READ_TIMEOUT_MS : 0)
  const timeout = ms > 0 ? AbortSignal.timeout(ms) : undefined
  const signals = [opts.signal, timeout].filter((s): s is AbortSignal => s !== undefined)
  const signal = signals.length > 1 ? AbortSignal.any(signals) : signals[0]
  let res: Response
  try {
    res = await fetch(CORE + path, { method, ...init, signal })
  } catch (err) {
    if (opts.signal?.aborted) throw err
    if (timeout?.aborted) throw new ApiError(0, 'The core took too long to answer. Try again.', 'timeout')
    throw new ApiError(0, OFFLINE, 'offline')
  }
  if (!res.ok) throw errorFrom(res.status, res.statusText, await res.text().catch(() => ''))
  return res
}

/** A JSON request to the core; resolves with the decoded answer, or undefined when it has no body. */
export async function api<T>(method: string, path: string, body?: unknown, opts?: RequestOptions): Promise<T> {
  const res = await request(
    method,
    path,
    body === undefined ? {} : { headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) },
    opts,
  )
  const text = await res.text()
  if (!text) return undefined as T
  try {
    return JSON.parse(text) as T
  } catch {
    throw new ApiError(res.status, 'The core sent an answer the app could not read.')
  }
}

/**
 * Whether a failed read is worth another try: only when no core answered, which mends itself
 * (the app restarts its core). An answer such as 404 or 422 will not change by asking again.
 */
export function shouldRetry(failures: number, err: unknown): boolean {
  return err instanceof ApiError && err.kind === 'offline' && failures < 3
}
