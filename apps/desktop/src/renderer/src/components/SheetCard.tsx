import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useState, type CSSProperties } from 'react'
import { answerSheet, artifactURL, type Job, type View } from '../lib/api'
import { keys } from '../lib/events'
import { upsertJob } from '../lib/jobs'
import { useToast } from '../lib/toast'
import { assignView, boxStyle, chosenParts, VIEWS, viewHints, viewLabels, viewShort } from '../lib/views'

// The cut-out never grows past this height in a queue row.
const MAX_HEIGHT = 440

/**
 * A job whose image held several views of one subject: each figure boxed on the cut-out with
 * the view proposed for it. The user relabels, then builds from the views or keeps the image.
 */
export function SheetCard({ job }: { job: Job }) {
  const qc = useQueryClient()
  const toast = useToast()
  const sheet = job.sheet!
  const parts = sheet.parts ?? []
  const [labels, setLabels] = useState<(View | '')[]>(() => parts.map((p) => p.view))
  const answer = useMutation({
    mutationFn: (body: Parameters<typeof answerSheet>[1]) => answerSheet(job.id, body),
    onSuccess: (next) => qc.setQueryData<Job[]>(keys.jobs, (list) => upsertJob(list, next)),
    onError: (e: Error) => toast(e.message, true),
  })
  const chosen = chosenParts(parts, labels)
  const hasFront = labels.includes('front')
  const [w, h] = sheet.size

  return (
    <div className="sheet-card">
      <div className="sheet-figure" style={{ width: `min(100%, ${Math.round((MAX_HEIGHT * w) / h)}px)`, aspectRatio: `${w} / ${h}` }}>
        {sheet.cutout && <img src={artifactURL(sheet.cutout)} alt="The image with each figure boxed" decoding="async" />}
        {parts.map((p, i) => (
          <div
            key={i}
            className="sheet-part enter"
            data-chosen={labels[i] ? 'true' : 'false'}
            style={{ ...boxStyle(p.box, sheet.size), '--i': i } as CSSProperties}
          >
            <select
              className="sheet-label"
              aria-label={`Figure ${i + 1}${p.kind === 'detail' ? ' (a close-up)' : ''}`}
              title={labels[i] ? `${viewLabels[labels[i]]}: ${viewHints[labels[i]].toLowerCase()}` : 'Left out'}
              value={labels[i]}
              disabled={answer.isPending}
              onChange={(e) => setLabels(assignView(labels, i, e.target.value as View | ''))}
            >
              {VIEWS.map((v) => (
                <option key={v} value={v}>
                  {viewShort[v]}
                </option>
              ))}
              <option value="">Leave out</option>
            </select>
          </div>
        ))}
      </div>
      <p className="sheet-note">
        {hasFront
          ? "Check the labels: an outline can't tell a front from a back, or which way a side view faces."
          : 'Choose which figure is the front.'}
      </p>
      <div className="row">
        <button className="btn primary" disabled={!hasFront || answer.isPending} onClick={() => answer.mutate({ views: chosen })}>
          Build from {chosen.length} {chosen.length === 1 ? 'view' : 'views'}
        </button>
        <button className="btn ghost" disabled={answer.isPending} onClick={() => answer.mutate({ keep: true })}>
          Use the whole image
        </button>
      </div>
    </div>
  )
}
