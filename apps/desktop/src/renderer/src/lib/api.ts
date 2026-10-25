// Typed access to the core API. Every request goes through forja://core, proxied by main.
export const CORE = 'forja://core'

/** `waiting`: the job needs the user's answer (its image holds several views of its subject). */
export type Status = 'queued' | 'running' | 'waiting' | 'succeeded' | 'failed' | 'cancelled'

/** The views a reference image can show. `left` faces the image's left edge. */
export type View = 'front' | 'left' | 'back' | 'right'

/** One region of a sheet of views: a figure, or a close-up cut by its frame. */
export interface SheetPart {
  /** x0, y0, x1, y1 in the cut-out's pixels. */
  box: [number, number, number, number]
  kind?: 'figure' | 'detail'
  /** The view it shows, or '' to leave it out. */
  view: View | ''
}

/** What the core found when the job's image looked like several views of one subject. */
export interface Sheet {
  mode: 'ask' | 'split' | 'keep'
  checked: boolean
  found: boolean
  /** The background-removal output the boxes are in, and its size. */
  cutout?: string
  size: [number, number]
  parts?: SheetPart[]
  answer?: 'split' | 'keep'
  chosen?: SheetPart[]
}

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
  /** What the provider last said while it runs, such as a place in a hosted model's queue. */
  note?: string
  /** The providers it left after failing there, oldest first. */
  fallbacks?: Fallback[]
  /** Where it runs, for people: "This computer", "Studio PC", "fal.ai". */
  provider_label?: string
}

/** A stage moving to another provider after it could not be reached or ran out of memory. */
export interface Fallback {
  from: string
  to: string
  reason: string
  from_label?: string
  to_label?: string
}

