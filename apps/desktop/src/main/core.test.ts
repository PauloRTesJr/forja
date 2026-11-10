// The supervisor against a fake core: an HTTP server on a real named pipe (Windows) or Unix
// socket, started by a fake launcher, that can be killed mid-stream like the real one.
import type { ChildProcess } from 'node:child_process'
import { EventEmitter } from 'node:events'
import http from 'node:http'
import type net from 'node:net'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { PassThrough } from 'node:stream'
import { afterEach, describe, expect, it } from 'vitest'
import { Core, CoreUnavailable, type CoreEvent, type CoreState, type Launcher } from './core'

let n = 0
function freshAddress(): string {
  const name = `forja-test-${process.pid}-${Date.now()}-${n++}`
  return process.platform === 'win32' ? `\\\\.\\pipe\\${name}` : join(tmpdir(), `${name}.sock`)
}

class FakeCore {
  private server = http.createServer((req, res) => {
    this.requests.push(`${req.method} ${req.url}`)
    if (req.url === '/v1/events/stream') {
      res.writeHead(200, { 'content-type': 'text/event-stream' })
      res.flushHeaders()
      this.streams.add(res)
      res.on('close', () => this.streams.delete(res))
      return
    }
    res.writeHead(req.url === '/v1/shutdown' ? 202 : 200, { 'content-type': 'application/json' })
    res.end(req.url === '/v1/shutdown' ? '' : '{"status":"ok"}')
    if (req.url === '/v1/shutdown') this.onShutdown()
  })
  private sockets = new Set<net.Socket>()
  private streams = new Set<http.ServerResponse>()
  requests: string[] = []
  onShutdown: () => void = () => {}

  constructor(readonly address: string) {
    this.server.on('connection', (s) => {
      this.sockets.add(s)
      s.on('close', () => this.sockets.delete(s))
    })
  }
  listen(): Promise<void> {
    return new Promise((ok) => this.server.listen(this.address, ok))
  }
  emit(ev: CoreEvent): void {
    for (const s of this.streams) s.write(`data: ${JSON.stringify(ev)}\n\n`)
  }
  /** Gone without a goodbye, as when the process is killed. */
  kill(): Promise<void> {
    for (const s of this.sockets) s.destroy()
    return new Promise((ok) => this.server.close(() => ok()))
  }
}

function fakeChild(): ChildProcess & { die(code: number): void } {
  const child = new EventEmitter() as ChildProcess & { die(code: number): void }
  Object.assign(child, { exitCode: null, signalCode: null, pid: undefined, stderr: new PassThrough(), kill: () => true })
  child.die = (code) => {
    Object.assign(child, { exitCode: code })
    child.emit('exit', code, null)
  }
  return child
}

/** A fake core as the launcher would start it: its child process ends when it is told to shut down. */
function startFake(address: string, delayMs = 0): { child: ReturnType<typeof fakeChild>; core: FakeCore } {
  const child = fakeChild()
  const core = new FakeCore(address)
  core.onShutdown = () => setTimeout(() => void core.kill().then(() => child.die(0)), 20)
  setTimeout(() => void core.listen(), delayMs)
  return { child, core }
}

async function until(what: () => boolean, ms = 4000): Promise<void> {
  const end = Date.now() + ms
  while (!what()) {
    if (Date.now() > end) throw new Error('timed out waiting')
    await new Promise((r) => setTimeout(r, 10))
  }
}

const open: Array<() => Promise<unknown>> = []
afterEach(async () => {
  for (const close of open.splice(0)) await close().catch(() => {})
})

function watch(core: Core): { states: CoreState[]; events: CoreEvent[]; resyncs: number[] } {
  const seen = { states: [] as CoreState[], events: [] as CoreEvent[], resyncs: [] as number[] }
  core.onState = (s) => seen.states.push(s)
  core.onEvent = (e) => seen.events.push(e)
  core.onResync = () => seen.resyncs.push(Date.now())
  open.push(() => core.stop())
  return seen
}

