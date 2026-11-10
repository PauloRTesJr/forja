import { afterEach, describe, expect, it, vi } from 'vitest'
import { about, ago, clockNow, clockTimers, duration, secondsSince, stopwatch, watchClock } from './time'

describe('duration', () => {
  it('carries seconds into minutes and minutes into hours', () => {
    expect(duration(0.25)).toBe('250 ms')
    expect(duration(0.9999)).toBe('1.0 s')
    expect(duration(4.2)).toBe('4.2 s')
    expect(duration(9.97)).toBe('10 s')
    expect(duration(59.7)).toBe('1 min')
    expect(duration(95)).toBe('1 min 35 s')
    expect(duration(119.6)).toBe('2 min')
    expect(duration(3700)).toBe('1 h 2 min')
    expect(duration(7200)).toBe('2 h')
  })

  it('has nothing to say about no time at all', () => {
    expect(duration(0)).toBe('—')
    expect(duration(Number.NaN)).toBe('—')
  })
})

describe('stopwatch and estimates', () => {
  it('counts like a stopwatch', () => {
    expect(stopwatch(0)).toBe('0:00')
    expect(stopwatch(42.9)).toBe('0:42')
    expect(stopwatch(725)).toBe('12:05')
    expect(stopwatch(3723)).toBe('1:02:03')
  })

  it('rounds what is left to what an estimate can say', () => {
    expect(about(2)).toBe('about 5 s')
    expect(about(38)).toBe('about 40 s')
    expect(about(95)).toBe('about 2 min')
    expect(about(4000)).toBe('about 1 h 5 min')
  })
})

describe('ago', () => {
  const now = new Date(2026, 10, 9, 15, 0, 0).getTime()
  const before = (s: number): string => new Date(now - s * 1000).toISOString()

  it('moves from just now to minutes to hours', () => {
    expect(ago(before(10), now)).toBe('just now')
    expect(ago(before(50), now)).toBe('1 min ago')
    expect(ago(before(108), now)).toBe('2 min ago')
    expect(ago(before(3500), now)).toBe('58 min ago')
    expect(ago(before(3590), now)).toBe('1 h ago')
    expect(ago(before(5 * 3600), now)).toBe('5 h ago')
  })

  it('says yesterday for the day before, whatever the hour', () => {
    expect(ago(new Date(2026, 10, 8, 9, 0, 0).toISOString(), new Date(2026, 10, 9, 23, 0, 0).getTime())).toBe('yesterday')
  })

  it('measures from a start that may not be known', () => {
    expect(secondsSince(undefined, now)).toBeUndefined()
    expect(secondsSince(before(42), now)).toBe(42)
    expect(secondsSince(before(-5), now)).toBe(0)
  })
})

describe('the shared clock', () => {
  afterEach(() => vi.useRealTimers())

  it('runs one timer for every label, at the pace of the fastest, and none when nobody watches', () => {
    vi.useFakeTimers()
    vi.setSystemTime(1_000_000)
    const slow = vi.fn()
    const fast = vi.fn()
    expect(clockTimers()).toBe(0)
    const stopSlow = watchClock(slow, 30_000)
    const stopFast = watchClock(fast, 1000)
    expect(clockTimers()).toBe(1)
    expect(vi.getTimerCount()).toBe(1)

    vi.advanceTimersByTime(3000)
    expect(fast).toHaveBeenCalledTimes(3)
    expect(slow).toHaveBeenCalledTimes(3)
    expect(clockNow()).toBe(1_003_000)

    stopFast()
    vi.advanceTimersByTime(30_000)
    expect(slow).toHaveBeenCalledTimes(4)
    stopSlow()
    expect(vi.getTimerCount()).toBe(0)
  })
})
