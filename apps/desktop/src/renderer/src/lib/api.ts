// Typed access to the core API. Every request goes through forja://core, proxied by main.
export const CORE = 'forja://core'

export type Status = 'queued' | 'running' | 'succeeded' | 'failed' | 'cancelled'

export interface Output {
  hash: string
  kind: string
  preview?: boolean
}

export interface Stage {
  id: string
  use: string
  model?: string
  status: Status
  key: string
  cached: boolean
  provider?: string
  outputs: Output[]
  seconds: number
  step: number
  total: number
  error?: string
}

export interface Job {
  id: string
  name: string
  preset: string
  input: string
  project?: string
  priority: string
  status: Status
  error?: string
  created_at: string
  finished_at?: string
  asset_id?: string
  stages: Stage[]
  estimate_seconds?: number
}

export interface Asset {
  id: string
  job_id: string
  name: string
  preset: string
  glb: string
  input: string
  thumbnail?: string
  stats?: Record<string, number | boolean | string>
  project?: string
  export_path?: string
  created_at: string
}

export interface PresetStage {
  id: string
  use: string
  model?: string
  params?: Record<string, unknown>
}

export interface Preset {
  id: string
  name: string
  description: string
  stages: PresetStage[]
  source: string
}

export interface Model {
  id: string
  name: string
  family: string
  tasks: string[]
  license: { spdx: string; commercial: boolean; restrictions: string[] }
  status: string
  device: string | null
}

export interface ProviderRow {
  id: string
  kind: string
  paid: boolean
  healthy: boolean
  latency_ms: number
  error?: string
}

export class ApiError extends Error {
  constructor(
    readonly status: number,
    message: string,
  ) {
    super(message)
  }
}

export async function api<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(CORE + path, {
    method,
    headers: body === undefined ? undefined : { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  if (!res.ok) {
    let detail = res.statusText
    try {
      detail = ((await res.json()) as { detail?: string }).detail ?? detail
    } catch {
      /* not a problem document */
    }
    throw new ApiError(res.status, detail)
  }
  if (res.status === 204 || res.status === 202) {
    const text = await res.text()
    return (text ? JSON.parse(text) : undefined) as T
  }
  return (await res.json()) as T
}

export const artifactURL = (hash: string, type?: string): string =>
  `${CORE}/v1/artifacts/${hash}${type ? `?type=${encodeURIComponent(type)}` : ''}`

export interface SubmitBody {
  paths: string[]
  preset: string
  project?: string
  priority?: 'interactive' | 'batch'
  overrides?: Record<string, unknown>
}

export const submitJobs = (body: SubmitBody): Promise<Job[]> => api('POST', '/v1/jobs', body)
export const cancelJob = (id: string): Promise<void> => api('DELETE', `/v1/jobs/${id}`)
