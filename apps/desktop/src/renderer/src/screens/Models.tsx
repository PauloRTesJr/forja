import { useMutation, useQuery } from '@tanstack/react-query'
import { api, type Model } from '../lib/api'
import { keys } from '../lib/events'
import { useToast } from '../lib/toast'

export function Models() {
  const toast = useToast()
  const models = useQuery({ queryKey: keys.models, queryFn: () => api<Model[]>('GET', '/v1/models') })
  const pause = useMutation({
    mutationFn: () => api<void>('POST', '/v1/engine/unload'),
    onSuccess: () => toast('Engine paused: all GPU memory freed'),
    onError: (e: Error) => toast(e.message, true),
  })
  return (
    <div className="screen">
      <div className="screen-head">
        <div>
          <h1>Models</h1>
          <p className="sub">What the local engine can run, and under which license.</p>
        </div>
        <button className="btn" onClick={() => pause.mutate()} disabled={pause.isPending}>
          Pause engine
        </button>
      </div>
      {models.isPending && <div className="skeleton" style={{ height: 160 }} />}
      {models.error && <div className="error">{(models.error as Error).message}</div>}
      {models.data && (
        <table className="table">
          <thead>
            <tr>
              <th>Model</th>
              <th>Stages</th>
              <th>License</th>
              <th>Device</th>
              <th>Status</th>
            </tr>
          </thead>
          <tbody>
            {models.data.map((m, i) => (
              <tr key={m.id} style={{ animationDelay: `${i * 30}ms` }}>
                <td>
                  <strong>{m.name}</strong>
                  <div className="muted mono">{m.id}</div>
                </td>
                <td>{m.tasks.join(', ')}</td>
                <td>
                  {m.license.spdx}
                  {!m.license.commercial && <span className="status failed" style={{ marginLeft: 6 }}>non-commercial</span>}
                  {m.license.restrictions.length > 0 && <div className="muted">{m.license.restrictions.join(', ')}</div>}
                </td>
                <td>{m.device ?? <span className="muted">remote only</span>}</td>
                <td>
                  <span className={`status ${m.status === 'loaded' ? 'running' : ''}`}>{m.status}</span>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  )
}
