import { useEffect, useState } from 'react'
import type { Pick } from '../lib/api'
import { viewsNotes, type ViewsNote } from '../lib/viewsNotice'

/** How long the notice takes to leave, as a last resort if the browser reports no animation end. */
const LEAVE_MS = 300

/**
 * Says, once side views are staged, which step's model will not read them, and which model
 * would. A calm line: the job still runs as it is. It rises in when it appears and fades out
 * when the plan changes so that it no longer applies (a model that reads the views was
 * installed), instead of popping away.
 */
export function ViewsNotice({ plan, onOpenModels }: { plan: Pick[]; onOpenModels: () => void }) {
  const notes = viewsNotes(plan)
  const key = notes.map((n) => `${n.stage} ${n.lead} ${n.reader ?? ''} ${n.installable}`).join('\n')
  // The last notes shown, kept while the notice fades out.
  const [held, setHeld] = useState<{ key: string; notes: ViewsNote[] }>({ key, notes })
  const [gone, setGone] = useState(notes.length === 0)
  if (notes.length > 0) {
    if (held.key !== key) setHeld({ key, notes })
    if (gone) setGone(false)
  }
  const leaving = notes.length === 0 && !gone
  useEffect(() => {
    if (!leaving) return
    const t = setTimeout(() => setGone(true), LEAVE_MS)
    return () => clearTimeout(t)
  }, [leaving])
  if (gone && notes.length === 0) return null
  const shown = notes.length > 0 ? notes : held.notes
  const canInstall = shown.some((n) => n.installable)
  return (
    <div className={`views-notice ${leaving ? 'leaving' : 'enter'}`} role="status" onAnimationEnd={() => leaving && setGone(true)}>
      {shown.map((n) => (
        <p key={n.stage}>
          {n.lead}
          {n.reader && <> {n.reader}</>}
        </p>
      ))}
      {canInstall && (
        <button className="text-btn" onClick={onOpenModels}>
          Open Models
        </button>
      )}
    </div>
  )
}
