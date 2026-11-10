// Keeps a Go core reachable for as long as the app runs, and proxies traffic to it over the
// local socket or pipe. It attaches to a core that already runs (a daemon, or the CLI), starts
// one when nobody answers, follows its event stream, and does both again whenever the stream
// ends. The renderer never touches the socket: it uses forja:// URLs (see protocol.ts) and IPC.
import { execFile, spawn, type ChildProcess } from 'node:child_process'
import { existsSync } from 'node:fs'
import http from 'node:http'
import net from 'node:net'
import { promisify } from 'node:util'
import { describeFailure, StartError, unavailable, type CoreState } from './state'
import { backoff, Frames, IDLE_MS, Sequence, type CoreEvent } from './stream'

export type { CoreEvent } from './stream'
export type { CoreState } from './state'

const run = promisify(execFile)
const sleep = (ms: number): Promise<void> => new Promise((r) => setTimeout(r, ms))

/** How a core is found and started; tests bring their own. */
export interface Launcher {
  /** The socket path or pipe name the core listens on. */
  address(): Promise<string>
  /** Starts a core that serves until it is told to stop; its stderr is read for the reason when it fails. */
  spawn(): ChildProcess
}

/** Starts the core as `<bin> daemon`. */
export function binaryLauncher(bin: () => string): Launcher {
  return {
    async address() {
      const path = bin()
      if (!existsSync(path)) throw new StartError('missing', { bin: path })
      try {
        return (await run(path, ['socket'], { timeout: 15_000, windowsHide: true })).stdout.trim()
      } catch (err) {
        throw new StartError('address', { bin: path, stderr: [err instanceof Error ? err.message : String(err)] })
      }
    },
    spawn() {
      const path = bin()
      if (!existsSync(path)) throw new StartError('missing', { bin: path })
      return spawn(path, ['daemon'], { stdio: ['ignore', 'ignore', 'pipe'], windowsHide: true })
    },
  }
}

export interface CoreOptions {
  launcher: Launcher
  /** Words for a packaged app's user instead of a developer's. */
  packaged?: boolean
  /** How long a started core may take to answer. */
  startMs?: number
  /** Failed tries in a row before giving up until retry(). */
  tries?: number
  log?: (line: string) => void
}

/** A request the core could not take: the page gets `problem` as a 503. */
export class CoreUnavailable extends Error {
  constructor(readonly problem: ReturnType<typeof unavailable>) {
    super(problem.detail)
  }
}

function reachable(address: string): Promise<boolean> {
  return new Promise((done) => {
    const sock = net.connect(address)
    sock.once('connect', () => {
      sock.destroy()
      done(true)
    })
    sock.once('error', () => done(false))
  })
}

function exited(child: ChildProcess): boolean {
  return child.exitCode !== null || child.signalCode !== null
}

/** Resolves true when `p` settles within `ms`. */
function within(p: Promise<unknown>, ms: number): Promise<boolean> {
  return new Promise((done) => {
    const timer = setTimeout(() => done(false), ms)
    void p.finally(() => {
      clearTimeout(timer)
      done(true)
    })
  })
}

type Waiter = { ok: () => void; fail: (err: Error) => void; timer: NodeJS.Timeout }

export class Core {
  address = ''
  onEvent: (ev: CoreEvent) => void = () => {}
  onState: (state: CoreState) => void = () => {}
  /** Events were lost: whatever was built from them must be read again. */
  onResync: () => void = () => {}

  private state: CoreState = { phase: 'starting', generation: 0 }
  private child: ChildProcess | null = null
  private stderr: string[] = []
  private stream: http.ClientRequest | null = null
  private waiters = new Set<Waiter>()
  private attempt = 0
  private looping = false
  private stopped = false
  private wake: (() => void) | null = null

  private readonly launcher: Launcher
  private readonly packaged: boolean
  private readonly startMs: number
  private readonly tries: number
  private readonly log: (line: string) => void

  constructor(opts: CoreOptions) {
    this.launcher = opts.launcher
    this.packaged = opts.packaged ?? false
    this.startMs = opts.startMs ?? 30_000
    this.tries = opts.tries ?? 4
    this.log = opts.log ?? (() => {})
  }

  current(): CoreState {
    return this.state
  }

  /** True when this app started the core that runs now, so quitting the app ends its work. */
  owns(): boolean {
    return this.child !== null && !exited(this.child)
  }

  /** Begins keeping a core reachable; returns at once. State changes arrive through onState. */
  start(): void {
    void this.loop()
  }

