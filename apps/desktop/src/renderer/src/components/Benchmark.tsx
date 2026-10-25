import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useState, type CSSProperties } from 'react'
import {
  api,
  cancelBench,
  compareBench,
  fetchBench,
  fetchBenchList,
  startBench,
  type BenchComparison,
  type BenchEntry,
  type BenchList,
  type BenchResult,
  type BenchSample,
  type Job,
} from '../lib/api'
import { baselineFor, budgetState, deltaName, deltaValue, entryOf, pct, phaseLabel, progress, sampleDone, shortCommit, withBench } from '../lib/bench'
import { keys } from '../lib/events'
import { bytes, count, relative, seconds } from '../lib/format'
import { useToast } from '../lib/toast'
import { Alert, Chevron } from './icons'
import { SectionHead } from './ScreenHead'
import { StageTrack } from './StageTrack'
import '../styles/bench.css'

/**
 * The benchmark on the Models screen: run a suite, follow it by push, read each run against the
 * suite's budgets, and compare it with an earlier run of the same suite on this device.
 */
export function Benchmark() {
  const list = useQuery({ queryKey: keys.bench, queryFn: fetchBenchList })
  const [open, setOpen] = useState<string>()
  const results = list.data?.results ?? []
  return (
    <div className="section bench">
      <SectionHead>Benchmark</SectionHead>
      <p className="bench-intro">
        Runs a suite of reference images through its presets and measures every stage: its time, model load, memory, GPU use
        and the mesh it made.
      </p>
      {list.isPending && <div className="skeleton" style={{ height: 132 }} />}
      {list.error && <div className="error">{(list.error as Error).message}</div>}
      {list.data &&
        (list.data.running ? <Running run={list.data.running} /> : <Start suite={results[0]?.suite.path} />)}
      {results.length > 0 && (
        <ul className="bench-runs">
          {results.map((e, i) => (
            <RunRow key={e.id} entry={e} i={i} results={results} open={open === e.id} onToggle={() => setOpen(open === e.id ? undefined : e.id)} />
          ))}
        </ul>
      )}
    </div>
  )
}

function Start({ suite: last }: { suite?: string }) {
  const toast = useToast()
  const qc = useQueryClient()
  const [picked, setPicked] = useState<string>()
  const [cold, setCold] = useState(false)
  const suite = picked ?? last
  const start = useMutation({
    mutationFn: () => startBench({ suite: suite!, cold }),
    onSuccess: (r) => qc.setQueryData<BenchList>(keys.bench, (list) => withBench(list, r)),
    onError: (e: Error) => toast(e.message, true),
  })
  const choose = async (): Promise<void> => {
    const path = await window.forja.pickFile('Benchmark suite', ['yaml', 'yml'])
    if (path) setPicked(path)
  }
  return (
    <div className="bench-start enter">
      <div className="bench-suite">
        <span className="bench-label">Suite</span>
        {suite ? (
          <span className="mono">{suite}</span>
        ) : (
          <span className="muted">Choose a suite file, such as bench/suites/default.yaml in a Forja checkout.</span>
        )}
      </div>
      <label className="accept">
        <input type="checkbox" checked={cold} onChange={(e) => setCold(e.target.checked)} />
        <span>
          Cold models <span className="muted">· every image loads its models, as after a pause</span>
        </span>
      </label>
      <div className="row">
        <button className="btn" onClick={() => void choose()}>
          Choose suite…
        </button>
        <button className="btn primary" onClick={() => start.mutate()} disabled={!suite || start.isPending}>
          Run benchmark
        </button>
      </div>
    </div>
  )
}

function Running({ run }: { run: BenchResult }) {
  const toast = useToast()
  // The Queue's list: job events keep it current, so the running sample's stages move by push.
  const jobs = useQuery({ queryKey: keys.jobs, queryFn: () => api<Job[]>('GET', '/v1/jobs?limit=100') })
  const cancel = useMutation({ mutationFn: () => cancelBench(run.id), onError: (e: Error) => toast(e.message, true) })
  const current = run.warm_up ?? run.jobs.find((s) => !sampleDone(s))?.job
  const live = current ? jobs.data?.find((j) => j.id === current) : undefined
  const warming = run.phase === 'warm-up'
  return (
    <div className="bench-live enter">
      <div className="bench-live-head">
        <div>
          <strong>{run.suite.name}</strong> <span className="muted">v{run.suite.version}</span>
          <p className="bench-phase">{phaseLabel(run)}</p>
        </div>
        <button className="btn ghost" onClick={() => cancel.mutate()} disabled={cancel.isPending}>
          Cancel
        </button>
      </div>
      <div className="bar" role="progressbar" aria-valuemin={0} aria-valuemax={run.total} aria-valuenow={run.jobs.filter(sampleDone).length}>
        <i className={warming ? 'indeterminate' : ''} style={{ '--p': progress(run) } as CSSProperties} />
      </div>
      {live && <StageTrack stages={live.stages} />}
      {run.jobs.length > 0 && (
        <ol className="bench-samples">
          {run.jobs.map((s, i) => (
            <SampleRow key={`${s.preset}/${s.image}/${s.repeat}/${i}`} sample={s} />
          ))}
        </ol>
      )}
    </div>
  )
}

