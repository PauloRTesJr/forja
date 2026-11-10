import { describe, expect, it } from 'vitest'
import { quitQuestion } from './quit'

describe('quitQuestion', () => {
  it('asks nothing when no work would be lost', () => {
    expect(quitQuestion([], [])).toBeNull()
    expect(quitQuestion([{ status: 'succeeded' }, { status: 'failed' }, { status: 'waiting' }], [{ status: 'succeeded', kind: 'install' }])).toBeNull()
    expect(quitQuestion(null, undefined)).toBeNull()
  })

  it('does not ask for jobs that only wait for their turn: the core keeps them', () => {
    expect(quitQuestion([{ status: 'queued' }, { status: 'queued' }], [])).toBeNull()
  })

  it('counts the jobs that are running, and says what becomes of the queued ones', () => {
    const q = quitQuestion([{ status: 'running' }, { status: 'queued' }, { status: 'queued' }, { status: 'succeeded' }], [])
    expect(q?.message).toBe('1 job is still generating.')
    expect(q?.detail).toBe(
      'Quitting Forja stops this work. The job is marked as interrupted, and you can retry it from the Queue. 2 queued jobs wait and run the next time Forja is open.',
    )
  })

  it('names installs too', () => {
    const q = quitQuestion([{ status: 'running' }, { status: 'running' }], [{ status: 'running', kind: 'install' }, { status: 'queued', kind: 'fix' }])
    expect(q?.message).toBe('2 jobs are still generating, 1 model is still installing and 1 setup task is still running.')
    expect(q?.detail).toMatch(/These jobs are marked as interrupted/)
    expect(q?.detail).toMatch(/starts over/)
  })
})
