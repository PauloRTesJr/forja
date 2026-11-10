import { Component, type ReactNode } from 'react'
import { ErrorBlock } from './Controls'

/**
 * Keeps a screen's render error inside that screen: without it React unmounts the whole app
 * and leaves an empty window. The sidebar stays, and moving to another screen starts over.
 */
export class ScreenBoundary extends Component<{ children: ReactNode }, { error?: Error }> {
  override state: { error?: Error } = {}

  static getDerivedStateFromError(error: Error): { error: Error } {
    return { error }
  }

  override render(): ReactNode {
    const { error } = this.state
    if (!error) return this.props.children
    return (
      <div className="screen capped">
        <div className="screen-head">
          <div className="screen-title">
            <h1>This screen stopped working</h1>
            <p className="sub">The rest of the app is fine. Try again, or open another screen from the sidebar.</p>
          </div>
        </div>
        <ErrorBlock role="alert">{error.message}</ErrorBlock>
        <div className="row" style={{ marginTop: 12 }}>
          <button className="btn" onClick={() => this.setState({ error: undefined })}>
            Try again
          </button>
        </div>
      </div>
    )
  }
}
