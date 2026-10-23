import { Component, type ReactNode } from 'react'

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
      <div className="screen">
        <div className="error">
          <strong>This screen hit a problem.</strong> {error.message}
        </div>
        <div className="row">
          <button className="btn" onClick={() => this.setState({ error: undefined })}>
            Try again
          </button>
        </div>
      </div>
    )
  }
}
