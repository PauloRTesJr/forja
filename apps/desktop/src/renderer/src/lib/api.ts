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
  optional?: { label: string; default: boolean }
}

/** Where one stage of a preset would run right now, and with which model. */
export interface Pick {
  stage: string
  use: string
  model?: string
  model_name?: string
  provider?: string
  missing?: boolean
}

export interface Preset {
  id: string
  name: string
  description: string
  stages: PresetStage[]
  source: string
  builtin: boolean
  plan: Pick[]
  ready: boolean
  error?: string
}

export interface License {
  spdx: string
  commercial: boolean
  restrictions: string[]
  url?: string
}

/** How a model fits this machine: in VRAM, with part of it in RAM, below its minimum, or not at all. */
export type Fit = 'fits' | 'offload' | 'too-big' | 'unsupported'

export interface Model {
  id: string
  name: string
  family: string
  tasks: string[]
  license: License
  status: 'ready' | 'loaded' | 'not-installed' | string
  device: string | null
  description?: string
  disk_gb?: number
  vram_gb?: number
  runs_on?: string | null
  fit?: Fit
  stub?: boolean
  rank?: number
  provider: string
  needs_acceptance: boolean
  accepted_at?: string
  /** Stage types this model does when a preset leaves the model open. */
  uses: { use: string; pinned: boolean }[]
}

export interface Fix {
  label: string
  runnable: boolean
  command: string
  url: string
  steps: string[]
  admin: boolean
}

export interface Check {
  id: string
  label: string
  ok: boolean
  found: string
  need: string
  why: string
  fix: Fix | null
  models: string[]
}

export interface Checks {
  lane: string
  ok: boolean
  checks: Check[]
}

export type TaskStatus = 'queued' | 'running' | 'succeeded' | 'failed' | 'cancelled'

/** An install, removal or fix running on a provider, as its latest state. */
export interface InstallTask {
  provider: string
  id: string
  kind: 'install' | 'remove' | 'fix'
  target: string
  status: TaskStatus
  steps: { id: string; label: string; status: 'pending' | 'running' | 'done' | 'skipped' | 'failed' }[]
  progress: { step: string; done: number; total: number } | null
  log: string[]
  error: string | null
  created_at: number
  version: number
}

export const taskDone = (t: InstallTask): boolean => t.status === 'succeeded' || t.status === 'failed' || t.status === 'cancelled'

export interface SetupStage {
  use: string
  status: 'ready' | 'install' | 'remote'
  model?: string
  model_name?: string
  provider?: string
  disk_gb?: number
  fit?: Fit
}

export interface Setup {
  system: {
    os?: string
    devices?: { id: string; backend: string; name: string; vram_bytes: number; driver?: string | null }[]
    disk_free_bytes?: number
  } | null
  stages: SetupStage[]
  install: string[]
  disk_gb: number
  ready: boolean
}

export const installModel = (id: string, acceptLicense: boolean, provider?: string): Promise<InstallTask> =>
  api('POST', `/v1/models/${id}/install`, { accept_license: acceptLicense, provider })
export const removeModel = (id: string): Promise<InstallTask> => api('DELETE', `/v1/models/${id}`)
export const runFix = (check: string): Promise<InstallTask> => api('POST', `/v1/checks/${encodeURIComponent(check)}/fix`)
export const cancelTask = (t: InstallTask): Promise<void> => api('DELETE', `/v1/installs/${t.provider}/${t.id}`)
export const pinModel = (use: string, model: string): Promise<void> => api('PUT', `/v1/models/stages/${use}`, { model })

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

/** Models as the provider reports them now; asking may start the local engine. */
export const fetchModels = (): Promise<Model[]> => api('GET', '/v1/models?fresh=1')

export interface Setting {
  key: string
  value: unknown
  default: unknown
  /** The layer that set it: default, system, user, project, env or flag. */
  source: string
}

export interface Settings {
  settings: Setting[]
  files: { user: string; project?: string }
  data_dir: string
  /** When engine settings apply: "restarted" now, or at the "next-start". */
  engine?: 'restarted' | 'next-start'
}

export const saveSettings = (set: Record<string, unknown>): Promise<Settings> => api('PATCH', '/v1/settings', { set })
