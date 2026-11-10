import { existsSync } from 'node:fs'
import { basename, isAbsolute, join, resolve } from 'node:path'
import { pathToFileURL } from 'node:url'
import { app, BrowserWindow, clipboard, dialog, ipcMain, Menu, shell, type NativeImage, type OpenDialogOptions } from 'electron'
import iconIco from '../../build/icon.ico?asset'
import iconPng from '../../build/icon.png?asset'
import { coreFileNames } from './binary'
import { binaryLauncher, Core } from './core'
import { dragIcon } from './drag'
import { absolutePath, assetIds, extensions, fileStem, isAppURL, isId, label } from './guard'
import { handleScheme, registerScheme } from './protocol'
import { quitQuestion } from './quit'

registerScheme()
app.commandLine.appendSwitch('enable-features', 'CanvasOopRasterization')

function coreBinary(): string {
  const exe = process.platform === 'win32' ? 'forja.exe' : 'forja'
  if (process.env.FORJA_CORE_BIN) return process.env.FORJA_CORE_BIN
  if (app.isPackaged) {
    const names = coreFileNames(process.platform, process.arch)
    const found = names.map((n) => join(process.resourcesPath, 'bin', n)).find((p) => existsSync(p))
    return found ?? join(process.resourcesPath, 'bin', exe)
  }
  return resolve(app.getAppPath(), '..', '..', 'core', 'bin', exe)
}

// The page the window shows, and the only place it may ever be.
const entryURL = process.env.ELECTRON_RENDERER_URL ?? pathToFileURL(join(import.meta.dirname, '../renderer/index.html')).href

const core = new Core({
  launcher: binaryLauncher(coreBinary),
  packaged: app.isPackaged,
  log: (line) => process.stderr.write(line + '\n'),
})
let win: BrowserWindow | null = null

/** Sends to the page, if there still is one: events keep coming while the window closes. */
function send(channel: string, payload?: unknown): void {
  if (win && !win.isDestroyed() && !win.webContents.isDestroyed()) win.webContents.send(channel, payload)
}

function createWindow(): BrowserWindow {
  const w = new BrowserWindow({
    width: 1360,
    height: 860,
    minWidth: 960,
    minHeight: 600,
    show: false, // shown on ready-to-show, so the first frame is never blank
    // The page's --bg (styles/tokens.css). The app has one dark theme, whatever the OS uses, so
    // the window never flashes light before the page paints.
    backgroundColor: '#0f1012',
    titleBarStyle: process.platform === 'darwin' ? 'hiddenInset' : 'default',
    trafficLightPosition: { x: 16, y: 18 },
    // macOS ignores this for the bundle's icon; Windows gets the .ico for its hand-drawn small sizes.
    icon: process.platform === 'win32' ? iconIco : iconPng,
    webPreferences: {
      preload: join(import.meta.dirname, '../preload/index.cjs'),
      contextIsolation: true,
      nodeIntegration: false,
      sandbox: true,
      v8CacheOptions: 'code',
      spellcheck: false,
    },
  })
  w.once('ready-to-show', () => w.show())

  // The app never navigates away from itself: not to a file dropped on the window, not to a
  // link. External links open in the browser after a confirm.
  w.webContents.on('will-navigate', (e, url) => {
    if (!isAppURL(url, entryURL)) e.preventDefault()
  })
  w.webContents.setWindowOpenHandler(({ url }) => {
    if (/^https:\/\//.test(url)) {
      void dialog
        .showMessageBox(w, { type: 'question', buttons: ['Open', 'Cancel'], message: `Open ${url} in your browser?` })
        .then(({ response }) => {
          if (response === 0) void shell.openExternal(url)
        })
    }
    return { action: 'deny' }
  })
  // The page asks for no camera, microphone, location or notification: nothing is granted.
  w.webContents.session.setPermissionRequestHandler((_wc, _permission, grant) => grant(false))

  // Closing the window quits the app, which may stop work in progress: ask first.
  w.on('close', (e) => {
    if (quit === 'stopping') return
    e.preventDefault()
    void requestQuit()
  })
  w.on('closed', () => {
    if (win === w) win = null
  })

  if (process.env.ELECTRON_RENDERER_URL) void w.loadURL(process.env.ELECTRON_RENDERER_URL)
  else void w.loadFile(join(import.meta.dirname, '../renderer/index.html'))
  return w
}

