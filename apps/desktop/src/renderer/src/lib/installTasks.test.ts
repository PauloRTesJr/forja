import { describe, expect, it } from 'vitest'
import type { InstallTask } from './api'
import { canRetry, stepProgress, stopped, taskFor, taskPhase } from './installTasks'

const steps = (...status: InstallTask['steps'][number]['status'][]): InstallTask['steps'] => {
  const all = [
    ['checks', 'Check this computer'],
    ['environment', 'Python environment'],
    ['upstream', 'Model code'],
    ['build', 'GPU extensions'],
    ['weights', 'Weights'],
    ['smoke', 'Test on this device'],
  ] as const
  return all.map(([id, label], i) => ({ id, label, status: status[i] ?? 'pending' }))
}

const task = (over: Partial<InstallTask> = {}): InstallTask => ({
  provider: 'local',
  id: 't1',
  kind: 'install',
  target: 'triposg',
  status: 'running',
  steps: steps(),
  progress: null,
  log: [],
  error: null,
  created_at: 1,
  version: 1,
  ...over,
})

describe('taskFor', () => {
  it('prefers the running task over a newer finished one', () => {
    const list = [task({ id: 'new', status: 'failed', created_at: 3 }), task({ id: 'run', created_at: 2 })]
    expect(taskFor(list, 'install', 'triposg')?.id).toBe('run')
    expect(taskFor([list[0]!], 'install', 'triposg')?.id).toBe('new')
  })

  it('keeps kinds and targets apart', () => {
    const list = [task({ id: 'rm', kind: 'remove' }), task({ id: 'other', target: 'birefnet' })]
    expect(taskFor(list, 'install', 'triposg')).toBeUndefined()
    expect(taskFor(list, 'remove', 'triposg')?.id).toBe('rm')
  })

  it('never shows another computer’s install on this computer’s row', () => {
    const list = [task({ id: 'studio', provider: 'lan-1' })]
    expect(taskFor(list, 'install', 'triposg', 'local')).toBeUndefined()
    expect(taskFor(list, 'install', 'triposg', 'lan-1')?.id).toBe('studio')
    expect(taskFor(list, 'install', 'triposg')?.id).toBe('studio')
  })
})

describe('taskPhase', () => {
  it('follows the task, not the request that started it', () => {
    expect(taskPhase(undefined)).toBe('idle')
    expect(taskPhase(task({ status: 'queued' }))).toBe('queued')
    expect(taskPhase(task())).toBe('running')
    expect(taskPhase(task({ status: 'succeeded' }))).toBe('succeeded')
    expect(taskPhase(task({ status: 'cancelled' }))).toBe('cancelled')
    expect(taskPhase(task({ status: 'failed', error: 'command failed with exit code 1: uv pip' }))).toBe('failed')
  })

  it('is cancelling from the moment cancel is asked until the engine stops', () => {
    expect(taskPhase(task(), true)).toBe('cancelling')
    expect(taskPhase(task({ status: 'queued' }), true)).toBe('cancelling')
    expect(taskPhase(task({ status: 'cancelled' }), true)).toBe('cancelled')
    // It finished before the cancel landed: that is what happened.
    expect(taskPhase(task({ status: 'succeeded' }), true)).toBe('succeeded')
  })

  it('calls a task the core lost track of interrupted, not failed', () => {
    const lost = 'lost contact with the provider: lost the progress of task 4f2a: Get "http://127.0.0.1:1/v1/tasks/4f2a/events": EOF'
    expect(taskPhase(task({ status: 'failed', error: lost }))).toBe('interrupted')
    expect(taskPhase(task({ status: 'interrupted' as InstallTask['status'] }))).toBe('interrupted')
  })

  it('offers another try after a stop, never while it runs or once it is done', () => {
    expect((['failed', 'interrupted', 'cancelled'] as const).every(canRetry)).toBe(true)
    expect((['idle', 'queued', 'running', 'cancelling', 'succeeded'] as const).some(canRetry)).toBe(false)
  })
})

describe('stepProgress', () => {
  it('counts the step among those the install does not skip', () => {
    const t = task({ steps: steps('done', 'done', 'skipped', 'skipped', 'running') })
    expect(stepProgress(t)).toMatchObject({ index: 3, count: 4, label: 'Weights', text: 'Step 3 of 4 · Weights' })
  })

  it('has a fraction only while the running step reports bytes', () => {
    const t = task({ steps: steps('done', 'done', 'done', 'done', 'running'), progress: { step: 'weights', done: 512 * 1024 * 1024, total: 2 * 1024 ** 3 } })
    expect(stepProgress(t)).toMatchObject({ fraction: 0.25, text: 'Step 5 of 6 · Weights · 512 MB of 2.0 GB' })
    // Bytes left over from another step, or a total the hub would not say, are no measure.
    expect(stepProgress({ ...t, steps: steps('done', 'running') })?.fraction).toBeUndefined()
    expect(stepProgress({ ...t, progress: { step: 'weights', done: 5, total: 0 } })?.fraction).toBeUndefined()
  })

  it('has nothing to say between steps or after the end', () => {
    expect(stepProgress(task({ status: 'queued' }))).toBeUndefined()
    expect(stepProgress(task({ status: 'succeeded', steps: steps('done', 'done', 'done', 'done', 'done', 'done') }))).toBeUndefined()
  })

  it('does not number a single step', () => {
    const t = task({ kind: 'remove', steps: [{ id: 'remove', label: 'Remove files', status: 'running' }] })
    expect(stepProgress(t)?.text).toBe('Remove files')
  })
})

describe('stopped', () => {
  it('names the step a failed install stopped at and says it can continue', () => {
    const t = task({ status: 'failed', steps: steps('done', 'done', 'done', 'failed'), error: 'command failed with exit code 1' })
    expect(stopped(t, 'failed')).toEqual({
      title: 'The install stopped at "GPU extensions".',
      next: 'What was downloaded is kept: installing again continues from there.',
    })
  })

  it('says a cancelled install was cancelled and keeps its files', () => {
    expect(stopped(task({ status: 'cancelled' }), 'cancelled')).toMatchObject({ title: 'The install was cancelled.' })
    expect(stopped(task({ status: 'cancelled' }), 'cancelled')?.next).toMatch(/continues/)
  })

  it('blames the engine, not the model, for an interrupted task', () => {
    expect(stopped(task({ status: 'failed' }), 'interrupted')?.title).toBe('The engine stopped during the install.')
    expect(stopped(task({ kind: 'fix', status: 'failed' }), 'interrupted')).toEqual({ title: 'The engine stopped during the fix.', next: 'Run it again.' })
  })

  it('promises no resume for a removal or a fix', () => {
    expect(stopped(task({ kind: 'remove', status: 'failed' }), 'failed')).toEqual({ title: 'The removal failed.', next: undefined })
  })

  it('says nothing while a task runs or after it succeeded', () => {
    expect(stopped(task(), 'running')).toBeUndefined()
    expect(stopped(task({ status: 'succeeded' }), 'succeeded')).toBeUndefined()
  })
})
