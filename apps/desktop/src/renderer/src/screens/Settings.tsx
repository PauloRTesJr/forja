import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Choices, NumberInput, Row, TextInput } from '../components/Controls'
import { PauseMoon } from '../components/icons'
import { ScreenHead, SectionHead } from '../components/ScreenHead'
import { Providers } from '../components/Providers'
import { api, saveSettings, type Preset, type Settings as SettingsView } from '../lib/api'
import { keys } from '../lib/events'
import { bytes } from '../lib/format'
import { useToast } from '../lib/toast'

interface Info {
  version: string
  data_dir: string
  engine: { running: boolean }
  system?: {
    os?: string
    devices?: { id: string; backend: string; name: string; vram_bytes: number; driver?: string | null }[]
    ram_total_bytes?: number
    disk_free_bytes?: number
  }
}

export function Settings() {
  const toast = useToast()
  const qc = useQueryClient()
  const info = useQuery({ queryKey: keys.info, queryFn: () => api<Info>('GET', '/v1/info') })
  const settings = useQuery({ queryKey: keys.settings, queryFn: () => api<SettingsView>('GET', '/v1/settings') })
  const presets = useQuery({ queryKey: keys.presets, queryFn: () => api<Preset[]>('GET', '/v1/presets') })
  const save = useMutation({
    mutationFn: saveSettings,
    onSuccess: (view) => {
      qc.setQueryData(keys.settings, view)
      if (view.engine === 'restarted') toast('Saved. The engine restarts with it the next time it runs.')
      else if (view.engine === 'next-start') toast('Saved. It applies once the running jobs finish and the engine restarts.')
    },
    onError: (e: Error) => toast(e.message, true),
  })
  const pause = useMutation({
    mutationFn: () => api<void>('POST', '/v1/engine/unload'),
    onSuccess: () => toast('Engine paused: all GPU memory freed'),
    onError: (e: Error) => toast(e.message, true),
  })
  const copy = useMutation({
    mutationFn: async () => {
      const res = await fetch('forja://core/v1/diagnostics')
      await window.forja.copyText(await res.text())
    },
    onSuccess: () => toast('Diagnostics copied: hardware, versions, settings and the engine log, without your home folder'),
    onError: (e: Error) => toast(e.message, true),
  })
  const open = (fn: () => Promise<void>) => () => void fn().catch((e: Error) => toast(e.message, true))

  const by = new Map(settings.data?.settings.map((s) => [s.key, s]))
  const set = (key: string, value: unknown): void => save.mutate({ [key]: value })
  const gpu = info.data?.system?.devices?.find((d) => d.backend !== 'cpu')

  return (
    <div className="screen">
      <ScreenHead eyebrow={['Arcanum V', 'The Sun']} title="Settings" sub="Changes apply to new jobs right away and are saved in config.toml." />

      <div className="section">
        <SectionHead>This computer</SectionHead>
        {info.isPending && <div className="skeleton" style={{ height: 90 }} />}
        {info.data && (
          <div className="stats hardware">
            <div className="stat wide">
              <b>{gpu?.name ?? 'No GPU found'}</b>
              <span>{gpu ? `${bytes(gpu.vram_bytes)} VRAM${gpu.driver ? ` · driver ${gpu.driver}` : ''}` : 'Models that need a GPU run on another computer'}</span>
            </div>
            {info.data.system?.ram_total_bytes ? (
              <div className="stat">
                <b>{bytes(info.data.system.ram_total_bytes)}</b>
                <span>RAM</span>
              </div>
            ) : null}
            {info.data.system?.disk_free_bytes ? (
              <div className="stat">
                <b>{bytes(info.data.system.disk_free_bytes)}</b>
                <span>free for models</span>
              </div>
            ) : null}
            <div className="stat">
              <b>{info.data.engine.running ? 'Running' : 'Idle'}</b>
              <span>engine</span>
            </div>
            <div className="stat">
              <b>{info.data.version}</b>
              <span>Forja</span>
            </div>
          </div>
        )}
        {!info.data?.system && info.data && <p className="muted">The hardware shows here once the engine has run once.</p>}
      </div>

      <div className="section">
        <SectionHead>GPU and engine</SectionHead>
        <div className="settings">
          <Row
            setting={by.get('engine.idle_unload_minutes')}
            label="Free GPU memory when idle"
            help="Unloads models after this many minutes without work, so Unity and games get the GPU back."
            onReset={set}
          >
            {(s, locked) => (
              <NumberInput value={Number(s.value)} unit="minutes" min={1} step={1} disabled={locked} onCommit={(v) => set(s.key, v)} />
            )}
          </Row>
          <Row
            setting={by.get('engine.vram_reserve_gb')}
            label="Keep VRAM free for other apps"
            help="Models never use this much of the GPU's memory."
            onReset={set}
          >
            {(s, locked) => (
              <NumberInput value={Number(s.value)} unit="GB" min={0} step={0.5} disabled={locked} onCommit={(v) => set(s.key, v)} />
            )}
          </Row>
          <Row setting={by.get('engine.fit_mode')} label="When a model does not fit in VRAM" onReset={set}>
            {(s, locked) => (
              <Choices
                value={String(s.value)}
                disabled={locked}
                onChange={(v) => set(s.key, v)}
                options={[
                  { value: 'fit-first', label: 'Keep going on this computer', help: 'Part of the model waits in RAM. Slower, but it stays local.' },
                  { value: 'quality-first', label: 'Stop the job', help: 'So it can run on a computer with a bigger GPU.' },
                ]}
              />
            )}
          </Row>
          <div className="row">
            <button className="btn" onClick={() => pause.mutate()} disabled={pause.isPending}>
              <PauseMoon />
              Free GPU memory now
            </button>
          </div>
        </div>
      </div>

      <Providers settings={by} onSet={set} />

      <div className="section">
        <SectionHead>Projects</SectionHead>
        <div className="settings">
          <Row
            setting={by.get('project.default_preset')}
            label="Default preset"
            help="Used when a job does not name one: the command line and watch folders."
            onReset={set}
          >
            {(s, locked) => (
              <select className="input" value={String(s.value)} disabled={locked} onChange={(e) => set(s.key, e.target.value)}>
                {presets.data?.map((p) => (
                  <option key={p.id} value={p.id}>
                    {p.name}
                  </option>
                ))}
              </select>
            )}
          </Row>
          <Row
            setting={by.get('project.export_dir')}
            label="Export folder inside a Unity project"
            help="Each asset gets a subfolder here with its GLB and a sidecar listing the models and licenses used."
            onReset={set}
          >
            {(s, locked) => <TextInput value={String(s.value)} disabled={locked} onCommit={(v) => set(s.key, v)} />}
          </Row>
        </div>
      </div>

      <div className="section">
        <SectionHead>Developer</SectionHead>
        <div className="settings">
          <Row
            setting={by.get('models.stubs')}
            label="Use stub models"
            help="Deterministic stand-ins for tests and UI work without a GPU. Their output is not a real asset; real models still win when installed."
            onReset={set}
          >
            {(s, locked) => (
              <button className="switch" role="switch" aria-checked={s.value === true} disabled={locked} onClick={() => set(s.key, s.value !== true)}>
                <span className="switch-track" aria-hidden />
                {s.value === true ? 'On' : 'Off'}
              </button>
            )}
          </Row>
        </div>
      </div>

      <div className="section">
        <SectionHead>Files and support</SectionHead>
        <div className="row">
          <button className="btn" onClick={open(window.forja.openConfig)}>
            Open config.toml
          </button>
          <button className="btn" onClick={open(window.forja.openDataFolder)}>
            Open the data folder
          </button>
          <button className="btn" onClick={() => copy.mutate()} disabled={copy.isPending}>
            Copy diagnostics
          </button>
        </div>
        <p className="muted small">
          Everything else lives in config.toml, which you can edit by hand: the app keeps your comments. Keys stay in the
          system keychain, never in files.
        </p>
      </div>
    </div>
  )
}

