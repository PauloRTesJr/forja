import { beforeEach, describe, expect, it, vi } from 'vitest'
import { api, ApiError, uploadImage } from './api'
import { emptyDraft, type Staged } from './draft'
import { droppedFrom, duplicatesNote, fileName, pastedName, skippedNote, sortDropped, stagePaths, stagePasted, submitStaged } from './intake'

vi.mock('./api', async (original) => ({ ...(await original<typeof import('./api')>()), api: vi.fn(), uploadImage: vi.fn() }))
const apiMock = vi.mocked(api)
const uploadMock = vi.mocked(uploadImage)
beforeEach(() => {
  apiMock.mockReset()
  uploadMock.mockReset()
})

describe('sortDropped', () => {
  it('keeps images, opens folders and names what it leaves out', () => {
    const got = sortDropped([
      { path: 'C:\\art\\knight.png' },
      { path: '/home/me/art/mage.JPEG' },
      { path: 'C:\\art\\sheet.webp' },
      { path: 'C:\\art\\turnarounds', directory: true },
      { path: 'C:\\art\\notes.txt' },
      { path: 'C:\\art\\model.glb' },
      { path: '' }, // a dragged link or text has no path
    ])
    expect(got).toEqual({
      images: ['C:\\art\\knight.png', '/home/me/art/mage.JPEG', 'C:\\art\\sheet.webp'],
      folders: ['C:\\art\\turnarounds'],
      skipped: ['notes.txt', 'model.glb'],
    })
  })

  it('takes a folder as a folder even when its name ends like an image', () => {
    expect(sortDropped([{ path: 'C:\\art\\old.png', directory: true }])).toEqual({ images: [], folders: ['C:\\art\\old.png'], skipped: [] })
  })
})

describe('skippedNote', () => {
  it('names one skipped file and counts several', () => {
    expect(skippedNote([])).toBeUndefined()
    expect(skippedNote(['notes.txt'])).toBe('notes.txt was skipped: it is not a PNG, JPEG or WebP image.')
    expect(skippedNote(['a.txt', 'b.pdf'])).toBe('2 files were skipped: they are not PNG, JPEG or WebP images.')
  })
})

describe('duplicatesNote', () => {
  it('counts the images that were already staged', () => {
    expect(duplicatesNote(0)).toBeUndefined()
    expect(duplicatesNote(1)).toBe('1 image was already here.')
    expect(duplicatesNote(3)).toBe('3 images were already here.')
  })
})

describe('pastedName', () => {
  it('names a pasted picture by the time, so two pasted pictures stay apart', () => {
    expect(pastedName('image/png', new Date(2026, 0, 5, 9, 4, 7))).toBe('pasted-090407.png')
    expect(pastedName('image/jpeg', new Date(2026, 0, 5, 14, 30, 22))).toBe('pasted-143022.jpg')
    expect(pastedName('image/webp', new Date(2026, 0, 5, 0, 0, 0))).toBe('pasted-000000.webp')
  })
})

describe('fileName', () => {
  it('is the last part of a path on any system', () => {
    expect(fileName('C:\\art\\knight.png')).toBe('knight.png')
    expect(fileName('/home/me/art/mage.png')).toBe('mage.png')
    expect(fileName('knight.png')).toBe('knight.png')
  })
})

describe('droppedFrom', () => {
  const file = (name: string): File => new File(['x'], name)
  const item = (kind: string, directory?: boolean) => ({ kind, webkitGetAsEntry: () => (directory === undefined ? null : { isDirectory: directory }) })

  it('pairs each file with what its item says it is', () => {
    const data = {
      files: [file('knight.png'), file('turnarounds'), file('notes.txt')],
      // A dragged link travels as a string item between the files.
      items: [item('file', false), item('string'), item('file', true), item('file', false)],
    } as unknown as Pick<DataTransfer, 'files' | 'items'>
    expect(droppedFrom(data, (f) => `C:\\art\\${f.name}`)).toEqual([
      { path: 'C:\\art\\knight.png', directory: false },
      { path: 'C:\\art\\turnarounds', directory: true },
      { path: 'C:\\art\\notes.txt', directory: false },
    ])
  })

  it('takes a file as a file where the entry cannot be read', () => {
    const data = { files: [file('a.png')], items: [item('file')] } as unknown as Pick<DataTransfer, 'files' | 'items'>
    expect(droppedFrom(data, () => '/art/a.png')).toEqual([{ path: '/art/a.png', directory: false }])
  })
})

