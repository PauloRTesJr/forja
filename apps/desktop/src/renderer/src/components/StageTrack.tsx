import type { CSSProperties } from 'react'
import type { Stage } from '../lib/api'
import { whereItRan } from '../lib/estimate'
import { stageName } from '../lib/format'
import { phaseState, phasesOf, progressText, stageFraction, stageTime } from '../lib/jobs'

/**
 * The pipeline as a segmented bar: a segment per stage, grouped by phase. Each segment fills
 * with transform only, as its stage moves. `compact` leaves out the phase names, for a row that
 * has no room for them.
 */
export function StageTrack({ stages, compact, label = 'Stages' }: { stages: Stage[]; compact?: boolean; label?: string }) {
  const done = stages.filter((s) => s.status === 'succeeded').length
  return (
    <div
      className={`track ${compact ? 'compact' : ''}`}
      role="progressbar"
      aria-label={label}
      aria-valuemin={0}
      aria-valuemax={stages.length}
      aria-valuenow={done}
      aria-valuetext={progressText(stages)}
    >
      {phasesOf(stages).map((p) => {
        const state = phaseState(p)
        const finished = p.stages.filter((s) => s.status === 'succeeded').length
        // Sized by its kinds of step: another view's copies of them share the phase's room.
        const size = new Set(p.stages.map((s) => s.use)).size
        return (
          <div key={p.stages[0]!.id} className={`track-phase ${state}`} style={{ '--size': size } as CSSProperties}>
            {!compact && (
              <div className="track-head">
                <span className="track-name">{p.label}</span>
                <span className="track-tally">{state === 'failed' ? 'stopped' : `${finished}/${p.stages.length}`}</span>
              </div>
            )}
            <div className="track-segs">
              {p.stages.map((s) => (
                <span key={s.id} className={`seg ${s.status}`} title={`${stageName(s)}: ${s.status}`} style={{ '--p': stageFraction(s) } as CSSProperties}>
                  <i />
                </span>
              ))}
            </div>
          </div>
        )
      })}
    </div>
  )
}

/** Every stage on its own line, in the order they run: what ran it, where when not here, and how long it took. */
export function StageList({ stages }: { stages: Stage[] }) {
  return (
    <table className="table compact flush stage-list">
      <thead>
        <tr>
          <th>Stage</th>
          <th>Model</th>
          <th className="n">Time</th>
        </tr>
      </thead>
      <tbody>
        {stages.map((s) => {
          const where = whereItRan(s)
          return (
            <tr key={s.id} className={s.status}>
              <td>
                <span className="stage-name">
                  <i className="stage-dot" aria-hidden />
                  {stageName(s)}
                </span>
                {s.status === 'running' && s.note && <span className="stage-note">{s.note}</span>}
                {s.status === 'succeeded' &&
                  s.outputs
                    .filter((o) => o.note)
                    .map((o) => (
                      <span key={o.hash} className="stage-note warn">
                        {o.note}
                      </span>
                    ))}
              </td>
              <td className="stage-model" title={where}>
                {s.model ?? 'built-in'}
                {where && ` · ${where}`}
              </td>
              <td className="n stage-time">{stageTime(s)}</td>
            </tr>
          )
        })}
      </tbody>
    </table>
  )
}
