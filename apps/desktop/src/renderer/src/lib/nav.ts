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

/** Swaps screens inside a View Transition when the platform supports it. */
export function transition(update: () => void): void {
  const doc = document as Document & { startViewTransition?: (cb: () => void) => unknown }
  if (doc.startViewTransition && !matchMedia('(prefers-reduced-motion: reduce)').matches) {
    doc.startViewTransition(update)
  } else {
    update()
  }
}