describe('stagePaths', () => {
  it('stages each file under its own ref and keeps going when another is refused', async () => {
    apiMock.mockImplementation(async (_m, _p, body) => {
      const path = (body as { path: string }).path
      if (path.endsWith('corrupt.png')) throw new ApiError(422, 'corrupt.png is not an image (it looks like text/plain)')
      return { hash: 'blake3:' + fileName(path) }
    })
    const got = await stagePaths([String.raw`C:\art\knight.png`, String.raw`C:\art\corrupt.png`, String.raw`C:\art\mage.png`])
    expect(got.staged.map((s) => [s.name, s.ref, s.path])).toEqual([
      ['knight.png', 'blake3:knight.png', String.raw`C:\art\knight.png`],
      ['mage.png', 'blake3:mage.png', String.raw`C:\art\mage.png`],
    ])
    expect(new Set(got.staged.map((s) => s.id)).size).toBe(2)
    expect(got.problems).toEqual(['corrupt.png is not an image (it looks like text/plain)'])
  })

  it('puts the name in front of a refusal that does not carry it', async () => {
    apiMock.mockRejectedValue(new ApiError(500, 'boom'))
    expect((await stagePaths(['/art/a.png'])).problems).toEqual(['a.png: boom'])
  })

  it('stages a folder as one entry by its path, without asking the core to import it', async () => {
    const got = await stagePaths([], ['C:/art/turnarounds'])
    expect(apiMock).not.toHaveBeenCalled()
    expect(got.staged).toMatchObject([{ name: 'turnarounds', path: 'C:/art/turnarounds', ref: '', folder: true }])
    expect(got.problems).toEqual([])
  })
})

describe('stagePasted', () => {
  it('uploads the bytes and names the picture by the time', async () => {
    uploadMock.mockResolvedValue('blake3:p')
    const blob = new Blob(['x'], { type: 'image/png' })
    const got = await stagePasted(blob, new Date(2026, 0, 5, 9, 4, 7))
    expect(got).toMatchObject({ name: 'pasted-090407.png', ref: 'blake3:p', blob })
  })
})

describe('submitStaged', () => {
  const settings = { preset: 'mobile-prop', overrides: { 'rig.enabled': false } }
  const staged = (name: string): Staged => ({ id: name, name, path: '/art/' + name, ref: 'r-' + name })
  const job = (id: string) => ({ id, name: id }) as never

  it('sends the files with the settings and returns the jobs the core made', async () => {
    apiMock.mockResolvedValue([job('j1'), job('j2')])
    const got = await submitStaged({ ...emptyDraft, images: [staged('a.png'), staged('b.png')] }, settings)
    expect(apiMock).toHaveBeenCalledTimes(1)
    expect(apiMock).toHaveBeenCalledWith('POST', '/v1/jobs', { paths: ['/art/a.png', '/art/b.png'], priority: 'batch', sheet: 'ask', ...settings })
    expect(got).toEqual({ created: [job('j1'), job('j2')] })
  })

  it('sends a lone folder in paths, as a batch, with nothing else staged', async () => {
    apiMock.mockResolvedValue([job('j1'), job('j2')])
    const folder: Staged = { id: 'f', name: 'set', path: '/art/set', ref: '', folder: true }
    await submitStaged({ ...emptyDraft, images: [folder] }, settings)
    expect(apiMock).toHaveBeenCalledWith('POST', '/v1/jobs', { paths: ['/art/set'], priority: 'batch', sheet: 'ask', ...settings })
  })

  it('sends images and a folder together in one request', async () => {
    apiMock.mockResolvedValue([job('j1')])
    const folder: Staged = { id: 'f', name: 'set', path: '/art/set', ref: '', folder: true }
    await submitStaged({ ...emptyDraft, images: [staged('a.png'), folder] }, settings)
    expect(apiMock).toHaveBeenCalledWith('POST', '/v1/jobs', expect.objectContaining({ paths: ['/art/a.png', '/art/set'] }))
  })

  it('uploads a pasted picture again and sends it by its ref', async () => {
    uploadMock.mockResolvedValue('blake3:again')
    apiMock.mockResolvedValue([job('j1')])
    const pasted: Staged = { id: 'p', name: 'pasted-1.png', blob: new Blob(['x']), ref: 'old' }
    await submitStaged({ ...emptyDraft, images: [pasted] }, settings)
    expect(apiMock).toHaveBeenCalledWith('POST', '/v1/jobs', expect.objectContaining({ name: 'pasted-1.png', input: 'blake3:again' }))
  })

  it('returns the refusal and no jobs when the core refuses the batch, so the images stay staged', async () => {
    apiMock.mockRejectedValue(new ApiError(422, 'gone.png could not be read: it was moved or deleted'))
    const got = await submitStaged({ ...emptyDraft, images: [staged('a.png'), staged('gone.png')] }, settings)
    expect(got.created).toEqual([])
    expect(got.error?.message).toBe('gone.png could not be read: it was moved or deleted')
  })

  it('keeps the jobs a spending limit let through before it stopped', async () => {
    apiMock.mockRejectedValue(new ApiError(422, 'over the limit', 'http', { created: [job('j1')] }))
    const got = await submitStaged({ ...emptyDraft, images: [staged('a.png'), staged('b.png')] }, settings)
    expect(got.created).toEqual([job('j1')])
    expect(got.error?.message).toBe('over the limit')
  })

  it('wraps something that is not an Error', async () => {
    apiMock.mockRejectedValue('nope')
    expect((await submitStaged({ ...emptyDraft, images: [staged('a.png')] }, settings)).error?.message).toBe('nope')
  })
})
