import { useId, useRef, useState, type ButtonHTMLAttributes, type KeyboardEvent, type ReactNode, type SyntheticEvent } from 'react'
import { MoreIcon } from './icons'

/**
 * A button that opens a popover menu anchored to it. The menu opens above everything and closes
 * on a click outside or Escape (focus then returns to the button) and on Tab. Opening focuses its
 * first item; arrows, Home and End move between the enabled ones. Its children are `MenuItem`s,
 * given the `id` to close the menu with.
 */
export function Menu({
  label,
  trigger,
  triggerClass = 'btn',
  menuClass = '',
  children,
}: {
  /** The button's and the menu's name for a screen reader. */
  label: string
  trigger: ReactNode
  triggerClass?: string
  menuClass?: string
  children: (id: string) => ReactNode
}) {
  const id = useId()
  const anchor = `--menu-${id.replace(/[^\w-]/g, '')}`
  const popup = useRef<HTMLDivElement>(null)
  const [open, setOpen] = useState(false)
  const items = (): HTMLElement[] => Array.from(popup.current?.querySelectorAll<HTMLElement>('[role="menuitem"]:not(:disabled)') ?? [])
  const onToggle = (e: SyntheticEvent<HTMLDivElement>): void => {
    const shown = (e.nativeEvent as Event & { newState?: string }).newState === 'open'
    setOpen(shown)
    if (shown) items()[0]?.focus()
  }
  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>): void => {
    if (e.key === 'Tab') {
      e.preventDefault()
      popup.current?.hidePopover()
      return
    }
    const all = items()
    if (all.length === 0) return
    const at = all.indexOf(document.activeElement as HTMLElement)
    const target: Record<string, number> = {
      ArrowDown: (at + 1) % all.length,
      ArrowUp: (at <= 0 ? all.length : at) - 1,
      Home: 0,
      End: all.length - 1,
    }
    if (!(e.key in target)) return
    e.preventDefault()
    all[target[e.key]!]?.focus()
  }
  return (
    <>
      <button className={triggerClass} popoverTarget={id} aria-label={label} aria-haspopup="menu" aria-expanded={open} style={{ anchorName: anchor }}>
        {trigger}
      </button>
      <div
        ref={popup}
        id={id}
        popover="auto"
        className={`menu ${menuClass}`}
        role="menu"
        aria-label={label}
        style={{ positionAnchor: anchor }}
        onToggle={onToggle}
        onKeyDown={onKeyDown}
      >
        {children(id)}
      </div>
    </>
  )
}

/** Rare actions behind a "more" button. */
export function MoreMenu({ label, children }: { label: string; children: (id: string) => ReactNode }) {
  return (
    <Menu label={label} trigger={<MoreIcon />} triggerClass="btn ghost icon">
      {children}
    </Menu>
  )
}

/**
 * One action of a menu, with an optional line that says what it does. `menu` is the id the
 * `MoreMenu` passes down: the item then closes the menu when clicked.
 */
export function MenuItem({
  menu,
  danger,
  help,
  children,
  ...rest
}: { menu?: string; danger?: boolean; help?: ReactNode; children: ReactNode } & ButtonHTMLAttributes<HTMLButtonElement>) {
  return (
    <button
      role="menuitem"
      className={`menu-item ${danger ? 'danger' : ''}`}
      popoverTarget={menu}
      popoverTargetAction={menu ? 'hide' : undefined}
      {...rest}
    >
      {children}
      {help && <small>{help}</small>}
    </button>
  )
}
