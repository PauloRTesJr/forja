// Applies pushed core events to the query cache. Nothing in the UI polls.
import { useEffect, useState } from 'react'
import type { QueryClient } from '@tanstack/react-query'
import type { Asset, Job } from './api'
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
      }
    })
    return () => {
      offStatus()
      offEvent()
    }
  }, [qc])
  return connected
}
