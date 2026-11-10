import { createContext, useCallback, useContext, useEffect, useRef, useState, type ReactNode } from 'react'
import { CloseIcon } from '../components/icons'
import { addToast, lifetime, removeToast, type ToastItem } from './toastlist'

/** A button on a toast, such as "Show in folder" after an export. */
export type ToastAction = { label: string; run: () => void }
type Toast = (text: string, bad?: boolean, action?: ToastAction) => () => void
const Ctx = createContext<Toast>(() => () => {})
/** `toast(text)` says what an action did; `toast(text, true)` is an error and stays until dismissed. It returns a function that takes the toast back. */
export const useToast = () => useContext(Ctx)

export function ToastProvider({ children }: { children: ReactNode }) {
  const [toasts, setToasts] = useState<ToastItem[]>([])
  const timers = useRef(new Map<number, ReturnType<typeof setTimeout>>())
  const dismiss = useCallback((id: number) => {
    clearTimeout(timers.current.get(id))
    timers.current.delete(id)
    setToasts((t) => t.filter((x) => x.id !== id))
  }, [])
  const push = useCallback<Toast>(
    (text, bad, action) => {
      const next: ToastItem = { id: Date.now() + Math.random(), text, bad, action }
      setToasts((t) => addToast(t, next).list)
      const ms = lifetime(next)
      // A duplicate never shows, so its timer finds nothing to remove.
      if (ms !== null) timers.current.set(next.id, setTimeout(() => dismiss(next.id), ms))
      return () => setToasts((t) => removeToast(t, next))
    },
    [dismiss],
  )
  useEffect(() => {
    const all = timers.current
    return () => all.forEach((t) => clearTimeout(t))
  }, [])
  return (
    <Ctx.Provider value={push}>
      {children}
      <div className="toast-stack">
        {toasts.map((t) => (
          // An error interrupts a screen reader; a notice waits its turn.
          <div key={t.id} className={`toast ${t.bad ? 'bad' : ''}`} role={t.bad ? 'alert' : 'status'}>
            <span>{t.text}</span>
            {t.action && (
              <button
                className="btn ghost sm"
                onClick={() => {
                  t.action!.run()
                  dismiss(t.id)
                }}
              >
                {t.action.label}
              </button>
            )}
            <button className="btn ghost sm icon toast-close" aria-label="Dismiss" onClick={() => dismiss(t.id)}>
              <CloseIcon />
            </button>
          </div>
        ))}
      </div>
    </Ctx.Provider>
  )
}