  /** Tries again after `failed`. */
  retry(): void {
    if (this.state.phase !== 'failed' || this.stopped) return
    this.attempt = 0
    this.set({ phase: 'starting', generation: this.state.generation })
    void this.loop()
  }

  private set(next: CoreState): void {
    this.state = next
    if (next.phase === 'connected') {
      for (const w of this.waiters) w.ok()
    } else if (next.phase === 'failed') {
      const err = new CoreUnavailable(unavailable('failed', next.message))
      for (const w of this.waiters) w.fail(err)
    }
    this.onState(next)
  }

  /** Resolves once a core is connected; rejects when none is within `ms`, or none will be. */
  private ready(ms: number): Promise<void> {
    const { phase, message } = this.state
    if (this.stopped) return Promise.reject(new CoreUnavailable(unavailable('failed', 'Forja is closing.')))
    if (phase === 'connected') return Promise.resolve()
    if (phase === 'failed') return Promise.reject(new CoreUnavailable(unavailable('failed', message)))
    return new Promise((ok, fail) => {
      const w: Waiter = {
        ok: () => {
          clearTimeout(w.timer)
          this.waiters.delete(w)
          ok()
        },
        fail: (err) => {
          clearTimeout(w.timer)
          this.waiters.delete(w)
          fail(err)
        },
        timer: setTimeout(() => w.fail(new CoreUnavailable(unavailable(this.state.phase, this.state.message))), ms),
      }
      this.waiters.add(w)
    })
  }

  private async loop(): Promise<void> {
    if (this.looping) return
    this.looping = true
    try {
      while (!this.stopped) {
        try {
          await this.ensure()
          await this.follow()
        } catch (err) {
          if (this.stopped) return
          this.attempt++
          this.log(`core: ${err instanceof Error ? err.message : String(err)} (try ${this.attempt})`)
          if (this.attempt >= this.tries || (err instanceof StartError && err.kind === 'missing')) {
            this.set({ phase: 'failed', generation: this.state.generation, ...describeFailure(err, this.packaged) })
            return
          }
        }
        if (this.stopped) return
        // A core that never connected is still starting; one that did is being reconnected.
        if (this.state.phase === 'connected') this.set({ phase: 'reconnecting', generation: this.state.generation })
        await new Promise<void>((r) => {
          this.wake = r
          setTimeout(r, backoff(this.attempt))
        })
        this.wake = null
      }
    } finally {
      this.looping = false
    }
  }

  /** Returns once a core answers on the address: one that runs already, or one started here. */
  private async ensure(): Promise<void> {
    if (!this.address) this.address = await this.launcher.address()
    if (await reachable(this.address)) return
    // One this app started that does not answer is on its way out (or stuck): it must be gone
    // before another can take the data folder.
    const old = this.child
    if (old && !exited(old)) {
      if (!(await within(new Promise((r) => old.once('exit', r)), 3000))) killTree(old)
    }
    const child = this.launcher.spawn()
    this.child = child
    this.stderr = []
    child.stderr?.setEncoding('utf8')
    child.stderr?.on('data', (text: string) => {
      this.log(`[core] ${text.trimEnd()}`)
      this.stderr.push(...text.split('\n').filter((l) => l.trim() !== ''))
      if (this.stderr.length > 30) this.stderr = this.stderr.slice(-30)
    })
    child.once('error', (err) => this.stderr.push(`error: ${err.message}`))
    child.once('exit', () => {
      if (this.child === child) this.child = null
    })
    const deadline = Date.now() + this.startMs
    for (;;) {
      if (await reachable(this.address)) return
      if (this.stopped) throw new Error('stopping')
      if (exited(child) || this.child !== child) {
        // Another core may have taken the data folder first (a daemon started at the same moment).
        if (await reachable(this.address)) return
        throw new StartError('exited', { code: child.exitCode, stderr: this.stderr })
      }
      if (Date.now() > deadline) {
        killTree(child)
        throw new StartError('timeout', { seconds: Math.round(this.startMs / 1000), stderr: this.stderr })
      }
      await sleep(40)
    }
  }

