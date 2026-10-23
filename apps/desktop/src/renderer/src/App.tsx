import { useQuery, useQueryClient } from '@tanstack/react-query'
import { lazy, Suspense, useCallback, useEffect, useState, type CSSProperties } from 'react'
import { Anvil, BrandMark, Eye, Grimoire, Hourglass, MoonPhases, SidebarSky, Sun } from './components/icons'
import { api, type Job } from './lib/api'
import { keys, useCoreEvents } from './lib/events'
import { isActive } from './lib/jobs'
import { NavContext, transition, type Route } from './lib/nav'
import { Generate } from './screens/Generate'
import { Queue } from './screens/Queue'

// Heavier screens load on first visit; Generate and Queue are ready at startup.
const Library = lazy(() => import('./screens/Library').then((m) => ({ default: m.Library })))
const AssetDetail = lazy(() => import('./screens/AssetDetail').then((m) => ({ default: m.AssetDetail })))
const Models = lazy(() => import('./screens/Models').then((m) => ({ default: m.Models })))
const Settings = lazy(() => import('./screens/Settings').then((m) => ({ default: m.Settings })))

const tabs = [
  { screen: 'generate', label: 'Generate', numeral: 'I', Icon: Anvil },
  { screen: 'queue', label: 'Queue', numeral: 'II', Icon: Hourglass },
  { screen: 'library', label: 'Library', numeral: 'III', Icon: Grimoire },
  { screen: 'models', label: 'Models', numeral: 'IV', Icon: Eye },
  { screen: 'settings', label: 'Settings', numeral: 'V', Icon: Sun },
] as const

export function App() {
  const qc = useQueryClient()
  const connected = useCoreEvents(qc)
  const [route, setRoute] = useState<Route>({ screen: 'generate' })
  const jobs = useQuery({ queryKey: keys.jobs, queryFn: () => api<Job[]>('GET', '/v1/jobs?limit=100') })
  const running = jobs.data?.filter(isActive).length ?? 0
  const navigate = useCallback((r: Route) => setRoute(r), [])

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
    <NavContext.Provider value={navigate}>
      <div className="app">
        <nav className="sidebar" aria-label="Main">
          <SidebarSky />
          <div className="brand">
            <BrandMark />
            <div>
              <span className="brand-name">Forja</span>
              <span className="brand-tag">image to game asset</span>
            </div>
          </div>
          <div className="nav" style={{ '--n': tabIndex } as CSSProperties}>
            <div className="nav-indicator" />
            {tabs.map(({ screen, label, numeral, Icon }) => (
              <button
                key={screen}
                aria-current={activeTab === screen ? 'page' : undefined}
                onClick={() => transition(() => setRoute({ screen }))}
              >
                <span className="nav-num" aria-hidden>
                  {numeral}
                </span>
                <Icon />
                <span>{label}</span>
                {screen === 'queue' && running > 0 && (
                  <span className="badge" aria-label={`${running} running or waiting`}>
                    {running}
                  </span>
                )}
              </button>
            ))}
          </div>
          <div className="sidebar-foot">
            <MoonPhases />
            <div className="conn">
              <span className={`dot ${!connected ? '' : running ? 'live' : 'ok'}`} />
              {!connected ? 'Reconnecting to core…' : running ? 'Generating' : 'Ready'}
            </div>
          </div>
        </nav>
        <main className="main" key={route.screen === 'asset' ? `asset-${route.id}` : route.screen}>
          <Suspense fallback={<div className="screen" />}>
            {route.screen === 'generate' && <Generate />}
            {route.screen === 'queue' && <Queue />}
            {route.screen === 'library' && <Library />}
            {route.screen === 'asset' && <AssetDetail id={route.id} />}
            {route.screen === 'models' && <Models />}
            {route.screen === 'settings' && <Settings />}
          </Suspense>
        </main>
      </div>
    </NavContext.Provider>
  )
}
