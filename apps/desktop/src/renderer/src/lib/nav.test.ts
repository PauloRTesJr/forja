import { describe, expect, it, vi } from 'vitest'
import { transition, type TransitionHost } from './nav'

const host = (over: Partial<TransitionHost>): TransitionHost => ({ hidden: false, reducedMotion: false, ...over })

describe('transition', () => {
  it('animates the change when the window shows and motion is allowed', () => {
    const update = vi.fn()
    const start = vi.fn((cb: () => void) => (cb(), { ready: Promise.resolve(), finished: Promise.resolve() }))
    transition(update, host({ start }))
    expect(start).toHaveBeenCalledOnce()
    expect(update).toHaveBeenCalledOnce()
  })

  it('changes at once, without a transition, in a hidden window', () => {
    const update = vi.fn()
    const start = vi.fn()
    transition(update, host({ hidden: true, start }))
    expect(start).not.toHaveBeenCalled()
    expect(update).toHaveBeenCalledOnce()
  })

  it('changes at once under reduced motion, and where transitions do not exist', () => {
    const update = vi.fn()
    transition(update, host({ reducedMotion: true, start: vi.fn() }))
    transition(update, host({}))
    expect(update).toHaveBeenCalledTimes(2)
  })

  it('leaves no unhandled rejection when the transition is skipped', async () => {
    const unhandled = vi.fn()
    process.on('unhandledRejection', unhandled)
    const skipped = (): Promise<never> => Promise.reject(new DOMException('Transition was skipped', 'AbortError'))
    transition(() => {}, host({ start: (cb) => (cb(), { ready: skipped(), finished: skipped() }) }))
    await new Promise((r) => setTimeout(r, 20))
    process.off('unhandledRejection', unhandled)
    expect(unhandled).not.toHaveBeenCalled()
  })
})
