import type { ReactNode } from 'react'

/** A screen's title, one line of context under it, and its actions on the right. */
export function ScreenHead({ title, sub, children }: { title: string; sub?: ReactNode; children?: ReactNode }) {
  return (
    <div className="screen-head">
      <div className="screen-title">
        <h1>{title}</h1>
        {sub !== undefined && <p className="sub">{sub}</p>}
      </div>
      {children && <div className="screen-actions">{children}</div>}
    </div>
  )
}

export function SectionHead({ children }: { children: ReactNode }) {
  return (
    <div className="section-head">
      <h2>{children}</h2>
    </div>
  )
}
