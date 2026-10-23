import { useQuery } from '@tanstack/react-query'
import { Choices, Row } from './Controls'
import { api, type ProviderRow, type Setting } from '../lib/api'
import { keys } from '../lib/events'
import { SectionHead } from './ScreenHead'

/** Where models run: this computer and the providers in config, and how stages pick one. */
export function Providers({ settings, onSet }: { settings: Map<string, Setting>; onSet: (key: string, value: unknown) => void }) {
  const providers = useQuery({ queryKey: keys.providers, queryFn: () => api<ProviderRow[]>('GET', '/v1/providers') })
  const paid = providers.data?.some((p) => p.paid) ?? false
  return (
    <div className="section">
      <SectionHead>Where models run</SectionHead>
      <div className="settings">
        {providers.data && (
          <ul className="provider-list">
            {providers.data.map((p) => (
              <li key={p.id} className="provider">
                <span className={`dot ${p.healthy ? 'ok' : ''}`} />
                <strong>{p.id === 'local' ? 'This computer' : p.id}</strong>
                <span className="muted">{p.healthy ? `${p.latency_ms} ms` : p.error === 'idle' ? 'idle, starts when needed' : p.error}</span>
              </li>
            ))}
          </ul>
        )}
        <Row setting={settings.get('routing.policy')} label="Which provider a stage uses" onReset={onSet}>
          {(s, locked) => (
            <Choices
              value={String(s.value)}
              disabled={locked}
              onChange={(v) => onSet(s.key, v)}
              options={[
                { value: 'prefer-local', label: 'This computer first', help: 'Others only for models it cannot run.' },
                { value: 'offline-only', label: 'Never leave this network', help: 'Only this computer and your own engines.' },
              ]}
            />
          )}
        </Row>
        {paid && (
          <Row setting={settings.get('routing.allow_paid_fallback')} label="Let paid providers step in" help="When no free provider can run a stage." onReset={onSet}>
            {(s, locked) => (
              <button className="switch" role="switch" aria-checked={s.value === true} disabled={locked} onClick={() => onSet(s.key, s.value !== true)}>
                <span className="switch-track" aria-hidden />
                {s.value === true ? 'On' : 'Off'}
              </button>
            )}
          </Row>
        )}
      </div>
    </div>
  )
}
