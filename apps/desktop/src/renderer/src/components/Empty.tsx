import type { ReactNode } from 'react'
import { BrandMark } from './icons'

export function Empty({ title, children }: { title: string; children?: ReactNode }) {
  return (
    <div className="empty">
      <BrandMark size={44} muted />
      <strong>{title}</strong>
      {children}
    </div>
  )
}
