import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { fetchPrices, removePrice, setPrice, type PriceRow, type PriceUnit, type Prices } from '../lib/api'
import { priceText } from '../lib/estimate'
import { keys } from '../lib/events'
import { money } from '../lib/format'
import { useToast } from '../lib/toast'
import { ErrorBlock, NumberInput } from './Controls'
import { LockedIcon, ResetIcon } from './icons'
import { SectionHead } from './ScreenHead'

const UNITS: PriceUnit[] = ['second', 'hour', 'generation']
const unitWord: Record<PriceUnit, string> = { second: 'second', hour: 'hour', generation: 'image' }
const LOCKED = new Set(['system', 'project', 'env', 'flag'])
// A hosted model with no usable price has none yet: the field is empty until one is given.
const UNSET = 0

/** What a paid provider charges, as the estimates and the spending limits count it. */
export function PriceTable() {
  const prices = useQuery({ queryKey: keys.prices, queryFn: fetchPrices })
  return (
    <div className="section prices">
      <SectionHead>Prices</SectionHead>
      <p className="section-note">
        What paid providers charge, for the estimate before a job and for the spending limits. Vendors change prices: correct them here.
      </p>
      {prices.isPending && <div className="skeleton" style={{ height: 56 }} />}
      {prices.error && <ErrorBlock>{(prices.error as Error).message}</ErrorBlock>}
      {prices.data && prices.data.prices.length === 0 && <p className="muted">No provider has a price yet.</p>}
      {prices.data && prices.data.prices.length > 0 && (
        <ul className="provider-list price-list group">
          {prices.data.prices.map((p) => (
            <PriceItem key={p.id} row={p} />
          ))}
        </ul>
      )}
    </div>
  )
}

function PriceItem({ row }: { row: PriceRow }) {
  const toast = useToast()
  const qc = useQueryClient()
  const name = row.label || row.id
  // A hosted model priced in its vendor's own unit gets a price per image here.
  const known = UNITS.includes(row.per as PriceUnit)
  const per: PriceUnit = known ? (row.per as PriceUnit) : 'generation'
  const saved = (v: Prices): void => void qc.setQueryData(keys.prices, v)
  const set = useMutation({ mutationFn: (usd: number) => setPrice(row.id, { usd, per }), onSuccess: saved })
  const reset = useMutation({ mutationFn: () => removePrice(row.id), onSuccess: saved, onError: (e: Error) => toast(e.message, true) })
  const locked = LOCKED.has(row.source)
  const back = row.default ? priceText(row.default) : row.vendor ? `the provider's ${money(row.vendor)}` : undefined
  return (
    <li className="provider price-item">
      <div className="grow">
        <strong>{name}</strong>
        <div className="muted provider-meta">
          <span>{row.for.length > 0 ? `Used by ${row.for.join(', ')}` : 'No provider uses it yet'}</span>
          {row.label && <span className="mono">{row.id}</span>}
        </div>
        <div className="provider-key">
          {locked && <LockedIcon size={12} />}
          {sourceText(row)}
        </div>
      </div>
      <div className="setting-control">
        <NumberInput
          value={known ? row.usd : UNSET}
          unit={`USD per ${unitWord[per]}`}
          min={0}
          max={100000}
          noneText={known ? undefined : 'Set a price'}
          label={`Price of ${name}, in USD per ${unitWord[per]}`}
          disabled={locked}
          // The field waits for the save and shows a refusal under itself.
          onCommit={(v) => set.mutateAsync(v)}
        />
        <span className="setting-slot">
          {row.source === 'user' && (
            <button
              className="btn ghost icon sm"
              onClick={() => reset.mutate()}
              disabled={reset.isPending}
              aria-label={`Reset the price of ${name}${back ? ` to ${back}` : ''}`}
              title={back ? `Reset to ${back}` : 'Reset'}
            >
              <ResetIcon />
            </button>
          )}
        </span>
      </div>
    </li>
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
