import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Fragment, useState } from 'react'
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
import {
  baselineChoices,
  baselineFor,
  budgetState,
  deltaName,
  deltaValue,
  entryOf,
  pct,
  phaseLabel,
  progress,
  sampleDone,
  shortCommit,
  verdictOf,
  withBench,
} from '../lib/bench'
import { keys } from '../lib/events'
import { bytes, count, relative, seconds } from '../lib/format'
import { useToast } from '../lib/toast'
import { ErrorBlock, Pill, Progress, StatusPill, Switch, type Tone } from './Controls'
import { AlertIcon, ChevronIcon } from './icons'
import { SectionHead } from './ScreenHead'
import { StageTrack } from './StageTrack'

/**
 * The benchmark on the Models screen: run a suite, follow it by push, read each run against the
 * suite's budgets, and compare it with an earlier completed run of the same suite on this device.
 */
export function Benchmark() {
  const list = useQuery({ queryKey: keys.bench, queryFn: fetchBenchList })
  const [open, setOpen] = useState<string>()
  const results = list.data?.results ?? []
  return (
    <div className="section bench">
      <SectionHead>Benchmark</SectionHead>
      <p className="section-note">Measures every stage of a suite of reference images on this computer: time, model load, memory, GPU use and the mesh.</p>
      {list.isPending && <div className="skeleton" style={{ height: 132 }} />}
      {list.error && <ErrorBlock>{(list.error as Error).message}</ErrorBlock>}
      {list.data && (list.data.running ? <Running run={list.data.running} /> : <Start suite={results[0]?.suite.path} />)}
      {results.length > 0 && (
        <table className="table bench-runs">
          <caption className="sr-only">Benchmark runs</caption>
          <thead>
            <tr>
              <th scope="col">Run</th>
              <th scope="col" className="bench-col-device">
                Device
              </th>
              <th scope="col">Per preset</th>
              <th scope="col">State</th>
            </tr>
          </thead>
          <tbody>
            {results.map((e) => (
              <RunRow key={e.id} entry={e} results={results} open={open === e.id} onToggle={() => setOpen(open === e.id ? undefined : e.id)} />
            ))}
          </tbody>
        </table>
      )}
    </div>
  )
}

const budgetTone = (state: string | undefined): Tone | undefined => (state === 'ok' ? 'ok' : state === 'over' ? 'bad' : undefined)
const fileName = (path: string): string => path.split(/[\\/]/).pop() ?? path

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
    <div className="group bench-start enter">
      <div className="bench-row">
        <div className="bench-row-text">
          <strong>Suite</strong>
          {suite ? (
            <span className="mono bench-file" title={suite}>
              {fileName(suite)}
            </span>
          ) : (
            <span className="muted">Choose a suite file to measure this computer.</span>
          )}
        </div>
        <button className="btn sm" onClick={() => void choose()}>
          Choose suite…
        </button>
      </div>
      <div className="bench-row">
        <div className="bench-row-text">
          <strong>Cold start</strong>
          <span className="muted">Each image loads its models again, as after a pause.</span>
        </div>
        <Switch checked={cold} onChange={setCold} label="Cold start" />
      </div>
      <div className="bench-row bench-foot">
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
  const stopping = cancel.isPending || cancel.isSuccess
  return (
    <div className="group bench-live enter">
      <div className="bench-row">
        <div className="bench-row-text">
          <strong>
            {run.suite.name} <span className="muted">v{run.suite.version}</span>
          </strong>
          <span className="muted" role="status">
            {stopping ? 'Cancelling…' : phaseLabel(run)}
          </span>
        </div>
        <button className="btn sm" onClick={() => cancel.mutate()} disabled={stopping}>
          {stopping ? 'Cancelling…' : 'Cancel'}
        </button>
      </div>
      <div className="bench-progress">
        <Progress value={warming ? undefined : progress(run)} label="Benchmark progress" />
        {live && <StageTrack stages={live.stages} />}
      </div>
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
      <StatusPill status={s.status} />
      <span className="mono">{s.status === 'succeeded' ? seconds(s.wall_seconds) : ''}</span>
    </li>
  )
}

function RunRow({ entry: e, results, open, onToggle }: { entry: BenchEntry; results: BenchEntry[]; open: boolean; onToggle: () => void }) {
  const measured = e.status === 'succeeded'
  return (
    <Fragment>
      <tr className={`bench-run ${open ? 'open' : ''}`}>
        <td>
          <button className="bench-run-head" aria-expanded={open} onClick={onToggle}>
            <ChevronIcon size={14} />
            <span className="bench-run-title">
              <strong>
                {e.suite.name} <span className="muted">v{e.suite.version}</span>
              </strong>
              <span className="muted small">
                {relative(e.started_at)} · {shortCommit(e.forja.commit)}
                {e.label && ` · ${e.label}`}
              </span>
            </span>
          </button>
        </td>
        <td className="bench-col-device muted">{e.device.gpu ?? e.device.id}</td>
        <td>
          {measured && (
            <span className="bench-run-presets">
              {e.presets.map((p) => (
                <Pill key={p.preset} tone={budgetTone(budgetState(p))} title={p.budget_seconds ? `Budget ${seconds(p.budget_seconds)}` : undefined}>
                  {p.preset} {seconds(p.seconds.median)}
                </Pill>
              ))}
            </span>
          )}
        </td>
        <td>
          <StatusPill status={e.status} />
        </td>
      </tr>
      {open && (
        <tr className="bench-detail-row">
          <td colSpan={4}>
            <RunDetail id={e.id} results={results} />
          </td>
        </tr>
      )}
    </Fragment>
  )
}

