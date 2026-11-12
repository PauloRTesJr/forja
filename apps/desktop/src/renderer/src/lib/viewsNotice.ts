// What to tell the user when the model of a step does not read the other views of the subject
// they staged. The CLI says the same in core/internal/cli/viewsnote.go.
import type { Pick, View } from './api'

export interface ViewsNote {
  stage: string
  /** The views the model leaves unread, and what that means for the result. */
  lead: string
  /** The model that would read them; absent when none could run here. */
  reader?: string
  /** The reader is not on its provider yet, so installing it is the way to use it. */
  installable?: boolean
}

/** Views as people list them: "the left view", "the left and back views", "the left, back and right views". */
export function theViews(views: View[]): string {
  const list = views.length > 1 ? `${views.slice(0, -1).join(', ')} and ${views[views.length - 1]}` : views.join('')
  return `the ${list} ${views.length === 1 ? 'view' : 'views'}`
}

/** What each stage type makes, in the words of a sentence about it. */
function work(use: string): { verb: string; what: string } {
  if (use === 'shape') return { verb: 'shapes', what: 'the shape' }
  if (use === 'texture' || use.startsWith('texture.')) return { verb: 'paints', what: 'the texture' }
  return { verb: 'works', what: 'the result' }
}

/** The sentence for one step, or undefined when its model reads every view the job has. */
export function viewsNote(p: Pick): ViewsNote | undefined {
  const unread = p.unread_views ?? []
  if (p.skipped || unread.length === 0) return undefined
  const { verb, what } = work(p.use)
  const model = p.model_name || p.model || p.use
  const partial = (p.views ?? []).some((v) => v !== 'front')
  const lead = partial
    ? `${model} does not read ${theViews(unread)}: ${unread.length === 1 ? 'it' : 'they'} will not change ${what}.`
    : `${model} ${verb} from the front alone: ${theViews(unread)} will not change ${what}.`
  const by = p.read_by
  if (!by) return { stage: p.stage, lead }
  const reads = unread.filter((v) => by.reads.includes(v))
  const object = reads.length === unread.length ? (reads.length === 1 ? 'it' : 'them') : theViews(reads)
  const name = by.model_name || by.model
  if (by.installed) return { stage: p.stage, lead, reader: `${name} reads ${object}, but cannot run on ${by.provider_label || by.install_on} yet.` }
  const elsewhere = by.install_on !== 'local' ? ` It installs on ${by.provider_label || by.install_on}.` : ''
  return { stage: p.stage, lead, reader: `${name} reads ${object}.${elsewhere}`, installable: true }
}

/** The notes for a plan, in step order. */
export function viewsNotes(plan: Pick[]): ViewsNote[] {
  return plan.flatMap((p) => viewsNote(p) ?? [])
}
