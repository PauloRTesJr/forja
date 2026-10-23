import type { ReactNode } from 'react'
import { Sparkle } from './icons'

/** A screen's title block: tarot-style eyebrow, title, one line of context, and any actions. */
export function ScreenHead({ eyebrow, title, sub, children }: { eyebrow: [string, string]; title: string; sub?: ReactNode; children?: ReactNode }) {
  return (
    <div className="screen-head">
      <div className="screen-title">
        <div className="eyebrow">
          <span>{eyebrow[0]}</span>
          <Sparkle />
          <span>{eyebrow[1]}</span>
        </div>
        <h1>{title}</h1>
        {sub !== undefined && <p className="sub">{sub}</p>}
      </div>
      {children}
    </div>
  )
}

export function SectionHead({ children }: { children: ReactNode }) {
  return (
    <div className="section-head">
      <h2>{children}</h2>
      <span className="rule" aria-hidden />
      <Sparkle size={9} />
    </div>
  )
}