describe('Core', () => {
  it('attaches to a core that already runs and passes its events on', async () => {
    const address = freshAddress()
    const running = new FakeCore(address)
    await running.listen()
    open.push(() => running.kill())
    let spawned = 0
    const core = new Core({ launcher: { address: async () => address, spawn: () => (spawned++, fakeChild()) } })
    const seen = watch(core)
    core.start()
    await until(() => core.current().phase === 'connected')
    expect(core.current().generation).toBe(1)
    expect(core.owns()).toBe(false)
    running.emit({ seq: 1, type: 'job.created', data: { id: 'a' } })
    running.emit({ seq: 2, type: 'job.updated', data: { id: 'a' } })
    await until(() => seen.events.length === 2)
    expect(seen.events.map((e) => e.type)).toEqual(['job.created', 'job.updated'])
    expect(seen.resyncs).toHaveLength(0)
    expect(spawned).toBe(0)
  })

  it('notices a killed core, starts one itself, and connects again', async () => {
    const address = freshAddress()
    const first = new FakeCore(address)
    await first.listen()
    let second: FakeCore | undefined
    const launcher: Launcher = {
      address: async () => address,
      spawn: () => {
        const started = startFake(address)
        second = started.core
        return started.child
      },
    }
    const core = new Core({ launcher })
    const seen = watch(core)
    core.start()
    await until(() => core.current().phase === 'connected')
    await first.kill()
    await until(() => core.current().phase === 'connected' && core.current().generation === 2)
    open.push(() => second!.kill())
    expect(seen.states.map((s) => s.phase)).toEqual(['connected', 'reconnecting', 'connected'])
    expect(core.owns()).toBe(true)
    // The new stream delivers again, and its numbering starting over is not a gap.
    second!.emit({ seq: 1, type: 'job.created', data: {} })
    await until(() => seen.events.length === 1)
    expect(seen.resyncs).toHaveLength(0)
  })

  it('says so when events were lost on the way', async () => {
    const address = freshAddress()
    const running = new FakeCore(address)
    await running.listen()
    open.push(() => running.kill())
    const core = new Core({ launcher: { address: async () => address, spawn: fakeChild } })
    const seen = watch(core)
    core.start()
    await until(() => core.current().phase === 'connected')
    running.emit({ seq: 7, type: 'job.updated', data: {} })
    running.emit({ seq: 9, type: 'job.finished', data: {} })
    await until(() => seen.events.length === 2)
    expect(seen.resyncs).toHaveLength(1)
  })

  it('makes requests wait for a core that is still starting', async () => {
    const address = freshAddress()
    let started: FakeCore | undefined
    const launcher: Launcher = {
      address: async () => address,
      spawn: () => {
        const fake = startFake(address, 150)
        started = fake.core
        return fake.child
      },
    }
    const core = new Core({ launcher })
    watch(core)
    core.start()
    expect(core.current().phase).toBe('starting')
    await expect(core.json<{ status: string }>('GET', '/v1/health')).resolves.toEqual({ status: 'ok' })
    open.push(() => started!.kill())
  })

  it('gives up with the reason the core gave, refuses requests, and tries again when asked', async () => {
    const address = freshAddress()
    let healthy: FakeCore | undefined
    let fail = true
    const launcher: Launcher = {
      address: async () => address,
      spawn: () => {
        if (!fail) {
          const started = startFake(address)
          healthy = started.core
          return started.child
        }
        const child = fakeChild()
        setTimeout(() => {
          ;(child.stderr as PassThrough).write('Error: the config file has an error on line 3\n')
          setTimeout(() => child.die(1), 10)
        }, 10)
        return child
      },
    }
    const core = new Core({ launcher, tries: 2 })
    watch(core)
    core.start()
    await until(() => core.current().phase === 'failed')
    expect(core.current().message).toBe("Forja's core could not start: the config file has an error on line 3")
    await expect(core.request('GET', '/v1/jobs', {})).rejects.toBeInstanceOf(CoreUnavailable)
    fail = false
    core.retry()
    await until(() => core.current().phase === 'connected')
    open.push(() => healthy!.kill())
  })

  it('stops the core it started and waits until it is gone', async () => {
    const address = freshAddress()
    let started: FakeCore | undefined
    let child: ReturnType<typeof fakeChild> | undefined
    const launcher: Launcher = {
      address: async () => address,
      spawn: () => {
        const fake = startFake(address)
        started = fake.core
        child = fake.child
        return fake.child
      },
    }
    const core = new Core({ launcher })
    const seen = watch(core)
    core.start()
    await until(() => core.current().phase === 'connected')
    await core.stop()
    expect(started!.requests).toContain('POST /v1/shutdown')
    expect(child!.exitCode).toBe(0)
    // Stopping is not a lost connection: nothing is started again.
    await new Promise((r) => setTimeout(r, 300))
    expect(seen.states.at(-1)?.phase).toBe('connected')
    await expect(core.request('GET', '/v1/jobs', {})).rejects.toBeInstanceOf(CoreUnavailable)
  })
})
