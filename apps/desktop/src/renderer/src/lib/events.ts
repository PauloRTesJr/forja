// Applies pushed core events to the query cache. Nothing in the UI polls.
import { useEffect, useState } from 'react'
import type { QueryClient } from '@tanstack/react-query'
import { taskDone, type Asset, type BenchList, type BenchResult, type CacheUsage, type InstallTask, type Job } from './api'
import { withBench } from './bench'
import { applyProgress, upsertJob } from './jobs'
import { withoutAsset } from './selection'

export const keys = {
  jobs: ['jobs'] as const,
  assets: (q = '') => ['assets', q] as const,
  asset: (id: string) => ['asset', id] as const,
  presets: ['presets'] as const,
  // Under presets, so whatever refreshes the presets refreshes this too.
  presetsWithViews: ['presets', 'views'] as const,
  models: ['models'] as const,
  providers: ['providers'] as const,
  info: ['info'] as const,
  config: ['config'] as const,
  installs: ['installs'] as const,
  setup: ['setup'] as const,
  checks: (models: string[]) => ['checks', models.join(',')] as const,
  settings: ['settings'] as const,
  share: ['share'] as const,
  spend: ['spend'] as const,
  catalog: (provider: string, q: string) => ['catalog', provider, q] as const,
  exportFormats: ['exportFormats'] as const,
  cache: ['cache'] as const,
  bench: ['bench'] as const,
  benchResult: (id: string) => ['bench', id] as const,
  benchCompare: (baseline: string, current: string) => ['benchCompare', baseline, current] as const,
  estimate: (preset: string, overrides: Record<string, unknown>, views: string[]) => ['estimate', preset, overrides, views] as const,
  prices: ['prices'] as const,
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
          // A finished job may have run paid stages, and its outputs became cache. Its timings
          // and costs change what the next one is estimated to take.
          if (ev.type === 'job.finished') {
            void qc.invalidateQueries({ queryKey: keys.spend })
            void qc.invalidateQueries({ queryKey: keys.cache })
            void qc.invalidateQueries({ queryKey: ['estimate'] })
          }
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
        case 'asset.deleted': {
          const d = ev.data as { id: string; job: string }
          qc.setQueriesData<Asset[]>({ queryKey: ['assets'] }, (list) => withoutAsset(list, d.id))
          qc.setQueryData<Job[]>(keys.jobs, (list) => list?.filter((j) => j.id !== d.job))
          qc.removeQueries({ queryKey: keys.asset(d.id), exact: true })
          // Its files are cache now.
          void qc.invalidateQueries({ queryKey: keys.cache })
          break
        }
        case 'cache.changed':
          qc.setQueryData(keys.cache, ev.data as CacheUsage)
          break
        case 'models.changed':
          void qc.invalidateQueries({ queryKey: keys.models })
          void qc.invalidateQueries({ queryKey: keys.presets })
          void qc.invalidateQueries({ queryKey: keys.setup })
          // Pins live in config: a model chosen for a stage changes it.
          void qc.invalidateQueries({ queryKey: keys.config })
          void qc.invalidateQueries({ queryKey: ['estimate'] })
          void qc.invalidateQueries({ queryKey: keys.prices })
          break
        case 'providers.changed':
          void qc.invalidateQueries({ queryKey: keys.providers })
          void qc.invalidateQueries({ queryKey: keys.share })
          void qc.invalidateQueries({ queryKey: keys.info })
          void qc.invalidateQueries({ queryKey: keys.prices })
          break
        case 'settings.changed':
          void qc.invalidateQueries({ queryKey: keys.exportFormats })
          void qc.invalidateQueries({ queryKey: keys.cache })
          void qc.invalidateQueries({ queryKey: keys.settings })
          void qc.invalidateQueries({ queryKey: keys.config })
          void qc.invalidateQueries({ queryKey: keys.presets })
          void qc.invalidateQueries({ queryKey: keys.models })
          void qc.invalidateQueries({ queryKey: keys.setup })
          // Prices and limits are settings: estimates read both.
          void qc.invalidateQueries({ queryKey: keys.prices })
          void qc.invalidateQueries({ queryKey: ['estimate'] })
          break
        case 'bench.updated':
        case 'bench.finished': {
          const r = ev.data as BenchResult
          qc.setQueryData(keys.benchResult(r.id), r)
          qc.setQueryData<BenchList>(keys.bench, (list) => withBench(list, r))
          break
        }
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
