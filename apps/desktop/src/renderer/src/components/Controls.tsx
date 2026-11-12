// The shared controls. DESIGN.md says when to use each; their looks are in styles/controls.css.
import { createContext, useContext, useEffect, useId, useRef, useState, type CSSProperties, type KeyboardEvent, type ReactNode } from 'react'
import type { Setting } from '../lib/api'
import { defaultText, isLocked, readNumber, sourceNote, type NumberRule } from '../lib/settings'
import { AlertIcon, HelpIcon, LockedIcon, ResetIcon } from './icons'

/** The ids that tie a setting's control to its label and its help. */
export interface RowField {
  /** For the control itself. */
  id: string
  labelId: string
  /** Undefined when the row has no help line. */
  helpId?: string
}

const RowContext = createContext<RowField | undefined>(undefined)

/**
 * One setting: label, help, its control, and where its value comes from. `stacked` puts a wide
 * control (a group of choices) under the text instead of beside it. The controls below name
 * themselves from the row; anything else takes the ids from `field`.
 */
export function Row({
  setting,
  label,
  help,
  unit,
  defaultAs,
  stacked,
  onReset,
  children,
}: {
  setting: Setting | undefined
  label: string
  help?: ReactNode
  /** Said after the default on the reset button: "Reset to 15 minutes". */
  unit?: string
  /** The default in words, where its value alone would mislead: "no limit" for a limit of 0. */
  defaultAs?: string
  stacked?: boolean
  onReset: (key: string, value: unknown) => void
  children: (s: Setting, locked: boolean, field: RowField) => ReactNode
}) {
  const id = useId()
  if (!setting) {
    return (
      <div className="setting">
        <div className="skeleton" style={{ height: 20, width: 240 }} />
      </div>
    )
  }
  const locked = isLocked(setting)
  const note = sourceNote(setting)
  const reset = defaultAs ?? defaultText(setting.default, unit)
  const field: RowField = { id: `${id}c`, labelId: `${id}l`, helpId: help || note ? `${id}h` : undefined }
  return (
    <div className={`setting ${stacked ? 'stacked' : ''}`}>
      <div className="setting-text">
        <strong id={field.labelId}>{label}</strong>
        <div id={field.helpId} className="setting-help">
          {help && <div>{help}</div>}
          {note && (
            <span className="setting-source">
              <LockedIcon size={12} />
              {note}
            </span>
          )}
        </div>
      </div>
      <div className="setting-control">
        <RowContext.Provider value={field}>{children(setting, locked, field)}</RowContext.Provider>
        {/* Always there, so a control does not move when its value starts to differ from the default. */}
        <span className="setting-slot">
          {setting.source === 'user' && (
            <button
              className="btn ghost icon sm"
              onClick={() => onReset(setting.key, null)}
              aria-label={`Reset ${label} to ${reset}`}
              title={`Reset to ${reset}`}
            >
              <ResetIcon />
            </button>
          )}
        </span>
      </div>
    </div>
  )
}

/** What a field says under itself: why a value was refused, or that the old one was kept. */
type FieldNote = { text: string; bad: boolean }

/**
 * The commit rules every settings field shares. A value is saved on Enter or when focus leaves,
 * never on every keystroke, and only when it changed and is valid; Escape puts the saved value
 * back. While editing, the draft shows; otherwise the saved value does, so a change from
 * elsewhere appears at once. `save` may return a promise: the field waits for it, and shows its
 * failure under itself.
 */
function useDraft() {
  const [draft, setDraft] = useState<string | null>(null)
  const [note, setNote] = useState<FieldNote | null>(null)
  const [busy, setBusy] = useState(false)
  const alive = useRef(true)
  useEffect(() => {
    alive.current = true
    return () => {
      alive.current = false
    }
  }, [])
  // A note that only says "kept as it was" leaves by itself; a refusal stays until the next edit.
  useEffect(() => {
    if (!note || note.bad) return
    const t = setTimeout(() => setNote(null), 6000)
    return () => clearTimeout(t)
  }, [note])
  const save = async (run: () => void | Promise<unknown>, leaving: boolean): Promise<void> => {
    setBusy(true)
    try {
      await run()
      if (alive.current) setDraft(null)
    } catch (err) {
      if (!alive.current) return
      setNote({ text: err instanceof Error ? err.message : String(err), bad: true })
      // Left behind, the field shows what is saved; still in it, the typed text stays to correct.
      if (leaving) setDraft(null)
    } finally {
      if (alive.current) setBusy(false)
    }
  }
  return {
    draft,
    note,
    busy,
    edit: (text: string) => {
      setDraft(text)
      setNote(null)
    },
    cancel: () => {
      setDraft(null)
      setNote(null)
    },
    restore: (text: string, bad = false) => {
      setDraft(null)
      setNote({ text, bad })
    },
    refuse: (text: string) => setNote({ text, bad: true }),
    save,
  }
}