export interface Job {
  id: string
  name: string
  preset: string
  input: string
  /** Images of the subject by view; a `front` cut out of a sheet stands in for `input`. */
  views?: Partial<Record<View, string>>
  sheet?: Sheet
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

/** A format the library exports to, and whether this computer can write it now. */
export interface ExportFormat {
  id: string
  name: string
  /** The main file's extension; some formats write their textures beside it. */
  ext: string
  /** Who reads the format and what it keeps of the asset. */
  for: string
  /** Made by Blender from the asset's GLB. */
  blender: boolean
  available: boolean
  problem?: string
}

export interface Blender {
  path: string
  version: string
}

export interface ExportFormats {
  blender?: Blender
  /** Why there is no Blender to use: not found, or too old. */
  blender_problem?: string
  formats: ExportFormat[]
}

/** What became of each asset of an export, in the order asked. */
export interface ExportResult {
  format: string
  assets: { asset: string; files?: string[]; error?: string }[]
}

export const fetchExportFormats = (): Promise<ExportFormats> => api('GET', '/v1/export/formats')
/** Writes assets in a format: to `path` when it names one file of that format, else into the folder `path`. */
export const exportAssets = (ids: string[], format: string, path: string): Promise<ExportResult> =>
  api('POST', '/v1/export', { ids, format, path })
/** Starts Blender with the asset imported into a new, unsaved file. */
export const openInBlender = (id: string): Promise<Blender> => api('POST', `/v1/assets/${id}/blender`)
/** Removes an asset, and the job that made it, from the library; exported files stay. */
export const deleteAsset = (id: string): Promise<void> => api('DELETE', `/v1/assets/${id}`)

/** How the object store's bytes divide: what the library keeps, and the trimmable cache. */
export interface CacheUsage {
  objects: number
  bytes: number
  /** Assets, their images and unfinished jobs: never trimmed. */
  library_bytes: number
  /** Stage outputs a new job can reuse, trimmed least recently used first. */
  cache_bytes: number
  /** general.cache_max_gb in bytes; 0 means no limit. */
  limit_bytes: number
}

export const fetchCache = (): Promise<CacheUsage> => api('GET', '/v1/cache')
/** Removes every reusable stage output now; refused while jobs run. */
export const clearCache = (): Promise<{ freed: { objects: number; bytes: number; entries: number }; usage: CacheUsage }> =>
  api('DELETE', '/v1/cache')

/** Median, 90th percentile and maximum of `n` values. */
export interface Spread {
  n: number
  median: number
  p90: number
  max: number
}

export interface BenchPreset {
  preset: string
  images: number
  failed: number
  /** Image to GLB, succeeded samples only. */
  seconds: Spread
  budget_seconds?: number
  over_budget: number
}

export interface BenchStage {
  preset: string
  stage: string
  use: string
  model?: string
  seconds: Spread
  load_seconds: Spread
  peak_vram_bytes: number | null
  peak_ram_bytes: number | null
  /** Mean percent of time the GPU ran kernels. */
  gpu_util: number | null
  triangles: number | null
  /** Share (0..1) of samples whose mesh was watertight. */
  watertight: number | null
  uv_coverage: number | null
}

/** One reference image through one preset, measured. */
export interface BenchSample {
  image: string
  category?: string
  preset: string
  repeat: number
  job?: string
  input?: string
  status: Status
  error?: string
  wall_seconds: number
  stage_seconds: number
  load_seconds: number
  core_overhead_seconds: number
}

export interface BenchDevice {
  id: string
  provider: string
  gpu?: string
  cpu?: string
  vram_bytes?: number
}

export interface BenchBuild {
  version: string
  commit?: string
  modified?: boolean
  engine?: string
}

export interface BenchSuite {
  name: string
  version: number
  path: string
}

export interface BenchOptions {
  presets: string[]
  cold: boolean
  batch: boolean
  repeat: number
}

export type BenchStatus = 'running' | 'succeeded' | 'failed' | 'cancelled'

/** A run of a benchmark suite: kept by the core, pushed while it runs. */
export interface BenchResult {
  id: string
  label?: string
  status: BenchStatus
  error?: string
  /** While running: loading the models with an unmeasured image, or measuring. */
  phase?: 'warm-up' | 'measuring'
  /** The job loading the models during the warm-up. */
  warm_up?: string
  suite: BenchSuite
  options: BenchOptions
  forja: BenchBuild
  device: BenchDevice
  started_at: string
  finished_at?: string
  total: number
  skipped: { path: string; reason: string }[]
  jobs: BenchSample[]
  summary?: {
    presets: BenchPreset[]
    stages: BenchStage[]
    core_overhead: Spread
    core_overhead_budget_ms?: number
    devices: { device: string; busy_seconds: number; busy: number; gpu_util: number | null }[]
    wall_seconds: number
  }
  report?: string
}

/** A kept result as the list shows it. */
export interface BenchEntry {
  id: string
  label?: string
  status: BenchStatus
  suite: BenchSuite
  options: BenchOptions
  forja: BenchBuild
  device: BenchDevice
  started_at: string
  samples: number
  failed: number
  presets: BenchPreset[]
  core_overhead: Spread
}

export interface BenchList {
  running: BenchResult | null
  results: BenchEntry[]
}

export interface BenchDelta {
  preset?: string
  stage?: string
  metric: 'seconds' | 'peak_vram_bytes' | 'core_overhead' | 'failed'
  baseline: number
  current: number
  /** Change in percent of the baseline; null when the baseline is zero. */
  pct: number | null
  regression: boolean
}

export interface BenchComparison {
  baseline: { id: string; forja: BenchBuild; started_at: string }
  current: { id: string; forja: BenchBuild; started_at: string }
  thresholds: { pct: number; min_seconds: number; min_vram_mb: number }
  deltas: BenchDelta[]
  regressions: number
  warnings: string[]
}

export const fetchBenchList = (): Promise<BenchList> => api('GET', '/v1/bench')
export const fetchBench = (id: string): Promise<BenchResult> => api('GET', `/v1/bench/${id}`)
/** Runs a suite file in the background; bench.updated events follow it. */
export const startBench = (body: { suite: string; cold?: boolean; batch?: boolean }): Promise<BenchResult> =>
  api('POST', '/v1/bench', body)
export const cancelBench = (id: string): Promise<void> => api('DELETE', `/v1/bench/${id}`)
/** Compares two kept results with the thresholds in the config. */
export const compareBench = (baseline: string, current: string): Promise<BenchComparison> =>
  api('POST', '/v1/bench/compare', { baseline, current })

export interface PresetStage {
  id: string
  use: string
  model?: string
  params?: Record<string, unknown>
  optional?: { label: string; help?: string; default: boolean }
}

/** Where one stage of a preset would run right now, and with which model. */
export interface Pick {
  stage: string
  use: string
  model?: string
  model_name?: string
  provider?: string
  /** Where it would run, for people: "This computer", "Studio PC", "fal.ai". */
  provider_label?: string
  missing?: boolean
  /** Done by the model of the stage named in `by` (a hosted model that returns a textured mesh). */
  skipped?: boolean
  by?: string
  paid?: boolean
  price?: Price
  /** The views of the subject the model reads; absent when it takes the front alone. */
  views?: View[]
  /** Views it cannot run without that the job lacks. */
  needs_views?: View[]
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
export type Fit = 'fits' | 'offload' | 'too-big' | 'unsupported' | 'hosted'

/** What a hosted model charges per unit (usually one generation). */
export interface Price {
  amount: number
  unit: string
  currency: string
}

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
  provider_label: string
  paid: boolean
  price?: Price
  recommended?: boolean
  thumbnail?: string
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
/** Pins the model (and the provider that runs it) a stage uses; an empty model means automatic. */
export const pinModel = (use: string, model: string, provider?: string): Promise<void> =>
  api('PUT', `/v1/models/stages/${use}`, { model, provider })

export interface ProviderRow {
  id: string
  label: string
  kind: string
  url?: string
  paid: boolean
  healthy: boolean
  latency_ms: number
  error?: string
  /** The pinned certificate, short, for comparing with what the other computer shows. */
  fingerprint?: string
  /** Where its key lives and how it ends; never the key itself. */
  key?: { stored: boolean; hint?: string; store: string }
  editable: boolean
  /** Rented GPUs: who rents it (runpod, modal), a RunPod endpoint, what a GPU second costs. */
  vendor?: string
  endpoint_id?: string
  price_usd_per_second?: number
}

export interface ShareView {
  enabled: boolean
  active: boolean
  port: number
  addresses: string[]
  fingerprint?: string
  pending?: 'restart'
}

export const addProvider = (body: { url: string; label?: string; code?: string; token?: string }): Promise<{ id: string; label: string }> =>
  api('POST', '/v1/providers', body)
export const removeProvider = (id: string): Promise<void> => api('DELETE', `/v1/providers/${id}`)
export const replaceToken = (id: string, token: string): Promise<void> => api('PUT', `/v1/providers/${id}/token`, { token })
export const setSharing = (enabled: boolean): Promise<ShareView> => api('PUT', '/v1/share', { enabled })
export const pairingCode = (): Promise<{ code: string; expires_at: string }> => api('POST', '/v1/share/code')

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
  /** Several images, one job each; or one front image as `input` (a store ref) with its `views`. */
  paths?: string[]
  input?: string
  name?: string
  views?: Partial<Record<View, string>>
  /** What to do when an image holds several views of its subject: the app asks the user. */
  sheet?: 'ask' | 'split' | 'keep'
  preset: string
  project?: string
  priority?: 'interactive' | 'batch'
  overrides?: Record<string, unknown>
}

export const submitJobs = (body: SubmitBody): Promise<Job[]> => api('POST', '/v1/jobs', body)
export const cancelJob = (id: string): Promise<void> => api('DELETE', `/v1/jobs/${id}`)
/** The views to cut out of a job's sheet, or `keep` to use the image whole. */
export const answerSheet = (id: string, body: { views: SheetPart[] } | { keep: true }): Promise<Job> =>
  api('POST', `/v1/jobs/${id}/sheet`, body)
/** Copies a picked file into the core's store and returns its ref. */
export const importImage = async (path: string): Promise<string> =>
  (await api<{ hash: string }>('POST', '/v1/artifacts/import', { path })).hash
/** Copies an image that has no file, such as a pasted screenshot, into the core's store. */
export async function uploadImage(image: Blob): Promise<string> {
  const res = await fetch(CORE + '/v1/artifacts', { method: 'PUT', body: image })
  if (!res.ok) throw new ApiError(res.status, res.statusText)
  return ((await res.json()) as { hash: string }).hash
}
/** Presets resolved for a job that also brings these views, so the app sees which model reads them. */
export const fetchPresets = (views?: View[]): Promise<Preset[]> =>
  api('GET', views?.length ? `/v1/presets?views=${views.join(',')}` : '/v1/presets')

/** Models as the provider reports them now; asking may start the local engine. */
export const fetchModels = (): Promise<Model[]> => api('GET', '/v1/models?all=1&fresh=1')

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

/** One model of a hub's catalog, as Forja could use it. */
export interface CatalogModel {
  id: string
  name: string
  description: string
  thumbnail?: string
  license: License
  price?: Price
  tasks: string[]
  /** Why Forja cannot call it; absent when it can. */
  problem?: string
  added: boolean
  recommended: boolean
}

export interface CatalogPage {
  models: CatalogModel[]
  next_cursor?: string
}

export interface Spend {
  currency: string
  today: number
  month: number
  limits: { per_job: number; per_day: number; per_month: number }
}

export const searchCatalog = (provider: string, q: string, cursor?: string): Promise<CatalogPage> =>
  api('GET', `/v1/providers/${provider}/catalog?q=${encodeURIComponent(q)}${cursor ? `&cursor=${encodeURIComponent(cursor)}` : ''}`)
/** A catalog picture, relayed by the core: the page loads images only from it. */
export const thumbnailURL = (provider: string, url: string): string =>
  `${CORE}/v1/providers/${provider}/thumbnail?url=${encodeURIComponent(url)}`
export const addHubModel = (provider: string, model: string): Promise<void> => api('POST', `/v1/providers/${provider}/models`, { model })
export const removeHubModel = (provider: string, model: string): Promise<void> =>
  api('DELETE', `/v1/providers/${provider}/models?model=${encodeURIComponent(model)}`)
export const connectHub = (vendor: string, token: string): Promise<{ id: string; label: string }> =>
  api('POST', '/v1/providers', { kind: 'api', vendor, token })

/** A job as Generate would submit it, to estimate before queueing. */
export interface EstimateBody {
  preset: string
  project?: string
  /** The other views it brings: left, back, right. */
  views?: View[]
  overrides?: Record<string, unknown>
  /** How many images would be queued this way. */
  images?: number
}

/** One stage of an estimate: where it runs, how long it took there before, what it costs. */
export interface StageEstimate extends Pick {
  seconds?: number
  /** Where the time comes from: earlier runs on this provider, or on others. */
  measured?: 'here' | 'elsewhere'
  cost_usd?: number
  /** False for a paid stage without a usable price. */
  priced: boolean
}

/** What one image would take and cost, before it is queued, against the spending limits. */
export interface Estimate {
  preset: string
  images: number
  stages: StageEstimate[]
  /** Per image; unmeasured names the stages without a time, unpriced the paid ones without a price. */
  seconds: number
  cost_usd: number
  unmeasured: string[]
  unpriced: string[]
  paid: boolean
  spend: { limits: { per_job: number; per_day: number; per_month: number }; today: number; month: number; queued: number }
  /** Why the job would be refused now: no model for a stage, a spending limit. */
  problem?: string
}

export const fetchEstimate = (body: EstimateBody): Promise<Estimate> => api('POST', '/v1/estimate', body)

export type PriceUnit = 'second' | 'hour' | 'generation'

/** One entry of the price table, or a hosted model's own price that no entry replaces. */
export interface PriceRow {
  id: string
  label?: string
  usd: number
  /** A unit of the table, or the vendor's own unit for a hosted model priced another way. */
  per: string
  /** The layer that set it (default, user, project…), or "vendor" for a hosted model's own price. */
  source: string
  default?: { usd: number; per: PriceUnit; label?: string }
  vendor?: Price
  /** What it prices, for people. */
  for: string[]
  model?: string
  provider?: string
}

export interface Prices {
  prices: PriceRow[]
  units: PriceUnit[]
  file: string
}

// A hosted model's id keeps its slashes in the address.
const pricePath = (id: string): string => `/v1/prices/${id.split('/').map(encodeURIComponent).join('/')}`

export const fetchPrices = (): Promise<Prices> => api('GET', '/v1/prices')
export const setPrice = (id: string, price: { usd: number; per: PriceUnit; label?: string }): Promise<Prices> =>
  api('PUT', pricePath(id), price)
/** Removes the user's entry: a built-in one goes back to its default, a hosted model to its provider's price. */
export const removePrice = (id: string): Promise<Prices> => api('DELETE', pricePath(id))
/** A kind of provider the core can add, field by field (GET /v1/provider-kinds). */
export interface ProviderKind {
  kind: string
  vendor?: string
  label: string
  description: string
  pairing: boolean
  paid: boolean
  key_url?: string
  fields: ProviderField[]
}

export interface ProviderField {
  key: string
  label: string
  type: 'text' | 'url' | 'secret' | 'number' | 'workflows'
  placeholder?: string
  help?: string
  optional?: boolean
  unit?: string
}

/** What trying a provider's settings found. */
export interface ProbeView {
  url: string
  version: string
  gpu?: string
  fingerprint?: string
  latency_ms: number
  detail?: string
}

export const providerKinds = (): Promise<ProviderKind[]> => api('GET', '/v1/provider-kinds')
export const testProvider = (body: Record<string, unknown>): Promise<ProbeView> => api('POST', '/v1/providers/test', body)
export const addCloudProvider = (body: Record<string, unknown>): Promise<{ id: string; label: string; probe?: ProbeView }> =>
  api('POST', '/v1/providers', body)
