import { useQuery } from '@tanstack/react-query'
import { api, type ProviderRow } from '../lib/api'
import { keys } from '../lib/events'

export function Settings() {
  const info = useQuery({ queryKey: keys.info, queryFn: () => api<Record<string, unknown>>('GET', '/v1/info') })
  const providers = useQuery({ queryKey: keys.providers, queryFn: () => api<ProviderRow[]>('GET', '/v1/providers') })
  const config = useQuery({ queryKey: keys.config, queryFn: () => api<Record<string, unknown>>('GET', '/v1/config/effective') })
  return (
    <div className="screen">
      <div className="screen-head">
        <div>
          <h1>Settings</h1>
          <p className="sub">Edit config.toml or a project's forja.toml; changes apply to new jobs without a restart.</p>
        </div>
      </div>
      <div className="section">
        <h2>Providers</h2>
        <table className="table">
          <thead>
            <tr>
              <th>Provider</th>
              <th>Kind</th>
              <th>Health</th>
              <th style={{ textAlign: 'right' }}>Latency</th>
            </tr>
          </thead>
          <tbody>
            {providers.data?.map((p) => (
              <tr key={p.id}>
                <td>
                  <strong>{p.id}</strong> {p.paid && <span className="status">paid</span>}
                </td>
                <td>{p.kind}</td>
                <td>
                  <span className={`dot ${p.healthy ? 'ok' : ''}`} style={{ display: 'inline-block', marginRight: 8 }} />
                  {p.healthy ? 'healthy' : p.error}
                </td>
                <td style={{ textAlign: 'right' }}>{p.healthy ? `${p.latency_ms} ms` : '—'}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <div className="section">
        <h2>System</h2>
        <pre className="code">{info.data ? JSON.stringify(info.data, null, 2) : '…'}</pre>
      </div>
      <div className="section">
        <h2>Effective configuration</h2>
        <pre className="code">{config.data ? JSON.stringify(config.data, null, 2) : '…'}</pre>
      </div>
    </div>
  )
}