function Note({ id, note }: { id: string; note: FieldNote | null }) {
  if (!note) return null
  return (
    <span id={id} className={`setting-note ${note.bad ? 'bad' : ''}`} role={note.bad ? 'alert' : 'status'}>
      {note.text}
    </span>
  )
}

const describedBy = (...ids: (string | undefined | null | false)[]): string | undefined => ids.filter(Boolean).join(' ') || undefined

/**
 * A number with its unit. An emptied field never means 0: the saved value comes back, with a
 * line that says so. A value outside `min`..`max` is refused under the field, not saved.
 */
export function NumberInput({
  value,
  unit,
  min,
  max,
  integer,
  noneText,
  label,
  disabled,
  onCommit,
  onClear,
}: {
  value: number
  unit: string
  min: number
  max?: number
  integer?: boolean
  /** What 0 means, when 0 means "none" (a limit): shown as the placeholder instead of a 0. */
  noneText?: string
  /** Names the field where no row does. */
  label?: string
  disabled?: boolean
  onCommit: (v: number) => void | Promise<unknown>
  /** With `noneText`: an emptied field means "none" and calls this (back to the default) instead of keeping the value. */
  onClear?: () => void | Promise<unknown>
}) {
  const row = useContext(RowContext)
  const noteId = useId()
  const d = useDraft()
  const rule: NumberRule = { min, max, integer }
  const shown = noneText !== undefined && value === 0 ? '' : String(value)
  const commit = (leaving: boolean): void => {
    if (d.draft === null || d.busy) return
    const read = readNumber(d.draft, value, rule)
    switch (read.kind) {
      case 'unchanged':
        return d.cancel()
      case 'empty':
        if (shown === '') return d.cancel()
        if (noneText && onClear) return void d.save(onClear, leaving)
        return d.restore(noneText ? `Kept at ${shown}. Type 0 for ${noneText.toLowerCase()}.` : `Kept at ${shown}: an empty field is not a value.`)
      case 'invalid':
        return leaving ? d.restore(`${read.message} Kept at ${shown || noneText?.toLowerCase() || '0'}.`, true) : d.refuse(read.message)
      case 'value':
        return void d.save(() => onCommit(read.value), leaving)
    }
  }
  const onKey = (e: KeyboardEvent<HTMLInputElement>): void => {
    if (e.key === 'Enter') commit(false)
    else if (e.key === 'Escape' && d.draft !== null) {
      // Only this edit is called off, not a dialog around it.
      e.stopPropagation()
      d.cancel()
    }
  }
  return (
    <span className="setting-field">
      <span className="number">
        <input
          id={row?.id}
          className="input"
          type="text"
          inputMode="decimal"
          autoComplete="off"
          spellCheck={false}
          value={d.draft ?? shown}
          placeholder={noneText}
          disabled={disabled}
          readOnly={d.busy}
          aria-busy={d.busy || undefined}
          aria-label={row ? undefined : label}
          aria-labelledby={row?.labelId}
          aria-describedby={describedBy(row?.helpId, d.note && noteId)}
          aria-invalid={d.note?.bad || undefined}
          onChange={(e) => d.edit(e.target.value)}
          onBlur={() => commit(true)}
          onKeyDown={onKey}
        />
        <span>{unit}</span>
      </span>
      <Note id={noteId} note={d.note} />
    </span>
  )
}

/**
 * A line of text. Emptied, it calls `onClear` (back to the default) when the setting has a
 * meaning for "nothing"; otherwise the saved text comes back, with a line that says so.
 */
