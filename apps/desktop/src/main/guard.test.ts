import { isAbsolute } from 'node:path'
import { describe, expect, it } from 'vitest'
import { absolutePath, assetIds, extensions, fileStem, isAppURL, isId, label } from './guard'

describe('isAppURL', () => {
  const built = 'file:///C:/Program%20Files/Forja/resources/app.asar/out/renderer/index.html'
  it('accepts the app page, with any query or fragment', () => {
    expect(isAppURL(built, built)).toBe(true)
    expect(isAppURL(built + '#x', built)).toBe(true)
    expect(isAppURL('file:///c:/Program Files/Forja/resources/app.asar/out/renderer/index.html', built)).toBe(true)
  })
  it('refuses any other file, such as one dropped on the window', () => {
    expect(isAppURL('file:///C:/Users/me/Desktop/chest.png', built)).toBe(false)
    expect(isAppURL('file:///C:/Users/me/Downloads/page.html', built)).toBe(false)
  })
  it('refuses a host that only starts like the dev server', () => {
    const dev = 'http://localhost:5173/'
    expect(isAppURL('http://localhost:5173/', dev)).toBe(true)
    expect(isAppURL('http://localhost:5173.evil.example/', dev)).toBe(false)
    expect(isAppURL('http://localhost:5173/other.html', dev)).toBe(false)
    expect(isAppURL('https://example.com/', dev)).toBe(false)
  })
  it('refuses what is not a URL', () => {
    expect(isAppURL('not a url', built)).toBe(false)
  })
})

describe('ids from the page', () => {
  const id = '01M70Z67STQP15K0H6J2RBA21B'
  it('knows a ULID', () => {
    expect(isId(id)).toBe(true)
    expect(isId('../../etc/passwd')).toBe(false)
    expect(isId(7)).toBe(false)
  })
  it('keeps only ids, and none from what is not a list', () => {
    expect(assetIds([id, 'x', null, id])).toEqual([id, id])
    expect(assetIds(id)).toEqual([])
    expect(assetIds(undefined)).toEqual([])
    expect(assetIds(Array(600).fill(id))).toHaveLength(500)
  })
})

describe('dialog arguments', () => {
  it('cleans labels, names and extensions', () => {
    expect(label('ComfyUI\nworkflow')).toBe('ComfyUI workflow')
    expect(label({})).toBe('')
    expect(fileStem('a/b:c')).toBe('a_b_c')
    expect(fileStem('..\\..\\evil')).toBe('_.._evil')
    expect(fileStem('..')).toBe('untitled')
    expect(fileStem('')).toBe('untitled')
    expect(extensions(['.GLB', 'fbx', '../x', 5])).toEqual(['glb', 'fbx'])
    expect(extensions('glb')).toEqual([])
  })
  it('shows only absolute paths', () => {
    const abs = process.platform === 'win32' ? 'C:\\out\\chest.glb' : '/out/chest.glb'
    expect(absolutePath(abs, isAbsolute)).toBe(abs)
    expect(absolutePath('chest.glb', isAbsolute)).toBeNull()
    expect(absolutePath(42, isAbsolute)).toBeNull()
    expect(absolutePath(abs + '\0', isAbsolute)).toBeNull()
  })
})
