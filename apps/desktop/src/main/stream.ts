// The pure parts of following the core's event stream (GET /v1/events/stream, server-sent
// events): framing, noticing lost events, and how long to wait before trying again.

export type CoreEvent = { seq: number; type: string; data: unknown }

/** Splits a server-sent event stream into the events it carries, whatever the chunking. */
export class Frames {
  private buf = ''

  /** The events completed by this chunk. A frame that is not an event (a keepalive) or is not JSON is dropped and counted in `bad`. */
  push(chunk: string): { events: CoreEvent[]; bad: number } {
    this.buf += chunk.replaceAll('\r\n', '\n')
    const events: CoreEvent[] = []
    let bad = 0
    let cut: number
    while ((cut = this.buf.indexOf('\n\n')) >= 0) {
      const frame = this.buf.slice(0, cut)
      this.buf = this.buf.slice(cut + 2)
      const data = frame
        .split('\n')
        .filter((line) => line.startsWith('data:'))
        .map((line) => line.slice(5).trimStart())
        .join('\n')
      if (!data) continue
      const ev = parseEvent(data)
      if (ev) events.push(ev)
      else bad++
    }
    return { events, bad }
  }
}

function parseEvent(text: string): CoreEvent | null {
  try {
    const v = JSON.parse(text) as Partial<CoreEvent> | null
    if (v && typeof v.type === 'string' && typeof v.seq === 'number') return { seq: v.seq, type: v.type, data: v.data }
  } catch {
    /* not an event */
  }
  return null
}

/**
 * Says whether events were lost before this one. The core numbers events from 1 and drops them
 * for a subscriber that falls behind, so a number that is not the last plus one means the page
 * holds state it can no longer trust and must read again.
 */
export class Sequence {
  private last: number | undefined

  /** A new connection: whatever number comes first is taken as it is. */
  reset(): void {
    this.last = undefined
  }

  /** True when events between the last one seen and `seq` are missing, or the numbering started over. */
  lost(seq: number): boolean {
    const last = this.last
    this.last = seq
    return last !== undefined && seq !== last + 1
  }
}

/** Milliseconds to wait before the n-th try in a row (0-based): quick at first, then up to 3 s. */
export function backoff(attempt: number): number {
  return Math.min(100 * 2 ** Math.max(0, attempt), 3000)
}

/**
 * The stream is taken for dead after this long without a byte. The core writes a keepalive
 * comment every 15 s, so two missed ones mean the other end is gone even though the pipe never
 * said so.
 */
export const IDLE_MS = 40_000
