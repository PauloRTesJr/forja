import { useMutation, useQueryClient } from '@tanstack/react-query'
import type { CSSProperties } from 'react'
import { answerSheet, artifactURL, type Job, type View } from '../lib/api'
import { draftStore, useDraft } from '../lib/draft'
import { keys } from '../lib/events'
import { upsertJob } from '../lib/jobs'
import { useToast } from '../lib/toast'
import { assignView, boxStyle, chosenParts, VIEWS, viewHints, viewLabels, viewShort } from '../lib/views'

/**
 * Answers a job that waits on its sheet of views. Once answered, Generate keeps following the
 * job, so the answer is seen to have worked.
 */
export function useSheetAnswer(job: Job) {
  const qc = useQueryClient()
  const toast = useToast()
  return useMutation({
    mutationFn: (body: Parameters<typeof answerSheet>[1]) => answerSheet(job.id, body),
    onSuccess: (next) => {
      qc.setQueryData<Job[]>(keys.jobs, (list) => upsertJob(list, next))
      draftStore.set((d) => {
        const labels = { ...d.labels }
        delete labels[job.id]
        return { ...d, labels, tracked: d.tracked.includes(job.id) ? d.tracked : [...d.tracked, job.id] }
      })
    },
    onError: (e: Error) => toast(e.message, true),
  })
}

/**
 * A job whose image held several views of one subject: each figure boxed on the cut-out with
 * the view proposed for it. The user relabels, then builds from the views or keeps the image.
 * The labels are kept while the app runs, so leaving the screen does not undo them.
 */
export function SheetCard({ job, maxHeight = 440 }: { job: Job; /** The cut-out never grows past this height. */ maxHeight?: number }) {
  const sheet = job.sheet!
  const parts = sheet.parts ?? []
  const kept = useDraft().labels[job.id]
  const labels: (View | '')[] = kept?.length === parts.length ? kept : parts.map((p) => p.view)
  const setLabels = (next: (View | '')[]): void => draftStore.set((d) => ({ ...d, labels: { ...d.labels, [job.id]: next } }))
  const answer = useSheetAnswer(job)
  const chosen = chosenParts(parts, labels)
  const hasFront = labels.includes('front')
  const [w, h] = sheet.size

  return (
    <div className="sheet-card">
      <div className="sheet-figure" style={{ width: `min(100%, ${Math.round((maxHeight * w) / Math.max(1, h))}px)`, aspectRatio: `${w} / ${h}` }}>
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
          ? 'Check the labels: an outline cannot tell a front from a back, or which way a side view faces.'
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
