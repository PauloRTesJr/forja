import { join } from 'node:path'
import { app, BrowserWindow, dialog, ipcMain, nativeTheme, shell } from 'electron'
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

ipcMain.handle('dialog:pickFolder', async () => {
  const res = await dialog.showOpenDialog(win!, { properties: ['openDirectory', 'createDirectory'] })
  return res.canceled ? null : res.filePaths[0]
})
ipcMain.handle('dialog:pickImages', async () => {
  const res = await dialog.showOpenDialog(win!, {
    properties: ['openFile', 'multiSelections'],
    filters: [{ name: 'Images', extensions: ['png', 'jpg', 'jpeg', 'webp'] }],
  })
  return res.canceled ? [] : res.filePaths
})
ipcMain.handle('shell:showItem', (_e, path: string) => shell.showItemInFolder(path))

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
