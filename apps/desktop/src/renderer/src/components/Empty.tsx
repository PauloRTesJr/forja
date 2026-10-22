import type { ReactNode } from 'react'

export function Empty({ title, children }: { title: string; children?: ReactNode }) {
  return (
    <div className="empty">
      <strong style={{ color: 'var(--text-2)', fontSize: 14 }}>{title}</strong>
      {children}
    </div>
  )
}
