import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import '@fontsource-variable/inter/wght.css'
import '@fontsource-variable/jetbrains-mono/wght.css'
import { App } from './App'
import { guardDrops } from './lib/dropguard'
import { shouldRetry } from './lib/http'
import { ToastProvider } from './lib/toast'
import './styles/app.css'

// Server state arrives by push (see lib/events.ts); refetching on focus or interval is off.
// The core is on this computer, so nothing waits for a network: without `networkMode: 'always'`
// every request would pause while the computer is offline, which is when Forja must still work.
const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: Infinity,
      refetchOnWindowFocus: false,
      networkMode: 'always',
      retry: shouldRetry,
      retryDelay: (failures) => Math.min(400 * 2 ** failures, 4000),
    },
    mutations: { networkMode: 'always' },
  },
})

guardDrops(window)

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <ToastProvider>
        <App />
      </ToastProvider>
    </QueryClientProvider>
  </StrictMode>,
)
