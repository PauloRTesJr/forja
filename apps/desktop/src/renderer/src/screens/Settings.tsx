import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { Choices, ErrorBlock, NumberInput, Pill, Row, Switch, TextInput } from '../components/Controls'
import { ChevronIcon, OpenExternalIcon, WaitingIcon } from '../components/icons'
import { ScreenHead, SectionHead } from '../components/ScreenHead'
import { Providers } from '../components/Providers'
import { api, fetchExportFormats, saveSettings, type Preset, type Settings as SettingsView } from '../lib/api'
import { keys } from '../lib/events'
import { bytes } from '../lib/format'
import { request } from '../lib/http'
import { engineNote, ruleFor } from '../lib/settings'
import { useToast } from '../lib/toast'

interface Info {
  version: string
  data_dir: string
  engine: {
    running: boolean
    /** Newer cores say more than running or not. */
    state?: 'stopped' | 'starting' | 'running' | 'failed'
    error?: string
    restart_pending?: boolean
  }
  system?: {
    os?: string
    devices?: { id: string; backend: string; name: string; vram_bytes: number; driver?: string | null }[]
    ram_total_bytes?: number
    disk_free_bytes?: number
  }
}

const GB = 1024 ** 3

function EnginePill({ engine }: { engine: Info['engine'] }) {
  switch (engine.state ?? (engine.running ? 'running' : 'stopped')) {
    case 'running':
      return (
        <Pill tone="ok" dot>
          Running
        </Pill>
      )
    case 'starting':
      return (
        <Pill tone="accent" live>
          Starting
        </Pill>
      )
    case 'failed':
      return (
        <Pill tone="bad" dot title={engine.error}>
          Could not start
        </Pill>
      )
  }
  return (
    <Pill dot title="It starts when a job needs it">
      Idle
    </Pill>
  )
}

