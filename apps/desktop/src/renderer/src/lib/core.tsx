// The app's connection to its core, for any screen: App provides what useCoreEvents reports.
import { createContext, useContext } from 'react'
import { STARTING, type CoreState } from './connection'

export const CoreContext = createContext<CoreState>(STARTING)

/** How the app is connected to its core right now. */
export const useCore = (): CoreState => useContext(CoreContext)
