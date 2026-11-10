import react from '@vitejs/plugin-react'
import { defineConfig } from 'electron-vite'
import { resolve } from 'node:path'

export default defineConfig({
  main: {
    build: { externalizeDeps: true },
  },
  preload: {
    build: {
      externalizeDeps: true,
      rollupOptions: { output: { format: 'cjs', entryFileNames: '[name].cjs' } },
    },
  },
  renderer: {
    root: resolve('src/renderer'),
    plugins: [react()],
    build: {
      // Never inline an asset as a data: URI: the page's CSP (font-src 'self') refuses an inlined
      // font, and the files are read from disk anyway.
      assetsInlineLimit: 0,
      rollupOptions: { input: resolve('src/renderer/index.html') },
    },
    worker: { format: 'es' },
  },
})
