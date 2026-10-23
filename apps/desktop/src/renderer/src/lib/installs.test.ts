import { describe, expect, it } from 'vitest'
import type { InstallTask } from './api'
import { upsertTask } from './events'

const task = (id: string, version: number, created_at = 1): InstallTask => ({
  provider: 'local',
  id,
  kind: 'install',
  target: 'triposg',
  status: 'running',
  steps: [],
  progress: null,
  log: [],
  error: null,
  created_at,
  version,
})

describe('upsertTask', () => {
  it('keeps one entry per task, the newest state, newest task first', () => {
    let list = upsertTask(undefined, task('a', 1, 1))
    list = upsertTask(list, task('b', 1, 2))
    list = upsertTask(list, task('a', 3, 1))
    expect(list.map((t) => `${t.id}${t.version}`)).toEqual(['b1', 'a3'])
  })

  it('ignores a state older than the one it has', () => {
    const list = upsertTask([task('a', 5)], task('a', 4))
    expect(list[0]!.version).toBe(5)
  })
})
