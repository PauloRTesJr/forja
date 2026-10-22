import type { Stage } from '../lib/api'
import { stageLabel } from '../lib/format'
import { stageFraction } from '../lib/jobs'

/** One segment per stage; fills animate with transform only. */
export function StageTrack({ stages }: { stages: Stage[] }) {
  return (
    <>
      <div className="track" aria-hidden>
        {stages.map((s) => {
          const cls = [s.status, s.cached ? 'cached' : '', s.status === 'running' && s.total === 0 ? 'indeterminate' : '']
          return (
            <div key={s.id} className={`seg ${cls.join(' ')}`} title={`${stageLabel(s.use)}: ${s.cached ? 'cached' : s.status}`}>
              <i style={{ '--p': s.status === 'running' && s.total === 0 ? 1 : stageFraction(s) } as React.CSSProperties} />
            </div>
          )
        })}
      </div>
      <div className="track-labels">
        {stages.map((s) => (
          <span key={s.id} className={s.status}>
            {stageLabel(s.use)}
          </span>
        ))}
      </div>
    </>
  )
}
