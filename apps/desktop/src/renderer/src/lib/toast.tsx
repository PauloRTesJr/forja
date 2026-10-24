import { createContext, useCallback, useContext, useState, type ReactNode } from 'react'

/** A button on a toast, such as "Show in folder" after an export. */
export type ToastAction = { label: string; run: () => void }
type Toast = { id: number; text: string; bad?: boolean; action?: ToastAction }
const Ctx = createContext<(text: string, bad?: boolean, action?: ToastAction) => void>(() => {})
export const useToast = () => useContext(Ctx)

export function ToastProvider({ children }: { children: ReactNode }) {
  const [toasts, setToasts] = useState<Toast[]>([])
  const push = useCallback((text: string, bad?: boolean, action?: ToastAction) => {
    const id = Date.now() + Math.random()
    setToasts((t) => [...t, { id, text, bad, action }])
    // One with a button stays longer, so there is time to reach it.
    setTimeout(() => setToasts((t) => t.filter((x) => x.id !== id)), action ? 8000 : 4200)
  }, [])
  return (
    <Ctx.Provider value={push}>
      {children}
      <div className="toast-stack" role="status" aria-live="polite">
        {toasts.map((t) => (
          <div key={t.id} className={`toast ${t.bad ? 'bad' : ''}`}>
            {t.text}
            {t.action && (
              <button
                className="toast-action"
                onClick={() => {
                  t.action!.run()
                  setToasts((list) => list.filter((x) => x.id !== t.id))
                }}
              >
                {t.action.label}
              </button>
            )}
          </div>
        ))}
      </div>
    </Ctx.Provider>
  )
}
