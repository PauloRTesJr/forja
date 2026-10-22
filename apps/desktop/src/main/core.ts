// Starts or attaches to the Go core and proxies traffic to it over the local socket or pipe.
// The renderer never touches the socket: it uses forja:// URLs (see protocol.ts) and IPC events.
import { execFile, spawn, type ChildProcess } from 'node:child_process'
import { existsSync } from 'node:fs'
import http from 'node:http'
import net from 'node:net'
import { join, resolve } from 'node:path'
import { promisify } from 'node:util'
import { app } from 'electron'

const run = promisify(execFile)

export type CoreEvent = { seq: number; type: string; data: unknown }

export function coreBinary(): string {
  const exe = process.platform === 'win32' ? 'forja.exe' : 'forja'
  if (process.env.FORJA_CORE_BIN) return process.env.FORJA_CORE_BIN
  if (app.isPackaged) return join(process.resourcesPath, 'bin', exe)
  return resolve(app.getAppPath(), '..', '..', 'core', 'bin', exe)
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

export class Core {
  address = ''
  private child: ChildProcess | null = null
  private eventsReq: http.ClientRequest | null = null
  private stopped = false

  async start(): Promise<void> {
    const bin = coreBinary()
    if (!existsSync(bin)) throw new Error(`forja core not found at ${bin}; run "make core"`)
    this.address = (await run(bin, ['socket'])).stdout.trim()
    if (await reachable(this.address)) return // a daemon (or the CLI) already runs the core
    this.child = spawn(bin, ['daemon'], { stdio: ['ignore', 'ignore', 'pipe'], windowsHide: true })
    this.child.stderr?.on('data', (b: Buffer) => process.stderr.write(`[core] ${b}`))
    const deadline = Date.now() + 10_000
    while (Date.now() < deadline) {
      if (await reachable(this.address)) return
      if (this.child.exitCode !== null) throw new Error(`forja core exited with code ${this.child.exitCode}`)
      await new Promise((r) => setTimeout(r, 40))
    }
    throw new Error('forja core did not start within 10 s')
  }

  request(method: string, path: string, headers: Record<string, string>, body?: NodeJS.ReadableStream) {
    return new Promise<http.IncomingMessage>((done, fail) => {
      const req = http.request({ socketPath: this.address, method, path, headers: { ...headers, host: 'forja' } }, done)
      req.once('error', fail)
      if (body) body.pipe(req)
      else req.end()
    })
  }

  /** Streams core events, reconnecting with backoff, until stop(). */
  subscribe(onEvent: (ev: CoreEvent) => void, onStatus: (connected: boolean) => void): void {
    let backoff = 100
    const connect = (): void => {
      if (this.stopped) return
      const req = http.get({ socketPath: this.address, path: '/v1/events/stream', headers: { host: 'forja' } }, (res) => {
        backoff = 100
        onStatus(true)
        let buf = ''
        res.setEncoding('utf8')
        res.on('data', (chunk: string) => {
          buf += chunk
          let cut: number
          while ((cut = buf.indexOf('\n\n')) >= 0) {
            const frame = buf.slice(0, cut)
            buf = buf.slice(cut + 2)
            for (const line of frame.split('\n')) {
              if (line.startsWith('data: ')) onEvent(JSON.parse(line.slice(6)) as CoreEvent)
            }
          }
        })
        res.on('end', retry)
      })
      req.on('error', retry)
      this.eventsReq = req
    }
    const retry = (): void => {
      onStatus(false)
      if (this.stopped) return
      setTimeout(connect, backoff)
      backoff = Math.min(backoff * 2, 3000)
    }
    connect()
  }

  /** Stops the core only if this app started it; a daemon started elsewhere keeps running. */
  async stop(): Promise<void> {
    this.stopped = true
    this.eventsReq?.destroy()
    if (!this.child || this.child.exitCode !== null) return
    try {
      const res = await this.request('POST', '/v1/shutdown', {})
      res.resume()
    } catch {
      this.child.kill()
    }
  }
}
