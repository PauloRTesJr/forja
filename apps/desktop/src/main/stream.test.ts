import { describe, expect, it } from 'vitest'
import { backoff, Frames, Sequence } from './stream'

const frame = (seq: number, type = 'job.updated'): string => `data: ${JSON.stringify({ seq, type, data: { id: 'a' } })}\n\n`

describe('Frames', () => {
  it('returns each event once, however the stream is cut into chunks', () => {
    const text = frame(1) + ': keepalive\n\n' + frame(2) + frame(3)
    for (const size of [1, 7, 64, text.length]) {
      const frames = new Frames()
      const seen: number[] = []
      for (let i = 0; i < text.length; i += size) seen.push(...frames.push(text.slice(i, i + size)).events.map((e) => e.seq))
      expect(seen).toEqual([1, 2, 3])
    }
  })

  it('keeps a frame that is not JSON from stopping the stream, and counts it', () => {
    const frames = new Frames()
    const got = frames.push('data: {"seq": 1, "type"\n\n' + frame(2))
    expect(got.events.map((e) => e.seq)).toEqual([2])
    expect(got.bad).toBe(1)
  })

  it('does not count keepalives as bad frames', () => {
    expect(new Frames().push(': keepalive\n\n')).toEqual({ events: [], bad: 0 })
  })

  it('reads frames that end in CRLF', () => {
    expect(new Frames().push('data: {"seq":4,"type":"x","data":null}\r\n\r\n').events).toEqual([{ seq: 4, type: 'x', data: null }])
  })
})

describe('Sequence', () => {
  it('takes consecutive numbers as nothing lost', () => {
    const seq = new Sequence()
    expect([5, 6, 7].map((n) => seq.lost(n))).toEqual([false, false, false])
  })

  it('notices a gap', () => {
    const seq = new Sequence()
    seq.lost(1)
    expect(seq.lost(3)).toBe(true)
    expect(seq.lost(4)).toBe(false)
  })

  it('notices numbering that started over (another core)', () => {
    const seq = new Sequence()
    seq.lost(41)
    expect(seq.lost(1)).toBe(true)
  })

  it('starts fresh after reset', () => {
    const seq = new Sequence()
    seq.lost(9)
    seq.reset()
    expect(seq.lost(1)).toBe(false)
  })
})

describe('backoff', () => {
  it('grows from 100 ms to 3 s and stays there', () => {
    expect([0, 1, 2, 5, 20].map(backoff)).toEqual([100, 200, 400, 3000, 3000])
  })
})