function openDialog(options: OpenDialogOptions): Promise<Electron.OpenDialogReturnValue> {
  return win && !win.isDestroyed() ? dialog.showOpenDialog(win, options) : dialog.showOpenDialog(options)
}

ipcMain.handle('dialog:pickImages', async () => {
  const res = await openDialog({
    properties: ['openFile', 'multiSelections'],
    filters: [{ name: 'Images', extensions: ['png', 'jpg', 'jpeg', 'webp'] }],
  })
  return res.canceled ? [] : res.filePaths
})
ipcMain.handle('dialog:pickFolder', async () => {
  const res = await openDialog({ properties: ['openDirectory', 'createDirectory'] })
  return res.canceled ? null : res.filePaths[0]
})
ipcMain.handle('dialog:pickFile', async (_e, name: unknown, exts: unknown) => {
  const res = await openDialog({
    properties: ['openFile'],
    filters: [{ name: label(name) || 'Files', extensions: extensions(exts) }],
  })
  return res.canceled ? null : res.filePaths[0]
})
ipcMain.handle('dialog:saveFile', async (_e, name: unknown, ext: unknown, kind: unknown) => {
  const extension = extensions([ext])[0]
  if (!extension) throw new Error('no file type to save as')
  const options: Electron.SaveDialogOptions = {
    defaultPath: `${fileStem(name)}.${extension}`,
    filters: [{ name: label(kind) || extension.toUpperCase(), extensions: [extension] }],
    properties: ['createDirectory', 'showOverwriteConfirmation'],
  }
  const res = await (win && !win.isDestroyed() ? dialog.showSaveDialog(win, options) : dialog.showSaveDialog(options))
  return res.canceled || !res.filePath ? null : res.filePath
})
ipcMain.handle('shell:showItem', (_e, path: unknown) => {
  const file = absolutePath(path, isAbsolute)
  if (file) shell.showItemInFolder(file)
})
// The renderer never names a path to open: these ask the core where its files are.
ipcMain.handle('settings:openConfig', async () => {
  const { path } = await core.json<{ path: string }>('POST', '/v1/settings/file')
  const err = await shell.openPath(path)
  if (err) throw new Error(err)
})
ipcMain.handle('settings:openData', async () => {
  const { data_dir } = await core.json<{ data_dir: string }>('POST', '/v1/settings/file')
  const err = await shell.openPath(data_dir)
  if (err) throw new Error(err)
})
ipcMain.handle('bench:openReport', async (_e, id: unknown) => {
  if (!isId(id)) throw new Error('this benchmark has no report')
  const { report } = await core.json<{ report?: string }>('GET', `/v1/bench/${id}`)
  if (!report) throw new Error('this benchmark has no report yet')
  // The core names the report after the result's id; the OS opens whatever it is given with
  // the program its type has, so anything else is refused.
  if (basename(report) !== `${id}.html`) throw new Error('this benchmark has no report')
  const err = await shell.openPath(report)
  if (err) throw new Error(err)
})
ipcMain.handle('clipboard:write', (_e, text: unknown) => clipboard.writeText(typeof text === 'string' ? text : ''))

// What the page needs to know about the core: asked for when it loads (also after a reload),
// pushed whenever it changes.
ipcMain.handle('core:state', () => core.current())
ipcMain.on('core:retry', () => core.retry())

// Assets' GLBs ready to drag into other programs (Blender, Unity, a folder). The OS drag has
// to start from the page's dragstart, at once, so the core names each file beforehand.
type DragFile = { file: string; icon: NativeImage }
const dragFiles = new Map<string, Promise<DragFile>>()
const dragIcons = new Map<string, NativeImage>()
// Icons are small, but a long session hovers many assets.
const MAX_DRAG_ICONS = 400
function readyDrag(id: string, png?: ArrayBuffer): Promise<DragFile> {
  if (png) {
    if (dragIcons.size >= MAX_DRAG_ICONS && !dragIcons.has(id)) dragIcons.delete(dragIcons.keys().next().value!)
    dragIcons.set(id, dragIcon(png))
  }
  const ready = (async (): Promise<DragFile> => {
    const { path } = await core.json<{ path: string }>('POST', `/v1/assets/${encodeURIComponent(id)}/drag`)
    return { file: path, icon: dragIcons.get(id) ?? dragIcon() }
  })()
  dragFiles.set(id, ready)
  ready.catch(() => dragFiles.delete(id))
  return ready
}
ipcMain.handle('drag:prepare', async (_e, id: unknown, png?: unknown) => {
  if (!isId(id)) throw new Error('not an asset')
  await readyDrag(id, png instanceof ArrayBuffer && png.byteLength <= 1 << 20 ? png : undefined)
})
ipcMain.on('drag:start', (e, raw: unknown) => {
  const ids = assetIds(raw)
  if (ids.length === 0) return
  void (async () => {
    // An asset not readied yet (one of a selection never hovered) is readied now: the button
    // is still down while the pointer travels.
    const got = await Promise.allSettled(ids.map((id) => dragFiles.get(id) ?? readyDrag(id)))
    const files = got.flatMap((r) => (r.status === 'fulfilled' ? [r.value] : []))
    if (files.length === 0 || e.sender.isDestroyed()) return
    e.sender.startDrag({ file: files[0]!.file, files: files.map((f) => f.file), icon: files[0]!.icon })
    // A drop into a folder on the same drive may have moved the files away: ready them again.
    for (const id of ids) readyDrag(id).catch(() => {})
  })()
})

