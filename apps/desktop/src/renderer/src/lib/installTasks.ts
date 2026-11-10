// What an install, removal or fix is doing, in the words the app shows for it.
import { taskDone, type InstallTask } from './api'
import { bytes } from './format'

/**
 * The task for a model or check: the running one if any, else the latest. With a provider, only
 * that computer's tasks count, so a row never shows another computer's install as its own.
 */
export function taskFor(list: InstallTask[] | undefined, kind: InstallTask['kind'], target: string, provider?: string): InstallTask | undefined {
  const mine = (list ?? []).filter((t) => t.kind === kind && t.target === target && (provider === undefined || t.provider === provider))
  return mine.find((t) => !taskDone(t)) ?? mine[0]
}

/**
 * Where a task stands for the user:
 * - `cancelling`: cancel was asked for and the engine has not stopped yet (it finishes a file first).
 * - `interrupted`: the engine or the app went away under it; nothing says it failed by itself.
 */
export type TaskPhase = 'idle' | 'queued' | 'running' | 'cancelling' | 'failed' | 'interrupted' | 'cancelled' | 'succeeded'

// The core ends a task it lost track of with these words (core/internal/installs).
const LOST = /^lost contact with the provider|lost the progress of task/i

export function taskPhase(task: InstallTask | undefined, cancelAsked = false): TaskPhase {
  if (!task) return 'idle'
  // A core that records interrupted installs may say so itself.
  if ((task.status as string) === 'interrupted') return 'interrupted'
  if (!taskDone(task)) return cancelAsked ? 'cancelling' : task.status === 'queued' ? 'queued' : 'running'
  if (task.status === 'failed') return task.error && LOST.test(task.error) ? 'interrupted' : 'failed'
  return task.status === 'cancelled' ? 'cancelled' : 'succeeded'
}

/** A task that stopped before its end and can be started again. */
export const canRetry = (phase: TaskPhase): boolean => phase === 'failed' || phase === 'interrupted' || phase === 'cancelled'

/** The step a task is on, among the steps it does not skip. */
export interface StepProgress {
  index: number
  count: number
  label: string
  /** 0..1 of the step's bytes, when the step reports them; the other steps have no measure. */
  fraction?: number
  text: string
}

export function stepProgress(task: InstallTask): StepProgress | undefined {
  const steps = task.steps.filter((s) => s.status !== 'skipped')
  const at = steps.findIndex((s) => s.status === 'running')
  if (at < 0) return undefined
  const step = steps[at]!
  const p = task.progress
  const counted = p && p.step === step.id && p.total > 0 ? p : undefined
  const parts = [steps.length > 1 ? `Step ${at + 1} of ${steps.length}` : undefined, step.label, counted ? `${bytes(counted.done)} of ${bytes(counted.total)}` : undefined]
  return {
    index: at + 1,
    count: steps.length,
    label: step.label,
    fraction: counted ? Math.min(1, counted.done / counted.total) : undefined,
    text: parts.filter(Boolean).join(' · '),
  }
}

const nouns: Record<InstallTask['kind'], string> = { install: 'install', remove: 'removal', fix: 'fix' }

/**
 * What to say when a task stopped: one sentence on what happened, and one on what to do. The
 * engine's own error is shown beside it, never instead of it.
 */
export function stopped(task: InstallTask, phase: TaskPhase): { title: string; next?: string } | undefined {
  const noun = nouns[task.kind]
  const resume = task.kind === 'install' ? 'What was downloaded is kept: installing again continues from there.' : undefined
  switch (phase) {
    case 'failed': {
      const step = task.steps.find((s) => s.status === 'failed')
      return { title: step ? `The ${noun} stopped at "${step.label}".` : `The ${noun} failed.`, next: resume }
    }
    case 'interrupted':
      return { title: `The engine stopped during the ${noun}.`, next: resume ?? 'Run it again.' }
    case 'cancelled':
      return { title: `The ${noun} was cancelled.`, next: resume }
    default:
      return undefined
  }
}
