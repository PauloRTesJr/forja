// What the app knows about its core, as the page shows it, and the words for when it is away.

export type CorePhase = 'starting' | 'connected' | 'reconnecting' | 'failed'

export interface CoreState {
  phase: CorePhase
  /** Counts event-stream connections: a new number means events in between were missed. */
  generation: number
  /** `failed`: what went wrong, for people. */
  message?: string
  /** `failed`: the core's last words, for a bug report. */
  detail?: string
}

/** Why no core could be started or reached. */
export class StartError extends Error {
  constructor(
    readonly kind: 'missing' | 'exited' | 'timeout' | 'address',
    readonly info: { bin?: string; code?: number | null; stderr?: string[]; seconds?: number } = {},
  ) {
    super(kind)
  }
}

// What the core writes last when it gives up: cobra's "Error: ..." or a log line's err="...".
function lastWords(stderr: string[]): string | undefined {
  for (let i = stderr.length - 1; i >= 0; i--) {
    const line = stderr[i]!.trim()
    const said = /^error:\s*(.+)$/i.exec(line)?.[1] ?? /\berr="((?:[^"\\]|\\.)+)"/.exec(line)?.[1]
    if (said) return said
  }
  return undefined
}

/** A failed start in words a user can act on; `detail` keeps the raw output. */
export function describeFailure(err: unknown, packaged: boolean): { message: string; detail?: string } {
  if (!(err instanceof StartError)) {
    return { message: "Forja's core could not be reached.", detail: err instanceof Error ? err.message : String(err) }
  }
  const detail = err.info.stderr?.length ? err.info.stderr.join('\n') : undefined
  switch (err.kind) {
    case 'missing':
      return {
        message: packaged
          ? "A part of Forja is missing from this installation (its core program). Install Forja again."
          : 'The core is not built yet: run "make core" in the repository, then try again.',
        detail: err.info.bin,
      }
    case 'address':
      return { message: "Forja's core program did not run. Install Forja again if this keeps happening.", detail }
    case 'timeout':
      return {
        message: `Forja's core did not answer within ${err.info.seconds ?? 30} seconds. Try again; if it keeps happening, restart the computer.`,
        detail,
      }
    case 'exited': {
      const said = err.info.stderr ? lastWords(err.info.stderr) : undefined
      return {
        message: said ? `Forja's core could not start: ${said}` : `Forja's core stopped while starting (exit code ${err.info.code ?? 'unknown'}).`,
        detail,
      }
    }
  }
}

/** The body the page gets when a request cannot reach the core: a problem document. */
export function unavailable(phase: CorePhase, message?: string): { title: string; status: 503; detail: string; code: string } {
  const detail =
    phase === 'failed'
      ? (message ?? "Forja's core is not running.")
      : phase === 'starting'
        ? "Forja's core is still starting. Try again in a moment."
        : "Forja's core is not answering. Reconnecting…"
  return { title: 'Core unavailable', status: 503, detail, code: `core-${phase === 'connected' ? 'reconnecting' : phase}` }
}