export function TextInput({
  value,
  disabled,
  placeholder,
  label,
  onCommit,
  onClear,
  children,
}: {
  value: string
  disabled?: boolean
  placeholder?: string
  /** Names the field where no row does. */
  label?: string
  onCommit: (v: string) => void | Promise<unknown>
  onClear?: () => void | Promise<unknown>
  /** A button beside the field, such as "Browse…". */
  children?: ReactNode
}) {
  const row = useContext(RowContext)
  const noteId = useId()
  const d = useDraft()
  const commit = (leaving: boolean): void => {
    if (d.draft === null || d.busy) return
    const text = d.draft.trim()
    if (text === value) return d.cancel()
    if (text !== '') return void d.save(() => onCommit(text), leaving)
    if (onClear) return void d.save(onClear, leaving)
    d.restore('Kept as it was: an empty field is not a value.')
  }
  const onKey = (e: KeyboardEvent<HTMLInputElement>): void => {
    if (e.key === 'Enter') commit(false)
    else if (e.key === 'Escape' && d.draft !== null) {
      e.stopPropagation()
      d.cancel()
    }
  }
  return (
    <span className="setting-field">
      <span className="setting-text-field">
        <input
          id={row?.id}
          className="input"
          value={d.draft ?? value}
          placeholder={placeholder}
          disabled={disabled}
          readOnly={d.busy}
          spellCheck={false}
          autoComplete="off"
          aria-busy={d.busy || undefined}
          aria-label={row ? undefined : label}
          aria-labelledby={row?.labelId}
          aria-describedby={describedBy(row?.helpId, d.note && noteId)}
          aria-invalid={d.note?.bad || undefined}
          onChange={(e) => d.edit(e.target.value)}
          onBlur={() => commit(true)}
          onKeyDown={onKey}
        />
        {children}
      </span>
      {d.busy && !d.note && (
        <span className="setting-note" role="status">
          Checking…
        </span>
      )}
      <Note id={noteId} note={d.note} />
    </span>
  )
}

/**
 * One of a few options, each with a line of help: a bordered group, the chosen one edged in
 * amber. A radio group to the keyboard: Tab reaches the chosen option, the arrow keys move
 * between options, Space or Enter chooses (moving alone does not, since a choice applies at once).
 */
export function Choices({
  value,
  options,
  disabled,
  label,
  onChange,
}: {
  value: string
  options: { value: string; label: string; help?: string }[]
  disabled?: boolean
  /** Names the group for assistive technology when no visible label does. */
  label?: string
  onChange: (v: string) => void
}) {
  const row = useContext(RowContext)
  const group = useRef<HTMLDivElement>(null)
  const chosen = options.some((o) => o.value === value) ? value : options[0]?.value
  const onKey = (e: KeyboardEvent<HTMLDivElement>): void => {
    const step = e.key === 'ArrowRight' || e.key === 'ArrowDown' ? 1 : e.key === 'ArrowLeft' || e.key === 'ArrowUp' ? -1 : 0
    const ends = e.key === 'Home' ? 0 : e.key === 'End' ? -1 : undefined
    if (step === 0 && ends === undefined) return
    const items = [...(group.current?.querySelectorAll<HTMLButtonElement>('[role=radio]:not(:disabled)') ?? [])]
    if (items.length === 0) return
    e.preventDefault()
    const at = items.indexOf(document.activeElement as HTMLButtonElement)
    const next = ends !== undefined ? items.at(ends) : items[(at + step + items.length) % items.length]
    next?.focus()
  }
  return (
    <div
      className="choices"
      role="radiogroup"
      ref={group}
      aria-label={row ? undefined : label}
      aria-labelledby={row?.labelId}
      aria-describedby={row?.helpId}
      onKeyDown={onKey}
    >
      {options.map((o) => (
        <button
          key={o.value}
          type="button"
          role="radio"
          aria-checked={value === o.value}
          tabIndex={o.value === chosen ? 0 : -1}
          disabled={disabled}
          className="choice"
          onClick={() => onChange(o.value)}
        >
          <strong>{o.label}</strong>
          {o.help && <span>{o.help}</span>}
        </button>
      ))}
    </div>
  )
}

/**
 * On or off. Name it with `children` (a visible label beside it), `label`, or a `<label htmlFor>` and `id`;
 * inside a settings `Row` it takes the row's label and help by itself.
 */
