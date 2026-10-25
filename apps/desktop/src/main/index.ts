import { join } from 'node:path'
import { app, BrowserWindow, clipboard, dialog, ipcMain, nativeImage, nativeTheme, shell, type NativeImage } from 'electron'
import iconIco from '../../build/icon.ico?asset'
import iconPng from '../../build/icon.png?asset'
import { Core } from './core'
import { handleScheme, registerScheme } from './protocol'

registerScheme()
app.commandLine.appendSwitch('enable-features', 'CanvasOopRasterization')

const core = new Core()
let win: BrowserWindow | null = null

function createWindow(): BrowserWindow {
  const w = new BrowserWindow({
    width: 1360,
    height: 860,
    minWidth: 960,
    minHeight: 600,
    show: false, // shown on ready-to-show, so the first frame is never blank
    backgroundColor: nativeTheme.shouldUseDarkColors ? '#0e0f12' : '#f6f6f4',
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

  // The app never navigates away from itself; external links open in the browser after a confirm.
  w.webContents.on('will-navigate', (e, url) => {
    if (!url.startsWith(process.env.ELECTRON_RENDERER_URL ?? 'file://')) e.preventDefault()
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

  if (process.env.ELECTRON_RENDERER_URL) void w.loadURL(process.env.ELECTRON_RENDERER_URL)
  else void w.loadFile(join(import.meta.dirname, '../renderer/index.html'))
  return w
}

ipcMain.handle('dialog:pickImages', async () => {
  const res = await dialog.showOpenDialog(win!, {
    properties: ['openFile', 'multiSelections'],
    filters: [{ name: 'Images', extensions: ['png', 'jpg', 'jpeg', 'webp'] }],
  })
  return res.canceled ? [] : res.filePaths
})
ipcMain.handle('dialog:pickFolder', async () => {
  const res = await dialog.showOpenDialog(win!, { properties: ['openDirectory', 'createDirectory'] })
  return res.canceled ? null : res.filePaths[0]
})
ipcMain.handle('dialog:pickFile', async (_e, label: string, extensions: string[]) => {
  const res = await dialog.showOpenDialog(win!, {
    properties: ['openFile'],
    filters: [{ name: String(label), extensions: (Array.isArray(extensions) ? extensions : []).map(String) }],
  })
  return res.canceled ? null : res.filePaths[0]
})
ipcMain.handle('dialog:saveFile', async (_e, name: string, ext: string, label: string) => {
  const extension = String(ext).replace(/^\./, '')
  const res = await dialog.showSaveDialog(win!, {
    defaultPath: `${String(name)}.${extension}`,
    filters: [{ name: String(label), extensions: [extension] }],
    properties: ['createDirectory', 'showOverwriteConfirmation'],
  })
  return res.canceled || !res.filePath ? null : res.filePath
})
ipcMain.handle('shell:showItem', (_e, path: string) => shell.showItemInFolder(path))

// The renderer never names a path to open: these ask the core where its files are.
async function coreJSON<T>(method: string, path: string): Promise<T> {
  const res = await core.request(method, path, {})
  let body = ''
  res.setEncoding('utf8')
  for await (const chunk of res) body += chunk
  if ((res.statusCode ?? 500) >= 400) throw new Error(`core ${method} ${path}: ${res.statusCode}`)
  return JSON.parse(body) as T
}
ipcMain.handle('settings:openConfig', async () => {
  const { path } = await coreJSON<{ path: string }>('POST', '/v1/settings/file')
  const err = await shell.openPath(path)
  if (err) throw new Error(err)
})
ipcMain.handle('settings:openData', async () => {
  const { data_dir } = await coreJSON<{ data_dir: string }>('POST', '/v1/settings/file')
  const err = await shell.openPath(data_dir)
  if (err) throw new Error(err)
})
ipcMain.handle('clipboard:write', (_e, text: string) => clipboard.writeText(String(text)))

// Assets' GLBs ready to drag into other programs (Blender, Unity, a folder). The OS drag has
// to start from the page's dragstart, at once, so the core names each file beforehand.
type DragFile = { file: string; icon: NativeImage }
const dragFiles = new Map<string, Promise<DragFile>>()
const dragIcons = new Map<string, NativeImage>()
function readyDrag(id: string, png?: ArrayBuffer): Promise<DragFile> {
  if (png) dragIcons.set(id, nativeImage.createFromBuffer(Buffer.from(png)))
  const ready = (async (): Promise<DragFile> => {
    const { path } = await coreJSON<{ path: string }>('POST', `/v1/assets/${encodeURIComponent(id)}/drag`)
    let icon = dragIcons.get(id) ?? nativeImage.createEmpty()
    if (icon.isEmpty()) icon = await app.getFileIcon(path, { size: 'large' })
    return { file: path, icon }
  })()
  dragFiles.set(id, ready)
  ready.catch(() => dragFiles.delete(id))
  return ready
}
ipcMain.handle('drag:prepare', async (_e, id: string, png?: ArrayBuffer) => {
  await readyDrag(String(id), png)
})
ipcMain.on('drag:start', async (e, ids: string[]) => {
  // An asset not readied yet (one of a selection never hovered) is readied now: the button
  // is still down while the pointer travels.
  const got = await Promise.allSettled(ids.map((id) => dragFiles.get(String(id)) ?? readyDrag(String(id))))
  const files = got.flatMap((r) => (r.status === 'fulfilled' ? [r.value] : []))
  if (files.length === 0) return
  e.sender.startDrag({ file: files[0]!.file, files: files.map((f) => f.file), icon: files[0]!.icon })
  // A drop into a folder on the same drive may have moved the files away: ready them again.
  for (const id of ids) readyDrag(String(id)).catch(() => {})
})

app.whenReady().then(async () => {
  handleScheme(core)
  win = createWindow()
  try {
    await core.start()
  } catch (err) {
    dialog.showErrorBox('Forja could not start its core', err instanceof Error ? err.message : String(err))
    app.quit()
    return
  }
  core.subscribe(
    (ev) => win?.webContents.send('core:event', ev),
    (connected) => win?.webContents.send('core:status', connected),
  )
})

app.on('window-all-closed', () => app.quit())

let quitting = false
app.on('before-quit', (e) => {
  if (quitting) return
  quitting = true
  e.preventDefault()
  void core.stop().finally(() => app.exit(0))
})