// Quitting: 'asking' while the user decides, 'stopping' once the core is being stopped.
let quit: 'no' | 'asking' | 'stopping' = 'no'

/** Asks the core what quitting would interrupt; true when the user agrees, or nothing would be lost. */
async function confirmQuit(): Promise<boolean> {
  // A core started elsewhere (the CLI, a daemon) keeps running and keeps its jobs.
  if (!core.owns() || core.current().phase !== 'connected') return true
  let question: ReturnType<typeof quitQuestion>
  try {
    const [jobs, installs] = await Promise.all([core.json<unknown>('GET', '/v1/jobs?limit=200', 1500), core.json<unknown>('GET', '/v1/installs', 1500)])
    question = quitQuestion(jobs, installs)
  } catch {
    return true // a core that cannot say is not worth keeping the app open for
  }
  if (!question) return true
  const options: Electron.MessageBoxOptions = {
    type: 'warning',
    buttons: ['Quit and stop', 'Keep Forja open'],
    defaultId: 1,
    cancelId: 1,
    noLink: true,
    title: 'Quit Forja?',
    message: question.message,
    detail: question.detail,
  }
  const { response } = await (win && !win.isDestroyed() ? dialog.showMessageBox(win, options) : dialog.showMessageBox(options))
  return response === 0
}

async function requestQuit(): Promise<void> {
  if (quit !== 'no') return
  quit = 'asking'
  if (!(await confirmQuit())) {
    quit = 'no'
    // Asked after the window was already gone (a page that closed itself): staying open means
    // having a window again.
    if ((!win || win.isDestroyed()) && app.isReady()) win = createWindow()
    return
  }
  quit = 'stopping'
  // The window goes at once; the core gets the time it needs to end its jobs and the engine.
  if (win && !win.isDestroyed()) win.hide()
  await core.stop().catch(() => {})
  app.exit(0)
}

// One Forja per user: a second one would share this one's core and take it down on quitting.
if (!app.requestSingleInstanceLock()) {
  app.exit(0)
} else {
  app.on('second-instance', () => {
    if (!win || win.isDestroyed()) return
    if (win.isMinimized()) win.restore()
    if (!win.isVisible()) win.show()
    win.focus()
  })

  void app.whenReady().then(() => {
    // Electron's stock menu bar (File, Edit, View, Window, Help with links to electronjs.org)
    // is not Forja's: an installed app on Windows and Linux shows none. macOS keeps it for
    // the app menu and the standard editing keys; development keeps it for reload and DevTools.
    if (app.isPackaged && process.platform !== 'darwin') Menu.setApplicationMenu(null)
    handleScheme(core)
    core.onEvent = (ev) => send('core:event', ev)
    core.onState = (state) => send('core:state', state)
    core.onResync = () => send('core:resync')
    win = createWindow()
    core.start()
  })

  app.on('window-all-closed', () => {
    if (quit === 'no') void requestQuit()
  })

  // Only requestQuit ends the app (with app.exit, once the core has stopped).
  app.on('before-quit', (e) => {
    e.preventDefault()
    void requestQuit()
  })
}

// A bug in the main process must not put a stack trace in a dialog over the app.
process.on('uncaughtException', (err) => process.stderr.write(`main: ${err.stack ?? String(err)}\n`))
process.on('unhandledRejection', (err) => process.stderr.write(`main: unhandled rejection: ${err instanceof Error ? (err.stack ?? err.message) : String(err)}\n`))
