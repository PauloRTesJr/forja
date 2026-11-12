import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useEffect, useState, type CSSProperties, type ReactNode } from 'react'
import {
  editsToSets,
  fetchMaterials,
  materialName,
  materialsKey,
  percent,
  SEND_DELAY_MS,
  setMaterials,
  shownValues,
  valueText,
  withEdit,
  withoutSent,
  type AssetMaterials,
  type Edits,
  type MaterialSet,
} from '../lib/materials'
import { useToast } from '../lib/toast'
import { Dialog, DialogActions } from './Dialog'
import { ErrorBlock, Slider } from './Controls'
import { SectionHead } from './ScreenHead'

/** The sentence an asset shows in place of sliders when its model painted roughness and metallic. */
export const PAINTED = "This asset's roughness and metallic were painted by its model."

/**
 * How rough and how metallic each of an asset's materials is, set with a slider each. The
 * materials are the dominant colors of its base color texture; the core writes the values into the
 * asset's metallic-roughness map and the viewer shows the new file when the core announces it.
 */
export function MaterialsSection({ id }: { id: string }) {
  const qc = useQueryClient()
  const toast = useToast()
  const query = useQuery({ queryKey: materialsKey(id), queryFn: ({ signal }) => fetchMaterials(id, signal) })
  const [edits, setEdits] = useState<Edits>(new Map())
  // The person agreed to replace what the asset's model painted.
  const [replace, setReplace] = useState(false)
  const [confirming, setConfirming] = useState(false)
  const { mutate, isPending } = useMutation({
    mutationFn: (sets: MaterialSet[]) => setMaterials(id, sets, replace),
    onSuccess: (data, sets) => {
      qc.setQueryData(materialsKey(id), data)
      setEdits((e) => withoutSent(e, sets))
    },
    onError: (e: Error) => {
      toast(e.message, true)
      setEdits(new Map())
      void query.refetch()
    },
  })

  // A drag is one change: the values go out a moment after the last move, one request at a time.
  useEffect(() => {
    if (edits.size === 0 || isPending) return
    const timer = setTimeout(() => mutate(editsToSets(edits)), SEND_DELAY_MS)
    return () => clearTimeout(timer)
  }, [edits, isPending, mutate])

  return (
    <>
      <MaterialsView
        data={query.data}
        loading={query.isPending}
        error={query.isError ? query.error.message : undefined}
        edits={edits}
        replacing={replace}
        onChange={(index, field, value) => setEdits((e) => withEdit(e, index, field, value))}
        onReplace={() => setConfirming(true)}
      />
      {confirming && (
        <Dialog title="Replace the painted values?" size="narrow" alert onClose={() => setConfirming(false)}>
          <p className="muted">
            The asset's model painted its own roughness and metallic. Setting them here replaces that painting with one value per material. To get it back,
            generate the asset again.
          </p>
          <DialogActions>
            <button className="btn ghost" autoFocus onClick={() => setConfirming(false)}>
              Cancel
            </button>
            <button
              className="btn danger"
              onClick={() => {
                setReplace(true)
                setConfirming(false)
              }}
            >
              Replace
            </button>
          </DialogActions>
        </Dialog>
      )}
    </>
  )
}

/** The section as it looks for the values it is given; it keeps no state of its own. */
export function MaterialsView({
  data,
  loading,
  error,
  edits,
  replacing,
  onChange,
  onReplace,
}: {
  data: AssetMaterials | undefined
  loading: boolean
  error?: string
  edits: Edits
  /** The person agreed to replace the painted values: show the sliders. */
  replacing: boolean
  onChange: (index: number, field: 'roughness' | 'metallic', value: number) => void
  onReplace: () => void
}) {
  let body: ReactNode
  if (error) body = <ErrorBlock role="alert">Materials could not be read. {error}</ErrorBlock>
  else if (loading || !data) {
    body = (
      <div className="mat-list" aria-busy>
        <span className="skeleton mat-wait" />
      </div>
    )
  } else if (data.reason) body = <p className="muted mat-note">{data.reason}</p>
  else if (data.painted && !replacing) {
    body = (
      <p className="muted mat-note">
        {PAINTED}{' '}
        <button className="text-btn" onClick={onReplace}>
          Replace them…
        </button>
      </p>
    )
  } else {
    body = (
      <ul className="mat-list">
        {data.materials.map((row, position) => {
          const v = shownValues(row, edits)
          const fields = [
            ['roughness', 'Roughness'],
            ['metallic', 'Metallic'],
          ] as const
          return (
            <li key={row.index} className="mat enter" style={{ '--i': position } as CSSProperties}>
              <div className="mat-head">
                <span className="mat-swatch" style={{ background: row.color }} role="img" aria-label={`Color ${row.color}`} />
                <span className="mono">{row.color}</span>
                <span className="num dim">{percent(row.share)}</span>
              </div>
              {fields.map(([field, label]) => (
                <div key={field} className="mat-field">
                  <span className="muted" aria-hidden>
                    {label}
                  </span>
                  <Slider label={`${label} of ${materialName(row, position)}`} value={v[field]} valueText={valueText(v[field])} onChange={(x) => onChange(row.index, field, x)} />
                  <span className="num mat-value">{valueText(v[field])}</span>
                </div>
              ))}
            </li>
          )
        })}
      </ul>
    )
  }
  return (
    <section className="section asset-materials" aria-label="Materials">
      <SectionHead>Materials</SectionHead>
      {!error && data && !data.reason && !(data.painted && !replacing) && (
        <p className="section-note">How rough and how metallic each color of the surface is.</p>
      )}
      {body}
    </section>
  )
}
