import type { CSSProperties } from 'react'
import type { Stage } from '../lib/api'
import { whereItRan } from '../lib/estimate'
import { stageName } from '../lib/format'
import { phaseState, phasesOf, stageFraction, stageTime } from '../lib/jobs'
import { Star } from './icons'

const starClass = (s: Stage): string => `star-slot ${s.status} ${s.cached ? 'cached' : ''}`

/** The pipeline as phases; each phase's rule fills with transform only as its stages finish. */
export function StageTrack({ stages }: { stages: Stage[] }) {
  return (
    <div className="phases">
      {phasesOf(stages).map((p) => {
        const state = phaseState(p)
        const done = p.stages.filter((s) => s.status === 'succeeded').length
        const fraction = p.stages.reduce((t, s) => t + stageFraction(s), 0) / p.stages.length
        // Sized by its kinds of step: another view's copies of them share the phase's room.
        const size = new Set(p.stages.map((s) => s.use)).size
        return (
          <div key={p.stages[0]!.id} className={`phase ${state}`} style={{ '--size': size, '--p': fraction } as CSSProperties}>
            <div className="phase-head">
              <span className="phase-name">{p.label}</span>
              <span className="phase-tally">{state === 'failed' ? 'stopped' : `${done} of ${p.stages.length}`}</span>
            </div>
            <div className="phase-rule" aria-hidden>
              <i />
            </div>
            <div className="phase-stars">
              {p.stages.map((s) => (
                <span key={s.id} className={starClass(s)} title={`${stageName(s)}: ${s.cached ? 'cached' : s.status}`}>
                  <Star />
                </span>
              ))}
            </div>
          </div>
        )
      })}
    </div>
  )
}

/** Every stage on its own line: what ran it, where when not here, and how long it took. */
export function StageList({ stages }: { stages: Stage[] }) {
  return (
    <ol className="stage-list">
      {stages.map((s) => {
        const where = whereItRan(s)
        return (
          <li key={s.id} className={`${s.status} ${s.cached ? 'cached' : ''}`}>
            <Star size={13} />
            <span className="stage-name">{stageName(s)}</span>
            <span className="stage-model" title={where}>
              {s.model ?? 'built-in'}
              {where && ` · ${where}`}
            </span>
            <span className="stage-time">{stageTime(s)}</span>
            {s.status === 'running' && s.note && <span className="stage-note">{s.note}</span>}
          </li>
        )
      })}
    </ol>
  )
}