export function Switch({
  id,
  checked,
  disabled,
  label,
  onChange,
  children,
}: {
  /** For a `<label htmlFor>` elsewhere on the row. */
  id?: string
  checked: boolean
  disabled?: boolean
  label?: string
  onChange: (on: boolean) => void
  children?: ReactNode
}) {
  const row = useContext(RowContext)
  const named = label !== undefined || children !== undefined
  return (
    <button
      id={id ?? row?.id}
      type="button"
      className="switch"
      role="switch"
      aria-checked={checked}
      aria-label={label}
      aria-labelledby={named ? undefined : row?.labelId}
      aria-describedby={row?.helpId}
      disabled={disabled}
      onClick={() => onChange(!checked)}
    >
      <span className="switch-track" aria-hidden />
      {children}
    </button>
  )
}

/**
 * A value between `min` and `max` set by dragging, with the arrow keys for fine steps. It reports
 * every move; the caller decides when to save. Name it with `label` (the readout beside it is
 * the caller's).
 */
export function Slider({
  value,
  min = 0,
  max = 1,
  step = 0.01,
  label,
  valueText,
  disabled,
  onChange,
}: {
  value: number
  min?: number
  max?: number
  step?: number
  label: string
  /** How the value reads aloud, when the number alone is not enough. */
  valueText?: string
  disabled?: boolean
  onChange: (value: number) => void
}) {
  const fill = ((value - min) / (max - min)) * 100
  return (
    <input
      type="range"
      className="slider"
      min={min}
      max={max}
      step={step}
      value={value}
      aria-label={label}
      aria-valuetext={valueText}
      disabled={disabled}
      style={{ '--fill': `${fill}%` } as CSSProperties}
      onChange={(e) => onChange(e.currentTarget.valueAsNumber)}
    />
  )
}

/** The ? beside an option: what it does, on hover or focus. */
export function Hint({ children }: { children: string }) {
  const tip = useId()
  // A named thing that the tooltip describes: a bare span's label is not read out reliably.
  return (
    <span className="help" tabIndex={0} role="img" aria-label="Help" aria-describedby={tip}>
      <HelpIcon size={14} />
      <span className="help-tip" role="tooltip" id={tip}>
        {children}
      </span>
    </span>
  )
}

export type Tone = 'accent' | 'ok' | 'warn' | 'bad'

/** A short label on a tinted ground. `dot` makes it a state; `live` pulses the dot while work runs. */
export function Pill({ tone, dot, live, title, children }: { tone?: Tone; dot?: boolean; live?: boolean; title?: string; children: ReactNode }) {
  return (
    <span className={`pill ${tone ?? ''} ${live ? 'live' : ''}`} title={title}>
      {(dot || live) && <i aria-hidden />}
      {children}
    </span>
  )
}

const STATUS: Record<string, { label: string; tone?: Tone; live?: boolean }> = {
  queued: { label: 'Queued' },
  running: { label: 'Forging', tone: 'accent', live: true },
  waiting: { label: 'Needs you', tone: 'accent', live: true },
  succeeded: { label: 'Done', tone: 'ok' },
  failed: { label: 'Failed', tone: 'bad' },
  cancelled: { label: 'Cancelled' },
}

/** The state of a job, a benchmark run or a task, in the same words and colours everywhere. */
export function StatusPill({ status, label }: { status: string; label?: string }) {
  const s = STATUS[status] ?? { label: status }
  return (
    <Pill tone={s.tone} live={s.live} dot>
      {label ?? s.label}
    </Pill>
  )
}

/** Progress from 0 to 1; without a value it runs back and forth. `label` makes it a progressbar. */
export function Progress({ value, label }: { value?: number; label?: string }) {
  const known = value !== undefined
  return (
    <div
      className="bar"
      role={label ? 'progressbar' : undefined}
      aria-label={label}
      aria-valuemin={label ? 0 : undefined}
      aria-valuemax={label ? 100 : undefined}
      aria-valuenow={label && known ? Math.round(value * 100) : undefined}
      aria-hidden={label ? undefined : true}
    >
      <i className={known ? '' : 'indeterminate'} style={{ '--p': known ? Math.min(1, Math.max(0, value)) : 0 } as CSSProperties} />
    </div>
  )
}

/** What went wrong and what to do about it. */
export function ErrorBlock({ children, role }: { children: ReactNode; role?: 'alert' | 'status' }) {
  return (
    <div className="error" role={role}>
      <AlertIcon />
      <div>{children}</div>
    </div>
  )
}
