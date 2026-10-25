// What the Generate screen says about a job before it is queued: how long, how much, and why
// it would be refused. The core does the sums; this only words them.
import type { Estimate, Fallback, PriceRow, Stage } from './api'
import { listOf, PER_CALL, seconds, stageLabel, stageName } from './format'

/** An expected cost: a stage billed by the second can cost less than a cent. */
export const usd = (n: number): string => (n > 0 && n < 0.01 ? 'under $0.01' : `$${n.toFixed(2)}`)

export interface EstimateText {
  /** "About 1 min 12 s per image", or that nothing measured it yet. */
  time: string
  /** "$0.40 per image on fal.ai", when a stage is paid. */
  cost?: string
  /** What the numbers leave out, and the spending so far against the limits. */
  notes: string[]
  /** Why the job would be refused now. */
  problem?: string
}

export function describeEstimate(e: Estimate): EstimateText {
  const notes: string[] = []
  const unmeasured = e.unmeasured.map((id) => stageLabel(e.stages.find((s) => s.stage === id)?.use ?? id))
  const counted = e.stages.filter((s) => !s.skipped && !s.missing).length
  let time = `About ${seconds(e.seconds)} per image`
  if (e.seconds <= 0) time = 'No time measured yet: it shows after the first run'
  else if (unmeasured.length > 0 && unmeasured.length < counted)
    notes.push(`Not counting ${listOf(unmeasured)}, which ${unmeasured.length === 1 ? 'has' : 'have'} not run yet.`)
  let cost: string | undefined
  if (e.paid) {
    const hosts = [...new Set(e.stages.filter((s) => s.paid && !s.skipped).map((s) => s.provider_label || s.provider || ''))]
    cost = `${usd(e.cost_usd)} per image on ${listOf(hosts.filter(Boolean))}`
    if (e.unpriced.length > 0) notes.push(`${listOf(e.unpriced)} has no known price: give it one in Settings → Prices.`)
    const { per_day: day, per_month: month } = e.spend.limits
    if (day > 0) notes.push(`Spent today ${usd(e.spend.today)} of ${usd(day)}.`)
    else if (month > 0) notes.push(`Spent this month ${usd(e.spend.month)} of ${usd(month)}.`)
  }
  return { time, cost, notes, problem: e.problem }
}

/** Where a stage ran, and why it moved there when it did: "Studio PC, after This computer ran out of memory". */
export function whereItRan(s: Pick<Stage, 'provider' | 'provider_label' | 'fallbacks'>): string | undefined {
  const last: Fallback | undefined = s.fallbacks?.at(-1)
  const where = s.provider_label || s.provider
  if (last) return `${where}, after ${last.from_label || last.from} ${last.reason}`
  return s.provider && s.provider !== 'local' ? where : undefined
}

/** A job's stage moves, one sentence per move that several stages made together. */
export function describeMoves(stages: Stage[]): string[] {
  const moves = new Map<string, { names: string[]; f: Fallback }>()
  for (const s of stages) {
    for (const f of s.fallbacks ?? []) {
      const key = `${f.from}|${f.to}|${f.reason}`
      const move = moves.get(key) ?? { names: [], f }
      move.names.push(stageName(s))
      moves.set(key, move)
    }
  }
  return [...moves.values()].map(({ names, f }) => `${listOf(names)} moved to ${f.to_label || f.to}: ${f.from_label || f.from} ${f.reason}.`)
}

/** What a job's stages said about their outputs, one sentence each: "Quad remesh: QuadriFlow could not …". */
export function describeNotes(stages: Stage[]): string[] {
  return stages.flatMap((s) => s.outputs.flatMap((o) => (o.note ? [`${stageName(s)}: ${o.note}.`] : [])))
}

/** A price as a person reads it: "$0.34 per hour", "$0.40 per image". */
export function priceText(p: Pick<PriceRow, 'usd' | 'per'>): string {
  const per = p.per === PER_CALL ? 'image' : p.per.replace(/s$/, '')
  return `$${p.usd >= 0.1 ? p.usd.toFixed(2) : Number(p.usd.toPrecision(4))} per ${per}`
}
