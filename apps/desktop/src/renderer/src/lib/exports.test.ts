import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { ExportFormat, ExportResult } from './api'
import { exportLabel, exportReport, exportsStore } from './exports'

const fbx = { id: 'fbx', name: 'FBX', ext: '.fbx' } as ExportFormat

describe('exportReport', () => {
  it('names the file of a single export, on any OS', () => {
    const res: ExportResult = { format: 'fbx', assets: [{ asset: 'a', files: ['C:\\out\\knight.fbx'] }] }
    expect(exportReport(res, fbx)).toEqual({ text: 'Saved knight.fbx', bad: false, file: 'C:\\out\\knight.fbx' })
  })
  it('counts the assets of an export into a folder', () => {
    const res: ExportResult = { format: 'fbx', assets: [{ asset: 'a', files: ['/out/a.fbx'] }, { asset: 'b', files: ['/out/b.fbx'] }] }
    expect(exportReport(res, fbx)).toEqual({ text: 'Exported 2 assets as FBX', bad: false, file: '/out/a.fbx' })
  })
  it('says how many failed and why, and still points at what was written', () => {
    const res: ExportResult = {
      format: 'fbx',
      assets: [{ asset: 'a', error: 'Blender could not read the file' }, { asset: 'b', files: ['/out/b.fbx'] }, { asset: 'c', error: 'other' }],
    }
    expect(exportReport(res, fbx)).toEqual({
      text: '2 of 3 assets could not be exported as FBX: Blender could not read the file',
      bad: true,
      file: '/out/b.fbx',
    })
  })
  it('does not say "1 of 1" when the only asset failed', () => {
    const res: ExportResult = { format: 'fbx', assets: [{ asset: 'a', error: 'disk full' }] }
    expect(exportReport(res, fbx)).toEqual({ text: 'The FBX export failed: disk full', bad: true, file: undefined })
  })
})

describe('exportLabel', () => {
  it('names one asset and counts several', () => {
    expect(exportLabel(['red-barrel'], fbx)).toBe('Exporting red-barrel as FBX')
    expect(exportLabel(['a', 'b', 'c'], fbx)).toBe('Exporting 3 assets as FBX')
  })
})

describe('exportsStore', () => {
  beforeEach(() => exportsStore.reset())

  it('shows one running export to every part of the app until it ends', () => {
    const changed = vi.fn()
    const off = exportsStore.subscribe(changed)
    const cancel = vi.fn()
    exportsStore.start({ label: 'Exporting 3 assets as FBX', cancel })
    expect(exportsStore.get().running?.label).toBe('Exporting 3 assets as FBX')
    exportsStore.finish()
    expect(exportsStore.get().running).toBeNull()
    expect(changed).toHaveBeenCalledTimes(2)
    off()
  })

  it('remembers where each asset went, and not the ones that failed', () => {
    const res: ExportResult = {
      format: 'fbx',
      assets: [{ asset: 'a', files: ['/out/a.fbx', '/out/a.png'] }, { asset: 'b', error: 'no' }],
    }
    exportsStore.finish(res, fbx, 1000)
    expect(exportsStore.get().last.get('a')).toEqual({ file: '/out/a.fbx', format: 'FBX', at: 1000 })
    expect(exportsStore.get().last.has('b')).toBe(false)
    // A later export of another asset keeps the first one's record.
    exportsStore.finish({ format: 'glb', assets: [{ asset: 'c', files: ['/out/c.glb'] }] }, undefined, 2000)
    expect(exportsStore.get().last.get('a')?.file).toBe('/out/a.fbx')
    expect(exportsStore.get().last.get('c')).toEqual({ file: '/out/c.glb', format: 'glb', at: 2000 })
  })
})
