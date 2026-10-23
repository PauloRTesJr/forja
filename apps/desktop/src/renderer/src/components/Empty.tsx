import type { ReactNode } from 'react'
import { Crescent } from './icons'

export function Empty({ title, children }: { title: string; children?: ReactNode }) {
  return (
    <div className="empty">
      <Crescent />
      <strong>{title}</strong>
      {children}
    </div>
  )
}
