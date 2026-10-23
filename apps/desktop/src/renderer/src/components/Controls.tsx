import { useState, type ReactNode } from 'react'
import type { Setting } from '../lib/api'

// Layers above "user" win over what the Settings screen writes.
const ABOVE_USER = new Set(['project', 'env', 'flag'])

function sourceNote(s: Setting): string | undefined {
  switch (s.source) {
    case 'env':
      return `Set by the environment variable FORJA_${s.key.toUpperCase().replace('.', '__')}, so it cannot change here.`
    case 'flag':
      return 'Set on the command line for this run.'
    case 'project':
      return "Set by the project's forja.toml, which wins over this."
  }
  return undefined
}

/** One setting: label, help, its control, and where its value comes from. */
export function Row({
  setting,
  label,
  help,
  onReset,
  children,
}: {
  setting: Setting | undefined
  label: string
  help?: string
  onReset: (key: string, value: unknown) => void
  children: (s: Setting, locked: boolean) => ReactNode
}) {
  if (!setting) return <div className="skeleton" style={{ height: 52 }} />
  const locked = ABOVE_USER.has(setting.source)
  const note = sourceNote(setting)
  return (
    <div className="setting">
      <div className="setting-text">
        <strong>{label}</strong>
        {help && <span>{help}</span>}
        {note && <span className="setting-source">{note}</span>}
      </div>
      <div className="setting-control">
        {children(setting, locked)}
        {setting.source === 'user' && (
          <button className="btn ghost small-btn" onClick={() => onReset(setting.key, null)} title={`Default: ${String(setting.default)}`}>
            Reset
          </button>
        )}
      </div>
    </div>
  )
}

/**
 * Saves on Enter or when focus leaves, never on every keystroke. While editing, the draft
 * shows; otherwise the saved value does, so a change from elsewhere appears at once.
 */
export function NumberInput({
  value,
  unit,
  min,
  step,
  disabled,
  onCommit,
}: {
  value: number
  unit: string
  min: number
  step: number
  disabled?: boolean
  onCommit: (v: number) => void
}) {
  const [draft, setDraft] = useState<string | null>(null)
  const commit = (): void => {
    const v = Number(draft)
    if (draft !== null && Number.isFinite(v) && v >= min && v !== value) onCommit(v)
    setDraft(null)
  }
  return (
    <label className="number">
      <input
        className="input"
        type="number"
        min={min}
        step={step}
        value={draft ?? String(value)}
        disabled={disabled}
        onChange={(e) => setDraft(e.target.value)}
        onBlur={commit}
        onKeyDown={(e) => e.key === 'Enter' && commit()}
      />
      <span>{unit}</span>
    </label>
  )
}

export function TextInput({ value, disabled, onCommit, placeholder }: { value: string; disabled?: boolean; onCommit: (v: string) => void; placeholder?: string }) {
  const [draft, setDraft] = useState<string | null>(null)
  const commit = (): void => {
    const v = draft?.trim()
    if (v && v !== value) onCommit(v)
    setDraft(null)
  }
  return (
    <input
      className="input"
      value={draft ?? value}
      placeholder={placeholder}
      disabled={disabled}
      onChange={(e) => setDraft(e.target.value)}
      onBlur={commit}
      onKeyDown={(e) => e.key === 'Enter' && commit()}
    />
  )
}

export function Choices({
  value,
  options,
  disabled,
  onChange,
}: {
  value: string
  options: { value: string; label: string; help?: string }[]
  disabled?: boolean
  onChange: (v: string) => void
}) {
  return (
    <div className="choices" role="radiogroup">
      {options.map((o) => (
        <button key={o.value} role="radio" aria-checked={value === o.value} disabled={disabled} className="choice" onClick={() => onChange(o.value)}>
          <strong>{o.label}</strong>
          {o.help && <span>{o.help}</span>}
        </button>
      ))}
    </div>
  )
}