export function Settings() {
  const toast = useToast()
  const qc = useQueryClient()
  const info = useQuery({ queryKey: keys.info, queryFn: () => api<Info>('GET', '/v1/info') })
  const settings = useQuery({ queryKey: keys.settings, queryFn: () => api<SettingsView>('GET', '/v1/settings') })
  const presets = useQuery({ queryKey: keys.presets, queryFn: () => api<Preset[]>('GET', '/v1/presets') })
  const exportFormats = useQuery({ queryKey: keys.exportFormats, queryFn: fetchExportFormats })
  const save = useMutation({
    mutationFn: saveSettings,
    onSuccess: (view) => {
      qc.setQueryData(keys.settings, view)
      const note = engineNote(view.engine)
      if (note) toast(note)
    },
  })
  const failed = (e: unknown): void => void toast(e instanceof Error ? e.message : String(e), true)
  /** Saves one setting; a field that waits for it shows a refusal under itself. */
  const set = (key: string, value: unknown): Promise<unknown> => save.mutateAsync({ [key]: value })
  /** For a switch, a choice or a reset, which have nowhere of their own to show a refusal. */
  const apply = (key: string, value: unknown): void => void set(key, value).catch(failed)

  const unload = useMutation({
    mutationFn: () => api<void>('POST', '/v1/engine/unload'),
    onSuccess: () => toast('GPU memory freed: every model was unloaded.'),
    onError: failed,
  })
  const copy = useMutation({
    mutationFn: async () => {
      const res = await request('GET', '/v1/diagnostics', {})
      await window.forja.copyText(await res.text())
    },
    onSuccess: () => toast('Diagnostics copied: hardware, versions, settings and the engine log. Your home folder is shown as ~.'),
    onError: failed,
  })
  const open = (fn: () => Promise<void>) => () => void fn().catch(failed)
  const [browsing, setBrowsing] = useState(false)
  const [advanced, setAdvanced] = useState<boolean>()

  const by = new Map(settings.data?.settings.map((s) => [s.key, s]))
  const gpu = info.data?.system?.devices?.find((d) => d.backend !== 'cpu')
  const engine = info.data?.engine
  // A newer core's info is the live answer; the settings view's own answer is only as old as the last save.
  const pending = engine?.state !== undefined ? engine.restart_pending === true : settings.data?.engine === 'pending'
  // A reserve as large as the card would leave no room for any model.
  const reserveMax = gpu ? Math.max(1, Math.floor(gpu.vram_bytes / GB) - 1) : 64
  const blender = exportFormats.data?.blender
  const stubs = by.get('models.stubs')
  const showAdvanced = advanced ?? (stubs ? stubs.value === true || stubs.source !== 'default' : false)

  const browseBlender = async (): Promise<void> => {
    const path = await window.forja.pickFile("Blender's program", window.forja.platform === 'win32' ? ['exe'] : ['*'])
    if (!path) return
    setBrowsing(true)
    try {
      await set('blender.path', path)
    } catch (e) {
      failed(e)
    } finally {
      setBrowsing(false)
    }
  }

  return (
    <div className="screen capped">
      <ScreenHead title="Settings" sub="Saved to config.toml as you change them.">
        <button className="btn ghost" onClick={open(window.forja.openConfig)}>
          <OpenExternalIcon />
          Open config.toml
        </button>
      </ScreenHead>
      {settings.error && <ErrorBlock>{(settings.error as Error).message}</ErrorBlock>}

      <div className="section">
        <SectionHead>This computer</SectionHead>
        {info.isPending && <div className="skeleton" style={{ height: 62 }} />}
        {info.error && <ErrorBlock>{(info.error as Error).message}</ErrorBlock>}
        {info.data && engine && (
          <dl className="facts group">
            <div className="facts-item wide">
              <dt>GPU</dt>
              <dd>{gpu?.name ?? 'None found'}</dd>
              <dd className="facts-detail">
                {gpu ? `${bytes(gpu.vram_bytes)} VRAM${gpu.driver ? ` · driver ${gpu.driver}` : ''}` : 'Models that need a GPU run on another computer'}
              </dd>
            </div>
            {info.data.system?.ram_total_bytes ? (
              <div className="facts-item">
                <dt>Memory</dt>
                <dd>{bytes(info.data.system.ram_total_bytes)}</dd>
              </div>
            ) : null}
            {info.data.system?.disk_free_bytes ? (
              <div className="facts-item">
                <dt>Free for models</dt>
                <dd>{bytes(info.data.system.disk_free_bytes)}</dd>
              </div>
            ) : null}
            <div className="facts-item">
              <dt>Engine</dt>
              <dd>
                <EnginePill engine={engine} />
              </dd>
            </div>
            <div className="facts-item">
              <dt>Forja</dt>
              <dd>{info.data.version}</dd>
            </div>
          </dl>
        )}
        {engine?.state === 'failed' && engine.error && <ErrorBlock>{engine.error}</ErrorBlock>}
        {!info.data?.system && info.data && <p className="muted small section-foot">The hardware shows here once the engine has run once.</p>}
      </div>

      <div className="section">
        <SectionHead>GPU and engine</SectionHead>
        <p className="section-note">Changing the first three restarts the engine, so loaded models load again on the next job.</p>
        <div className="settings group">
          <Row
            setting={by.get('engine.idle_unload_minutes')}
            label="Unload models when idle"
            help="After this long without work, so other apps and games get the GPU back. 0 keeps models loaded."
            unit="minutes"
            onReset={apply}
          >
            {(s, locked) => (
              <NumberInput
                value={Number(s.value)}
                unit="minutes"
                {...ruleFor(s, { min: 1, max: 1440, integer: true })}
                disabled={locked}
                onCommit={(v) => set(s.key, v)}
              />
            )}
          </Row>
          <Row
            setting={by.get('engine.vram_reserve_gb')}
            label="VRAM kept free for other apps"
            help="Models never use this much of the GPU's memory."
            unit="GB"
            onReset={apply}
          >
            {(s, locked) => (
              <NumberInput value={Number(s.value)} unit="GB" {...ruleFor(s, { min: 0, max: reserveMax })} disabled={locked} onCommit={(v) => set(s.key, v)} />
            )}
          </Row>
          <Row setting={by.get('engine.fit_mode')} label="When a model does not fit in VRAM" stacked onReset={apply}>
            {(s, locked) => (
              <Choices
                value={String(s.value)}
                disabled={locked}
                onChange={(v) => v !== s.value && apply(s.key, v)}
                options={[
                  { value: 'fit-first', label: 'Keep going on this computer', help: 'Part of the model waits in system memory. Slower, but it stays local.' },
                  { value: 'quality-first', label: 'Stop the job', help: 'So it can run on a computer with a bigger GPU.' },
                ]}
              />
            )}
          </Row>
          <div className="setting">
            <div className="setting-text">
              <strong id="free-gpu-label">Free GPU memory now</strong>
              <div id="free-gpu-help" className="setting-help">
                <div>
                  {engine && !engine.running
                    ? 'The engine is not running, so nothing is loaded.'
                    : 'Unloads every model. Jobs keep running: the next stage loads what it needs again.'}
                </div>
              </div>
            </div>
            <div className="setting-control">
              <button className="btn" aria-describedby="free-gpu-help" onClick={() => unload.mutate()} disabled={unload.isPending || (engine ? !engine.running : false)}>
                Free GPU memory
              </button>
              <span className="setting-slot" />
            </div>
          </div>
          {pending && (
            <p className="setting-pending" role="status">
              <WaitingIcon />
              Waiting for the running job to end: then the engine restarts with these settings.
            </p>
          )}
        </div>
      </div>

      <Providers settings={by} onSave={set} onApply={apply} />

      <div className="section">
        <SectionHead>Blender</SectionHead>
        <div className="settings group">
          <Row
            setting={by.get('blender.path')}
            label="Blender's program"
            help={
              <>
                Used for exports other than GLB, Open in Blender, and the remesh and UV stages. Leave empty to find it automatically.{' '}
                {exportFormats.data &&
                  (blender ? (
                    <Pill tone="ok" dot title={blender.path}>
                      Blender {blender.version} found
                    </Pill>
                  ) : (
                    <Pill tone="warn" dot title={exportFormats.data.blender_problem}>
                      Not found
                    </Pill>
                  ))}
              </>
            }
            onReset={apply}
          >
            {(s, locked) => (
              <TextInput
                value={String(s.value ?? '')}
                placeholder={blender?.path ?? 'Found automatically'}
                disabled={locked || browsing}
                onCommit={(v) => set(s.key, v)}
                onClear={() => set(s.key, null)}
              >
                <button className="btn" onClick={() => void browseBlender()} disabled={locked || browsing}>
                  {browsing ? 'Checking…' : 'Browse…'}
                </button>
              </TextInput>
            )}
          </Row>
        </div>
        {!blender && exportFormats.data?.blender_problem && <p className="muted small section-foot">{exportFormats.data.blender_problem}</p>}
      </div>

      <div className="section">
        <SectionHead>Defaults</SectionHead>
        <div className="settings group">
          <Row
            setting={by.get('project.default_preset')}
            label="Default preset"
            help="Generate starts on it, and the forja command uses it when a job names none."
            onReset={apply}
          >
            {(s, locked, field) => (
              <select
                id={field.id}
                className="input"
                aria-labelledby={field.labelId}
                aria-describedby={field.helpId}
                value={String(s.value)}
                disabled={locked || !presets.data}
                onChange={(e) => apply(s.key, e.target.value)}
              >
                {presets.data ? (
                  presets.data.map((p) => (
                    <option key={p.id} value={p.id}>
                      {p.name}
                    </option>
                  ))
                ) : (
                  <option value={String(s.value)}>Loading…</option>
                )}
              </select>
            )}
          </Row>
          <Row
            setting={by.get('project.export_dir')}
            label="Export folder in a project"
            help="For jobs the forja command runs inside a project: each asset gets a subfolder here with its GLB and a list of the models and licenses used."
            onReset={apply}
          >
            {(s, locked) => <TextInput value={String(s.value)} disabled={locked} onCommit={(v) => set(s.key, v)} />}
          </Row>
        </div>
      </div>

      <div className="section">
        <button className="disclosure" aria-expanded={showAdvanced} aria-controls="settings-advanced" onClick={() => setAdvanced(!showAdvanced)}>
          Advanced
          <ChevronIcon size={14} />
        </button>
        {showAdvanced && (
          <div id="settings-advanced" className="settings group enter">
            <Row
              setting={stubs}
              label="Use stub models"
              help="Stand-ins that need no GPU, for tests and work on Forja itself. What they make is not a real asset; installed models still come first."
              onReset={apply}
            >
              {(s, locked) => <Switch checked={s.value === true} disabled={locked} onChange={(on) => apply(s.key, on)} />}
            </Row>
          </div>
        )}
      </div>

      <div className="section">
        <SectionHead>Files and support</SectionHead>
        <div className="row">
          <button className="btn" onClick={open(window.forja.openDataFolder)}>
            Open the data folder
          </button>
          <button className="btn" onClick={() => copy.mutate()} disabled={copy.isPending}>
            Copy diagnostics
          </button>
        </div>
        <p className="muted small section-foot">
          Settings not shown here live in config.toml, which you can edit by hand: the app keeps your comments. Keys stay in the system
          keychain, never in files.
        </p>
      </div>
    </div>
  )
}
