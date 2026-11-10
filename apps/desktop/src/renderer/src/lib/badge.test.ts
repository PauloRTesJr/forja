import { describe, expect, it } from 'vitest'
import type { Job } from './api'
import { queueBadge } from './badge'

const job = (id: string, status: Job['status'], extra: Partial<Job> = {}): Job => ({
  id, name: id, preset: 'p', input: 'i', priority: 'batch', status, created_at: `2026-01-01T00:00:0${id.length}Z`, stages: [], ...extra,
})

describe('queueBadge', () => {
  it('counts what is running, queued or waiting and says it as the Queue does', () => {
    const jobs = [
      job('r', 'running'),
      job('q1', 'queued'),
      job('q2', 'queued'),
      job('w', 'waiting', { sheet: { found: 3 } as unknown as Job['sheet'] }),
      job('done', 'succeeded'),
    ]
    expect(queueBadge(jobs)).toEqual({ count: 4, label: '1 forging, 2 queued, 1 needs you' })
  })
  it('is empty without jobs', () => {
    expect(queueBadge(undefined).count).toBe(0)
  })
})
