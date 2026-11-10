// Applies pushed core events to the query cache. Nothing in the UI polls.
import { useEffect, useState } from 'react'
import type { QueryClient } from '@tanstack/react-query'
import { taskDone, type Asset, type BenchList, type BenchResult, type InstallTask, type Job } from './api'
import { withBench } from './bench'
import { guardFreshness, onConnected, STARTING, type CoreState } from './connection'
import { isNameSortedList, withAsset } from './assets'
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
  checks: (models: string[], stages: string[] = []) => ['checks', models.join(','), stages.join(',')] as const,
  settings: ['settings'] as const,
  share: ['share'] as const,
  spend: ['spend'] as const,
  catalog: (provider: string, q: string) => ['catalog', provider, q] as const,
  exportFormats: ['exportFormats'] as const,
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

/**
 * Applies the core's events to the query cache and says how the app is connected to its core.
 * Whenever events may have been missed (another stream, a gap in their numbers) everything is
 * read again, so no screen is left showing a state the core has moved on from.
 */
export function useCoreEvents(qc: QueryClient): CoreState {
  const [state, setState] = useState<CoreState>(STARTING)
  useEffect(() => {
    let seen: number | undefined
    let newest = -1
    const fresh = guardFreshness(qc)
    const apply = (next: CoreState): void => {
      setState(next)
      if (next.phase !== 'connected') return
      const what = onConnected(seen, next.generation)
      seen = next.generation
      // Anything may have changed while disconnected.
      if (what === 'all') void qc.invalidateQueries()
      else if (what === 'failed') void qc.refetchQueries({ predicate: (q) => q.state.status === 'error' })
    }
    // Pushed changes win over the answer to the first question when they cross.
    const offState = window.forja.onCoreState((next) => {
      newest = Math.max(newest, next.generation)
      apply(next)
    })
    void window.forja.coreState().then((first) => {
      if (first.generation >= newest) apply(first)
    })
    const offResync = window.forja.onResync(() => void qc.invalidateQueries())
    const offEvent = window.forja.onEvent((ev) => {
      switch (ev.type) {
        case 'job.created':
        case 'job.updated':
        case 'job.finished':
          qc.setQueryData<Job[]>(keys.jobs, (list) => upsertJob(list, ev.data as Job))
          fresh.touched(keys.jobs)
          // A finished job may have run paid stages. Its timings and costs change what the next
          // one is estimated to take.
          if (ev.type === 'job.finished') {
            void qc.invalidateQueries({ queryKey: keys.spend })
            void qc.invalidateQueries({ queryKey: ['estimate'] })
          }
          break
        case 'job.removed': {
          // Taken off the list (removed, cleared), or replaced by its retry, which follows as
          // a job.created.
          const d = ev.data as { id: string; replaced_by?: string }
          qc.setQueryData<Job[]>(keys.jobs, (list) => list?.filter((j) => j.id !== d.id))
          fresh.touched(keys.jobs)
          break
        }
        case 'stage.progress': {
          const p = ev.data as { job: string; stage: string; step: number; total: number }
          qc.setQueryData<Job[]>(keys.jobs, (list) => applyProgress(list, p.job, p.stage, p.step, p.total))
          break
        }
        case 'asset.created': {
          const a = ev.data as Asset
          qc.setQueryData(keys.asset(a.id), a)
          fresh.touched(keys.asset(a.id))
          void qc.invalidateQueries({ queryKey: ['assets'] })
          break
        }
        case 'asset.updated': {
          // A rename or a new thumbnail: the card changes in place, no list is read again.
          const a = ev.data as Asset
          // A new name moves the asset in a list sorted by name; the lists of other sorts stay put.
          const renamed = qc.getQueryData<Asset>(keys.asset(a.id))?.name !== a.name
          qc.setQueryData(keys.asset(a.id), a)
          qc.setQueriesData({ queryKey: ['assets'] }, (cache) => withAsset(cache, a))
          if (renamed) void qc.invalidateQueries({ queryKey: ['assets'], predicate: (q) => isNameSortedList(q.queryKey) })
          fresh.touched(keys.asset(a.id))
          fresh.touched(['assets'])
          break
        }
        case 'asset.deleted': {
          const d = ev.data as { id: string; job: string }
          qc.setQueriesData({ queryKey: ['assets'] }, (cache) => withoutAsset(cache, d.id))
          qc.setQueryData<Job[]>(keys.jobs, (list) => list?.filter((j) => j.id !== d.job))
          qc.removeQueries({ queryKey: keys.asset(d.id), exact: true })
          fresh.touched(['assets'])
          fresh.touched(keys.jobs)
          break
        }
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
          fresh.touched(keys.bench)
          break
        }
        case 'install.updated': {
          const t = ev.data as InstallTask
          qc.setQueryData<InstallTask[]>(keys.installs, (list) => upsertTask(list, t))
          fresh.touched(keys.installs)
          // A fix or an install changes what the checks find.
          if (taskDone(t)) void qc.invalidateQueries({ queryKey: ['checks'] })
          break
        }
      }
    })
    return () => {
      offState()
      offResync()
      offEvent()
      fresh.stop()
    }
  }, [qc])
  return state
}
