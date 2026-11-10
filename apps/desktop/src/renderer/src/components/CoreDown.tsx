import type { CoreState } from '../lib/connection'
import { ErrorBlock } from './Controls'

/**
 * Shown in place of every screen when the core could not be started: nothing in the app works
 * without it, so this says why and offers the one thing to do.
 */
export function CoreDown({ state }: { state: CoreState }) {
  return (
    <div className="screen capped">
      <div className="screen-head">
        <div className="screen-title">
          <h1>Forja could not start</h1>
          <p className="sub">The part of Forja that runs jobs and keeps the library (its core) is not running.</p>
        </div>
      </div>
      <ErrorBlock role="alert">{state.message ?? "Forja's core could not be started."}</ErrorBlock>
      <div className="row" style={{ marginTop: 12 }}>
        <button className="btn primary" onClick={() => window.forja.retryCore()}>
          Try again
        </button>
        {state.detail && (
          <button className="btn" onClick={() => void window.forja.copyText(`${state.message ?? ''}\n\n${state.detail}`)}>
            Copy details
          </button>
        )}
      </div>
    </div>
  )
}
