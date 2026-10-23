// Applies pushed core events to the query cache. Nothing in the UI polls.
import { useEffect, useState } from 'react'
import type { QueryClient } from '@tanstack/react-query'
import { taskDone, type Asset, type InstallTask, type Job } from './api'
import { applyProgress, upsertJob } from './jobs'

export const keys = {
  jobs: ['jobs'] as const,
  assets: (q = '') => ['assets', q] as const,
  asset: (id: string) => ['asset', id] as const,
  presets: ['presets'] as const,
  models: ['models'] as const,
  providers: ['providers'] as const,
  info: ['info'] as const,
  config: ['config'] as const,
  installs: ['installs'] as const,
  setup: ['setup'] as const,
  checks: (models: string[]) => ['checks', models.join(',')] as const,
  settings: ['settings'] as const,
}

/** Keeps the newest state of each task, newest task first. */
export function upsertTask(list: InstallTask[] | undefined, t: InstallTask): InstallTask[] {
  const rest = (list ?? []).filter((x) => !(x.id === t.id && x.provider === t.provider))
  const prev = (list ?? []).find((x) => x.id === t.id && x.provider === t.provider)
  if (prev && prev.version > t.version) return list ?? []
  return [t, ...rest].sort((a, b) => b.created_at - a.created_at)
}

export function useCoreEvents(qc: QueryClient): boolean {
  const [connected, setConnected] = useState(true)
  useEffect(() => {
    const offStatus = window.forja.onStatus((ok) => {
      setConnected(ok)
      // Anything may have changed while disconnected.
      if (ok) void qc.invalidateQueries()
    })
    const offEvent = window.forja.onEvent((ev) => {
      switch (ev.type) {
        case 'job.created':
        case 'job.updated':
        case 'job.finished':
          qc.setQueryData<Job[]>(keys.jobs, (list) => upsertJob(list, ev.data as Job))
          break
        case 'stage.progress': {
          const p = ev.data as { job: string; stage: string; step: number; total: number }
          qc.setQueryData<Job[]>(keys.jobs, (list) => applyProgress(list, p.job, p.stage, p.step, p.total))
          break
        }
        case 'asset.created': {
          const a = ev.data as Asset
          qc.setQueryData(keys.asset(a.id), a)
          void qc.invalidateQueries({ queryKey: ['assets'] })
          break
        }
        case 'models.changed':
          void qc.invalidateQueries({ queryKey: keys.models })
          void qc.invalidateQueries({ queryKey: keys.presets })
          void qc.invalidateQueries({ queryKey: keys.setup })
          break
        case 'settings.changed':
          void qc.invalidateQueries({ queryKey: keys.settings })
          void qc.invalidateQueries({ queryKey: keys.config })
          void qc.invalidateQueries({ queryKey: keys.presets })
          void qc.invalidateQueries({ queryKey: keys.models })
          break
        case 'install.updated': {
          const t = ev.data as InstallTask
          qc.setQueryData<InstallTask[]>(keys.installs, (list) => upsertTask(list, t))
          // A fix or an install changes what the checks find.
          if (taskDone(t)) void qc.invalidateQueries({ queryKey: ['checks'] })
          break
        }
      }
    })
    return () => {
      offStatus()
      offEvent()
    }
  }, [qc])
  return connected
}
