// What closing the app would interrupt, and the question to ask before it does.

type JobLike = { status?: unknown }
type TaskLike = { status?: unknown; kind?: unknown }

export interface QuitQuestion {
  message: string
  detail: string
}

const count = (n: number, one: string, many: string): string => `${n} ${n === 1 ? one : many}`

/**
 * The question to ask before quitting stops the core, or null when nothing would be lost.
 * A running job is interrupted and has to be retried. Jobs that only wait (for their turn, or
 * for an answer about their views) are kept by the core and go on the next time it starts, so
 * alone they are no reason to ask.
 */
export function quitQuestion(jobs: unknown, installs: unknown): QuitQuestion | null {
  const list = Array.isArray(jobs) ? (jobs as JobLike[]) : []
  const running = list.filter((j) => j?.status === 'running').length
  const queued = list.filter((j) => j?.status === 'queued').length
  const tasks = (Array.isArray(installs) ? (installs as TaskLike[]) : []).filter((t) => t?.status === 'running' || t?.status === 'queued')
  const installing = tasks.filter((t) => t.kind === 'install').length
  const other = tasks.length - installing
  const parts: string[] = []
  if (running > 0) parts.push(count(running, 'job is', 'jobs are') + ' still generating')
  if (installing > 0) parts.push(count(installing, 'model is', 'models are') + ' still installing')
  if (other > 0) parts.push(count(other, 'setup task is', 'setup tasks are') + ' still running')
  if (parts.length === 0) return null
  const said = parts.length > 1 ? parts.slice(0, -1).join(', ') + ' and ' + parts.at(-1) : parts[0]
  const lost: string[] = ['Quitting Forja stops this work.']
  if (running > 0) lost.push(`${running === 1 ? 'The job is' : 'These jobs are'} marked as interrupted, and you can retry ${running === 1 ? 'it' : 'them'} from the Queue.`)
  if (queued > 0) lost.push(`${count(queued, 'queued job waits', 'queued jobs wait')} and ${queued === 1 ? 'runs' : 'run'} the next time Forja is open.`)
  if (installing + other > 0) lost.push('An install that was interrupted starts over next time.')
  return { message: said + '.', detail: lost.join(' ') }
}