function SampleRow({ sample: s }: { sample: BenchSample }) {
  return (
    <li className="enter">
      <span className="bench-sample-name">
        {s.image}
        {s.repeat > 1 && <span className="muted"> #{s.repeat}</span>}
      </span>
      <span className="muted">{s.preset}</span>
      <span className={`status ${s.status}`}>
        <i aria-hidden />
        {s.status}
      </span>
      <span className="mono">{s.status === 'succeeded' ? seconds(s.wall_seconds) : ''}</span>
    </li>
  )
}

function RunRow({ entry: e, i, results, open, onToggle }: { entry: BenchEntry; i: number; results: BenchEntry[]; open: boolean; onToggle: () => void }) {
  return (
    <li className={`bench-run enter ${open ? 'open' : ''}`} style={{ '--i': Math.min(i, 8) } as CSSProperties}>
      <button className="bench-run-head" aria-expanded={open} onClick={onToggle}>
        <span className="bench-run-title">
          <strong>
            {e.suite.name} <span className="muted">v{e.suite.version}</span>
          </strong>
          <span className="muted small">
            {relative(e.started_at)} · {e.device.gpu ?? e.device.id} · {shortCommit(e.forja.commit)}
            {e.label && ` · ${e.label}`}
          </span>
        </span>
        <span className="bench-run-presets">
          {e.presets.map((p) => (
            <span key={p.preset} className={`tag ${budgetState(p) ?? ''}`} title={p.budget_seconds ? `Budget ${seconds(p.budget_seconds)}` : undefined}>
              {p.preset} {seconds(p.seconds.median)}
            </span>
          ))}
        </span>
        <span className={`status ${e.status}`}>
          <i aria-hidden />
          {e.status}
        </span>
        <Chevron />
      </button>
      {open && <RunDetail id={e.id} results={results} />}
    </li>
  )
}

function RunDetail({ id, results }: { id: string; results: BenchEntry[] }) {
  const toast = useToast()
  const res = useQuery({ queryKey: keys.benchResult(id), queryFn: () => fetchBench(id) })
  const here = res.data ? entryOf(res.data) : undefined
  const others = here ? results.filter((e) => e.id !== id && e.suite.name === here.suite.name) : []
  const [picked, setPicked] = useState<string | null>()
  const against = picked === undefined ? (here ? baselineFor(results, here)?.id : undefined) : (picked ?? undefined)
  const cmp = useQuery({
    queryKey: keys.benchCompare(against ?? '', id),
    queryFn: () => compareBench(against!, id),
    enabled: !!against && !!res.data?.summary,
    staleTime: Infinity,
  })
  const report = useMutation({ mutationFn: () => window.forja.openBenchReport(id), onError: (e: Error) => toast(e.message, true) })
  if (res.isPending) return <div className="skeleton bench-detail" style={{ height: 180 }} />
  if (res.error) return <div className="error">{(res.error as Error).message}</div>
  const r = res.data!
  const s = r.summary
  return (
    <div className="bench-detail">
      {r.error && <div className="error">{r.error}</div>}
      {s && (
        <div className="bench-facts">
          {s.presets.map((p) => (
            <div key={p.preset} className={`bench-fact ${budgetState(p) ?? ''}`}>
              <span className="bench-label">{p.preset}</span>
              <strong>{seconds(p.seconds.median)}</strong>
              <span className="muted small">
                {p.budget_seconds ? `budget ${seconds(p.budget_seconds)} · ` : ''}
                {p.images} image{p.images === 1 ? '' : 's'}
                {p.failed > 0 && `, ${p.failed} failed`}
              </span>
            </div>
          ))}
          <div className={`bench-fact ${s.core_overhead_budget_ms && s.core_overhead.median * 1000 > s.core_overhead_budget_ms ? 'over' : ''}`}>
            <span className="bench-label">Core per job</span>
            <strong>{seconds(s.core_overhead.median)}</strong>
            <span className="muted small">{s.core_overhead_budget_ms ? `budget ${s.core_overhead_budget_ms} ms` : 'inference excluded'}</span>
          </div>
          {s.devices.map((d) => (
            <div key={d.device} className="bench-fact">
              <span className="bench-label">{d.device}</span>
              <strong>{Math.round(d.busy * 100)}% busy</strong>
              <span className="muted small">{d.gpu_util === null ? 'no GPU reading' : `GPU ran ${Math.round(d.gpu_util * 100)}% of the time`}</span>
            </div>
          ))}
        </div>
      )}
      {s && s.stages.length > 0 && (
        <div className="bench-scroll">
          <table className="table bench-table">
            <thead>
              <tr>
                <th>Stage</th>
                <th>Model</th>
                <th className="n">Median</th>
                <th className="n">Load</th>
                <th className="n">VRAM</th>
                <th className="n">RAM</th>
                <th className="n">GPU</th>
                <th className="n">Triangles</th>
              </tr>
            </thead>
            <tbody>
              {s.stages.map((st) => (
                <tr key={`${st.preset}/${st.stage}`}>
                  <td>
                    {st.stage} <span className="muted small">{st.preset}</span>
                  </td>
                  <td className="muted">{st.model ?? 'built-in'}</td>
                  <td className="n">{seconds(st.seconds.median)}</td>
                  <td className="n">{st.load_seconds.n ? seconds(st.load_seconds.median) : '—'}</td>
                  <td className="n">{st.peak_vram_bytes === null ? '—' : bytes(st.peak_vram_bytes)}</td>
                  <td className="n">{st.peak_ram_bytes === null ? '—' : bytes(st.peak_ram_bytes)}</td>
                  <td className="n">{st.gpu_util === null ? '—' : `${Math.round(st.gpu_util)}%`}</td>
                  <td className="n">{st.triangles === null ? '—' : count(st.triangles)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {r.skipped.length > 0 && (
        <p className="muted small">
          Skipped {r.skipped.length}: {r.skipped.map((k) => k.path.split(/[\\/]/).pop()).join(', ')}
        </p>
      )}
      <div className="row bench-actions">
        {others.length > 0 && s && (
          <label className="bench-against">
            <span className="muted">Compare with</span>
            <select className="input" value={against ?? ''} onChange={(e) => setPicked(e.target.value || null)}>
              <option value="">nothing</option>
              {others.map((o) => (
                <option key={o.id} value={o.id}>
                  {new Date(o.started_at).toLocaleString()} · {shortCommit(o.forja.commit)}
                </option>
              ))}
            </select>
          </label>
        )}
        {r.report && (
          <button className="btn ghost" onClick={() => report.mutate()} disabled={report.isPending}>
            Open report
          </button>
        )}
      </div>
      {cmp.isFetching && <div className="skeleton" style={{ height: 64 }} />}
      {cmp.error && <div className="error">{(cmp.error as Error).message}</div>}
      {cmp.data && !cmp.isFetching && <Comparison cmp={cmp.data} />}
    </div>
  )
}

/** Regressions first, then every other change, each against the baseline. */
function Comparison({ cmp }: { cmp: BenchComparison }) {
  const rows = [...cmp.deltas].sort((a, b) => Number(b.regression) - Number(a.regression))
  return (
    <div className="bench-compare enter">
      <p className={cmp.regressions ? 'bench-verdict bad' : 'bench-verdict'}>
        {cmp.regressions ? <Alert size={16} /> : null}
        {cmp.regressions
          ? `${cmp.regressions} regression${cmp.regressions === 1 ? '' : 's'} past ${cmp.thresholds.pct}%`
          : `Nothing slower or larger by more than ${cmp.thresholds.pct}%`}
        <span className="muted">
          {' '}
          against {relative(cmp.baseline.started_at)} ({shortCommit(cmp.baseline.forja.commit)})
        </span>
      </p>
      {cmp.warnings.map((w) => (
        <p key={w} className="muted small">
          {w}
        </p>
      ))}
      <div className="bench-scroll">
        <table className="table bench-table">
          <thead>
            <tr>
              <th>Measure</th>
              <th className="n">Baseline</th>
              <th className="n">This run</th>
              <th className="n">Change</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((d) => (
              <tr key={`${d.preset}/${d.stage}/${d.metric}`} className={d.regression ? 'regression' : ''}>
                <td>{deltaName(d)}</td>
                <td className="n muted">{deltaValue(d, d.baseline)}</td>
                <td className="n">{deltaValue(d, d.current)}</td>
                <td className="n">{pct(d.pct)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  )
}
