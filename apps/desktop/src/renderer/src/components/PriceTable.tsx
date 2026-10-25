import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useState, type CSSProperties } from 'react'
import { fetchPrices, removePrice, setPrice, type PriceRow, type PriceUnit } from '../lib/api'
import { priceText } from '../lib/estimate'
import { keys } from '../lib/events'
import { money } from '../lib/format'
import { useToast } from '../lib/toast'
import { NumberInput } from './Controls'

const UNITS: PriceUnit[] = ['second', 'hour', 'generation']
const unitWord: Record<PriceUnit, string> = { second: 'second', hour: 'hour', generation: 'image' }
const LOCKED = new Set(['system', 'project', 'env', 'flag'])

/** What a paid provider charges, as the estimates and the spending limits count it. */
export function PriceTable() {
  const prices = useQuery({ queryKey: keys.prices, queryFn: fetchPrices })
  return (
    <div className="setting prices">
      <div className="setting-text">
        <strong>Prices</strong>
        <span>What paid providers charge, for the estimate before a job and the spending limits. Vendors change prices: correct them here.</span>
        {prices.isPending && <div className="skeleton" style={{ height: 52 }} />}
        {prices.error && <span className="error">{(prices.error as Error).message}</span>}
        {prices.data && (
          <ul className="provider-list price-list">
            {prices.data.prices.map((p, i) => (
              <PriceItem key={p.id} row={p} index={i} />
            ))}
          </ul>
        )}
      </div>
    </div>
  )
}

function PriceItem({ row, index }: { row: PriceRow; index: number }) {
  const toast = useToast()
  const qc = useQueryClient()
  // A hosted model priced in its vendor's own unit gets a price per image here.
  const per: PriceUnit = UNITS.includes(row.per as PriceUnit) ? (row.per as PriceUnit) : 'generation'
  const saved = { onSuccess: (v: Awaited<ReturnType<typeof fetchPrices>>) => qc.setQueryData(keys.prices, v), onError: (e: Error) => toast(e.message, true) }
  const set = useMutation({ mutationFn: (usd: number) => setPrice(row.id, { usd, per }), ...saved })
  const reset = useMutation({ mutationFn: () => removePrice(row.id), ...saved })
  const locked = LOCKED.has(row.source)
  return (
    <li className="provider price-item enter" style={{ '--i': Math.min(index, 8) } as CSSProperties}>
      <div className="grow">
        <strong>{row.label || row.id}</strong>
        <div className="muted provider-meta">
          <span>{row.for.length > 0 ? row.for.join(', ') : 'No provider uses it yet'}</span>
          {row.label && <span className="mono">{row.id}</span>}
        </div>
        <div className="provider-key">{sourceText(row)}</div>
      </div>
      <div className="row">
        {UNITS.includes(row.per as PriceUnit) ? (
          <NumberInput
            value={row.usd}
            unit={`USD per ${unitWord[per]}`}
            min={0}
            step={per === 'second' ? 0.00001 : 0.01}
            disabled={locked || set.isPending}
            onCommit={(v) => set.mutate(v)}
          />
        ) : (
          <PerImage disabled={locked || set.isPending} onCommit={(v) => set.mutate(v)} />
        )}
        {row.source === 'user' && (
          <button className="btn ghost small-btn" onClick={() => reset.mutate()} disabled={reset.isPending}>
            Reset
          </button>
        )}
      </div>
    </li>
  )
}

/** A first price per image for a model its provider prices in some other unit; empty until set. */
function PerImage({ disabled, onCommit }: { disabled: boolean; onCommit: (v: number) => void }) {
  const [draft, setDraft] = useState('')
  const commit = (): void => {
    const v = Number(draft)
    if (draft !== '' && Number.isFinite(v) && v >= 0) onCommit(v)
    setDraft('')
  }
  return (
    <label className="number">
      <input
        className="input"
        type="number"
        min={0}
        step={0.01}
        placeholder="Set"
        value={draft}
        disabled={disabled}
        onChange={(e) => setDraft(e.target.value)}
        onBlur={commit}
        onKeyDown={(e) => e.key === 'Enter' && commit()}
      />
      <span>USD per image</span>
    </label>
  )
}

function sourceText(row: PriceRow): string {
  switch (row.source) {
    case 'user':
      if (row.default) return `Your price. Built in: ${priceText(row.default)}.`
      if (row.vendor) return `Your price. The provider says ${money(row.vendor)}.`
      return 'Your price.'
    case 'default':
      return "Built in. Check the vendor's page: prices change."
    case 'vendor':
      return UNITS.includes(row.per as PriceUnit)
        ? 'As the provider reports it.'
        : `The provider prices it ${money(row.vendor)}, which says nothing of one image: give it a price per image for the spending limits.`
  }
  return `Set in the ${row.source} configuration, which wins over this.`
}