function RunDetail({ id, results }: { id: string; results: BenchEntry[] }) {
  const toast = useToast()
  const res = useQuery({ queryKey: keys.benchResult(id), queryFn: () => fetchBench(id) })
  const here = res.data ? entryOf(res.data) : undefined
  const choices = here ? baselineChoices(results, here) : []
  const [picked, setPicked] = useState<string | null>()
  const against = picked === undefined ? (here ? baselineFor(results, here)?.id : undefined) : (picked ?? undefined)
  const cmp = useQuery({
    queryKey: keys.benchCompare(against ?? '', id),
    queryFn: () => compareBench(against!, id),
    enabled: !!against && res.data?.status === 'succeeded' && !!res.data.summary,
    staleTime: Infinity,
  })
  const report = useMutation({ mutationFn: () => window.forja.openBenchReport(id), onError: (e: Error) => toast(e.message, true) })
  if (res.isPending) return <div className="skeleton bench-detail" style={{ height: 180 }} />
  if (res.error) return <ErrorBlock>{(res.error as Error).message}</ErrorBlock>
  const r = res.data!
  // A cancelled or failed run has no figures worth reading, and is nothing to compare.
  const s = r.status === 'succeeded' ? r.summary : undefined
  return (
    <div className="bench-detail">
      {r.error && <ErrorBlock>{r.error}</ErrorBlock>}
      {r.status !== 'succeeded' && !r.error && (
        <p className="muted">{r.status === 'cancelled' ? 'This run was cancelled, so it has no figures and cannot be compared with.' : 'This run did not finish, so it has no figures.'}</p>
      )}
      {s && (
        <dl className="bench-facts">
          {s.presets.map((p) => (
            <div key={p.preset} className={`bench-fact ${budgetState(p) ?? ''}`}>
              <dt>{p.preset}</dt>
              <dd>{seconds(p.seconds.median)}</dd>
              <dd className="muted small">
                {p.budget_seconds ? `budget ${seconds(p.budget_seconds)} · ` : ''}
                {p.images} image{p.images === 1 ? '' : 's'}
                {p.failed > 0 && `, ${p.failed} failed`}
              </dd>
            </div>
          ))}
          <div className={`bench-fact ${s.core_overhead_budget_ms && s.core_overhead.median * 1000 > s.core_overhead_budget_ms ? 'over' : ''}`}>
            <dt>Core per job</dt>
            <dd>{seconds(s.core_overhead.median)}</dd>
            <dd className="muted small">{s.core_overhead_budget_ms ? `budget ${s.core_overhead_budget_ms} ms` : 'inference excluded'}</dd>
          </div>
          {s.devices.map((d) => (
            <div key={d.device} className="bench-fact">
              <dt>{d.device}</dt>
              <dd>{Math.round(d.busy * 100)}% busy</dd>
              <dd className="muted small">{d.gpu_util === null ? 'no GPU reading' : `GPU ran ${Math.round(d.gpu_util * 100)}% of the time`}</dd>
            </div>
          ))}
        </dl>
      )}
      {s && s.stages.length > 0 && (
        <div className="bench-scroll">
          <table className="table compact flush bench-table">
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
                    {st.stage} <span className="dim small">{st.preset}</span>
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
          Skipped {r.skipped.length}: {r.skipped.map((k) => fileName(k.path)).join(', ')}
        </p>
      )}
      <div className="bench-actions">
        {s && (
          <label className="bench-against">
            <span className="muted">Compare with</span>
            <select
              className="input sm"
              value={against ?? ''}
              disabled={choices.length === 0}
              onChange={(e) => setPicked(e.target.value || null)}
            >
              <option value="">{choices.length === 0 ? 'No earlier completed run' : 'Nothing'}</option>
              {choices.map((o) => (
                <option key={o.id} value={o.id}>
                  {new Date(o.started_at).toLocaleString()} · {shortCommit(o.forja.commit)}
                </option>
              ))}
            </select>
          </label>
        )}
        {r.report && r.status === 'succeeded' && (
          <button className="btn sm" onClick={() => report.mutate()} disabled={report.isPending}>
            Open report
          </button>
        )}
      </div>
      {cmp.isFetching && <div className="skeleton" style={{ height: 64 }} />}
      {cmp.error && <ErrorBlock>{(cmp.error as Error).message}</ErrorBlock>}
      {cmp.data && !cmp.isFetching && <Comparison cmp={cmp.data} />}
    </div>
  )
}

/** Regressions first, then every other change, each against the baseline. */
function Comparison({ cmp }: { cmp: BenchComparison }) {
  const rows = [...cmp.deltas].sort((a, b) => Number(b.regression) - Number(a.regression))
  const verdict = verdictOf(cmp)
  const when = `against ${relative(cmp.baseline.started_at)} (${shortCommit(cmp.baseline.forja.commit)})`
  return (
    <div className="bench-compare enter">
      <p className={`bench-verdict ${verdict}`}>
        {verdict === 'regressed' && <AlertIcon />}
        {verdict === 'regressed'
          ? `${cmp.regressions} regression${cmp.regressions === 1 ? '' : 's'} past ${cmp.thresholds.pct}%`
          : verdict === 'clean'
            ? `Nothing slower or larger by more than ${cmp.thresholds.pct}%`
            : 'Nothing to compare'}
        <span className="muted"> {when}</span>
      </p>
      {cmp.warnings.map((w) => (
        <p key={w} className="muted small">
          {w}
        </p>
      ))}
      {rows.length > 0 && (
        <div className="bench-scroll">
          <table className="table compact flush bench-table">
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
      )}
    </div>
  )
}