  /**
   * Follows the event stream until it ends. Resolves when a stream that had opened is over
   * (the core stopped, was killed, or went silent); rejects when it never opened.
   */
  private follow(): Promise<void> {
    return new Promise((done, fail) => {
      const frames = new Frames()
      const seq = new Sequence()
      let opened = false
      let over = false
      let idle: NodeJS.Timeout | undefined
      const end = (err?: Error): void => {
        if (over) return
        over = true
        clearTimeout(idle)
        req.destroy()
        if (this.stream === req) this.stream = null
        if (opened) done()
        else fail(err ?? new Error('the event stream closed before it opened'))
      }
      // A cut pipe does not always say so: silence past the core's keepalives counts as the end.
      const alive = (ms: number): void => {
        clearTimeout(idle)
        idle = setTimeout(() => end(new Error('the core did not answer')), ms)
      }
      const req = http.get({ socketPath: this.address, path: '/v1/events/stream', headers: { host: 'forja' } }, (res) => {
        if (res.statusCode !== 200) {
          res.resume()
          end(new Error(`the event stream answered ${res.statusCode}`))
          return
        }
        opened = true
        this.attempt = 0
        alive(IDLE_MS)
        this.set({ phase: 'connected', generation: this.state.generation + 1 })
        res.setEncoding('utf8')
        res.on('data', (chunk: string) => {
          alive(IDLE_MS)
          const { events, bad } = frames.push(chunk)
          let lost = bad > 0
          for (const ev of events) {
            if (seq.lost(ev.seq)) lost = true
            this.onEvent(ev)
          }
          if (lost) this.onResync()
        })
        // 'close' follows a clean end, an abort and an error alike; 'end' alone misses a core
        // that was killed.
        res.on('error', () => end())
        res.on('close', () => end())
      })
      req.on('error', (err) => end(err))
      req.on('close', () => end())
      this.stream = req
      alive(10_000)
    })
  }

  private send(method: string, path: string, headers: Record<string, string>, body?: NodeJS.ReadableStream): Promise<http.IncomingMessage> {
    return new Promise<http.IncomingMessage>((done, fail) => {
      // Never without an address: an empty socketPath would make this a request to localhost:80.
      if (!this.address) {
        fail(new CoreUnavailable(unavailable(this.state.phase, this.state.message)))
        return
      }
      const req = http.request({ socketPath: this.address, method, path, headers: { ...headers, host: 'forja' } }, done)
      req.once('error', () => {
        this.suspect()
        // Not the socket's own words ("connect ENOENT \\.\pipe\..."): the page shows this text.
        const phase = this.state.phase === 'connected' ? 'reconnecting' : this.state.phase
        fail(new CoreUnavailable(unavailable(phase, this.state.message)))
      })
      if (body) body.pipe(req)
      else req.end()
    })
  }

  /** A request failed to reach the core: if nobody listens any more, the stream is dead too. */
  private suspect(): void {
    if (this.state.phase !== 'connected' || this.stopped) return
    void reachable(this.address).then((ok) => {
      if (!ok) this.stream?.destroy()
    })
  }

  /** Sends a request to the core, waiting for one that is still starting or coming back. */
  async request(method: string, path: string, headers: Record<string, string>, body?: NodeJS.ReadableStream, waitMs = 20_000): Promise<http.IncomingMessage> {
    await this.ready(waitMs)
    return this.send(method, path, headers, body)
  }

  /** A JSON answer from the core, for the main process's own questions. */
  async json<T>(method: string, path: string, waitMs = 5000): Promise<T> {
    const res = await this.request(method, path, {}, undefined, waitMs)
    let body = ''
    res.setEncoding('utf8')
    for await (const chunk of res) body += chunk
    if ((res.statusCode ?? 500) >= 400) throw new Error(`core ${method} ${path}: ${res.statusCode}`)
    return JSON.parse(body) as T
  }

  /**
   * Stops following, and stops the core if this app started it, waiting until its process (and
   * with it the engine) is gone. A core started elsewhere keeps running.
   */
  async stop(): Promise<void> {
    this.stopped = true
    this.stream?.destroy()
    this.wake?.()
    const err = new CoreUnavailable(unavailable('failed', 'Forja is closing.'))
    for (const w of this.waiters) w.fail(err)
    const child = this.child
    if (!child || exited(child)) return
    const gone = new Promise<void>((r) => child.once('exit', () => r()))
    try {
      const res = await this.send('POST', '/v1/shutdown', {})
      res.resume()
    } catch {
      killTree(child)
    }
    // The core ends its jobs and the engine before it exits; one that hangs is ended for it.
    if (!(await within(gone, 10_000))) {
      killTree(child)
      await within(gone, 3000)
    }
  }
}

/** Ends a process and what it started (the engine and its model workers). */
function killTree(child: ChildProcess): void {
  if (exited(child) || child.pid === undefined) return
  if (process.platform === 'win32') {
    execFile('taskkill', ['/pid', String(child.pid), '/T', '/F'], { windowsHide: true }, () => {})
  } else {
    child.kill('SIGTERM')
    setTimeout(() => {
      if (!exited(child)) child.kill('SIGKILL')
    }, 2000).unref()
  }
}
