import { useQuery, useQueryClient } from '@tanstack/react-query'
import { lazy, Suspense, useCallback, useEffect, useRef, useState, type CSSProperties } from 'react'
import { BrandMark, GenerateIcon, LibraryIcon, ModelsIcon, QueueIcon, SettingsIcon } from './components/icons'
import { CoreDown } from './components/CoreDown'
import { useInstalls } from './components/Install'
import { ScreenBoundary } from './components/ScreenBoundary'
import { api, taskDone, type Job, type Model } from './lib/api'
import { queueBadge } from './lib/badge'
import { connectionLabel } from './lib/connection'
import { CoreContext } from './lib/core'
import { keys, useCoreEvents } from './lib/events'
import { needsAnswer } from './lib/jobs'
import { NavContext, transition, type Route } from './lib/nav'
import { useToast } from './lib/toast'
import { Generate } from './screens/Generate'
import { Queue } from './screens/Queue'

// Heavier screens load on first visit; Generate and Queue are ready at startup.
const Library = lazy(() => import('./screens/Library').then((m) => ({ default: m.Library })))
const AssetDetail = lazy(() => import('./screens/AssetDetail').then((m) => ({ default: m.AssetDetail })))
const Models = lazy(() => import('./screens/Models').then((m) => ({ default: m.Models })))
const Settings = lazy(() => import('./screens/Settings').then((m) => ({ default: m.Settings })))

const tabs = [
  { screen: 'generate', label: 'Generate', Icon: GenerateIcon },
  { screen: 'queue', label: 'Queue', Icon: QueueIcon },
  { screen: 'library', label: 'Library', Icon: LibraryIcon },
  { screen: 'models', label: 'Models', Icon: ModelsIcon },
  { screen: 'settings', label: 'Settings', Icon: SettingsIcon },
] as const

/** Says once, away from the screens that already ask (Generate, Queue), that a job found several views. */
function useAnswerNotices(jobs: Job[] | undefined, screen: Route['screen'], go: (r: Route) => void): void {
  const toast = useToast()
  const told = useRef(new Set<string>())
  const asked = screen === 'generate' || screen === 'queue'
  useEffect(() => {
    for (const j of jobs ?? []) {
      if (!needsAnswer(j) || told.current.has(j.id)) continue
      told.current.add(j.id)
      if (!asked) toast(`${j.name} shows several views: check them`, false, { label: 'Check', run: () => transition(() => go({ screen: 'generate' })) })
    }
  }, [jobs, asked, toast, go])
}

export function App() {
  const qc = useQueryClient()
  const core = useCoreEvents(qc)
  // What to say while there is no core to talk to; undefined when connected.
  const away = connectionLabel(core)
  const [route, setRoute] = useState<Route>({ screen: 'generate' })
  const jobs = useQuery({ queryKey: keys.jobs, queryFn: () => api<Job[]>('GET', '/v1/jobs?limit=100') })
  const badge = queueBadge(jobs.data)
  // Jobs waiting for an answer count on the badge, but nothing is generating for them.
  const generating = jobs.data?.some((j) => j.status === 'running' || j.status === 'queued') ?? false
  const installs = useInstalls()
  const install = installs.data?.find((t) => t.kind === 'install' && !taskDone(t))
  const installPct = install?.progress?.total ? Math.round((install.progress.done / install.progress.total) * 100) : undefined
  // The name the Models screen shows, from what it already loaded; the id until then.
  const installing = install && (qc.getQueryData<Model[]>(keys.models)?.find((m) => m.id === install.target)?.name ?? install.target)
  const navigate = useCallback((r: Route) => setRoute(r), [])
  useAnswerNotices(jobs.data, route.screen, navigate)

  const activeTab = route.screen === 'asset' ? 'library' : route.screen
  const tabIndex = tabs.findIndex((t) => t.screen === activeTab)

  useEffect(() => {
    const onKey = (e: KeyboardEvent): void => {
      const mod = window.forja.platform === 'darwin' ? e.metaKey : e.ctrlKey
      const n = Number(e.key)
      if (mod && n >= 1 && n <= tabs.length) {
        e.preventDefault()
        transition(() => setRoute({ screen: tabs[n - 1]!.screen }))
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])

  return (
    <CoreContext.Provider value={core}>
    <NavContext.Provider value={navigate}>
      <div className="app" data-platform={window.forja.platform}>
        <nav className="sidebar" aria-label="Main">
          <div className="brand">
            <BrandMark />
            <span className="brand-name">Forja</span>
          </div>
          <div className="nav" style={{ '--n': tabIndex } as CSSProperties}>
            <div className="nav-indicator" />
            {tabs.map(({ screen, label, Icon }) => (
              <button
                key={screen}
                aria-current={activeTab === screen ? 'page' : undefined}
                onClick={() => transition(() => setRoute({ screen }))}
              >
                <Icon />
                <span>{label}</span>
                {screen === 'queue' && badge.count > 0 && (
                  <span className={`badge ${generating ? 'live' : ''}`} aria-label={badge.label}>
                    {badge.count}
                  </span>
                )}
              </button>
            ))}
          </div>
          <div className="sidebar-foot">
            <div className="conn" role="status">
              <span className={`dot ${away ? (core.phase === 'starting' ? 'live' : 'bad') : generating || install ? 'live' : 'ok'}`} />
              <span>
                {away
                  ? away
                  : generating
                    ? 'Generating'
                    : install
                      ? `Installing ${installing}${installPct !== undefined ? ` · ${installPct}%` : ''}`
                      : 'Ready'}
              </span>
            </div>
          </div>
        </nav>
        <main className="main" key={route.screen === 'asset' ? `asset-${route.id}` : route.screen}>
          <ScreenBoundary>
            {core.phase === 'failed' && <CoreDown state={core} />}
            <Suspense fallback={<div className="screen" />}>
              {core.phase !== 'failed' && (
                <>
                  {route.screen === 'generate' && <Generate />}
                  {route.screen === 'queue' && <Queue />}
                  {route.screen === 'library' && <Library />}
                  {route.screen === 'asset' && <AssetDetail id={route.id} />}
                  {route.screen === 'models' && <Models />}
                  {route.screen === 'settings' && <Settings />}
                </>
              )}
            </Suspense>
          </ScreenBoundary>
        </main>
      </div>
    </NavContext.Provider>
    </CoreContext.Provider>
  )
}
