import type { ReactNode } from 'react'
import type { IconType } from './icons'

/** Nothing to show yet: an icon, what is missing, one line on how it gets here, and one action. */
export function Empty({ icon: Icon, title, children, action }: { icon: IconType; title: string; children?: ReactNode; action?: ReactNode }) {
  return (
    <div className="empty">
      <Icon size={28} />
      <strong>{title}</strong>
      {children && <p>{children}</p>}
      {action}
    </div>
  )
}
