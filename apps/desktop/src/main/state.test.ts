import { describe, expect, it } from 'vitest'
import { describeFailure, StartError, unavailable } from './state'

describe('describeFailure', () => {
  it('quotes what the core said when it gave up', () => {
    const err = new StartError('exited', {
      code: 1,
      stderr: ['time=... level=INFO msg="starting"', 'Error: another Forja core already runs on this data folder'],
    })
    const got = describeFailure(err, true)
    expect(got.message).toBe("Forja's core could not start: another Forja core already runs on this data folder")
    expect(got.detail).toContain('level=INFO')
  })

  it('falls back to the exit code when the core said nothing', () => {
    expect(describeFailure(new StartError('exited', { code: 3, stderr: [] }), true).message).toBe("Forja's core stopped while starting (exit code 3).")
  })

  it('tells a user to reinstall and a developer to build', () => {
    const err = new StartError('missing', { bin: '/x/forja' })
    expect(describeFailure(err, true).message).toMatch(/Install Forja again/)
    expect(describeFailure(err, true).message).not.toMatch(/make/)
    expect(describeFailure(err, false).message).toMatch(/make core/)
  })

  it('never shows a bare socket error', () => {
    const got = describeFailure(new Error('connect ENOENT pipe forja-user'), true)
    expect(got.message).toBe("Forja's core could not be reached.")
    expect(got.detail).toContain('ENOENT')
  })
})

describe('unavailable', () => {
  it('is a 503 problem whose code names the state', () => {
    expect(unavailable('starting')).toMatchObject({ status: 503, code: 'core-starting' })
    expect(unavailable('reconnecting').detail).toMatch(/Reconnecting/)
    expect(unavailable('failed', 'because').detail).toBe('because')
  })
})
