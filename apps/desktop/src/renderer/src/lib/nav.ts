import { createContext, useContext } from 'react'

export type Route =
  | { screen: 'generate' }
  | { screen: 'queue' }
  | { screen: 'library' }
  | { screen: 'asset'; id: string }
  | { screen: 'models' }
  | { screen: 'settings' }

export const NavContext = createContext<(r: Route) => void>(() => {})
export const useNavigate = (): ((r: Route) => void) => useContext(NavContext)

type ViewTransitionLike = { ready: Promise<unknown>; finished: Promise<unknown> }
/** The parts of the page a screen change needs; tests bring their own. */
export interface TransitionHost {
  hidden: boolean
  reducedMotion: boolean
  start?: (update: () => void) => ViewTransitionLike
}

function pageHost(): TransitionHost {
  const doc = document as Document & { startViewTransition?: (cb: () => void) => ViewTransitionLike }
  return {
    hidden: doc.hidden,
    reducedMotion: matchMedia('(prefers-reduced-motion: reduce)').matches,
    start: doc.startViewTransition ? (update) => doc.startViewTransition!(update) : undefined,
  }
}

/** Swaps screens inside a View Transition when the platform supports it and there is a frame to animate. */
export function transition(update: () => void, host: TransitionHost = pageHost()): void {
  // A hidden window (minimised, covered, the user in another program) paints nothing: a
  // transition started there is skipped and rejects its promises.
  if (!host.start || host.hidden || host.reducedMotion) {
    update()
    return
  }
  const t = host.start(update)
  // A skipped transition (the window was hidden meanwhile, another one began) still ran the
  // update; only its animation is gone, which is nothing to report.
  t.ready.catch(() => {})
  t.finished.catch(() => {})
}
