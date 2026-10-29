import { beforeEach, describe, expect, it, vi } from 'vitest'

const image = (empty: boolean, from: string) => ({ from, isEmpty: () => empty, resize: vi.fn(() => image(false, `${from}@96`)) })
const getFileIcon = vi.fn()
const createFromBuffer = vi.fn((b: Buffer) => image(b.length === 0, 'thumbnail'))
const createFromPath = vi.fn((p: string) => image(false, p))

vi.mock('electron', () => ({ app: { getFileIcon }, nativeImage: { createFromBuffer, createFromPath } }))
vi.mock('../../build/icon.png?asset', () => ({ default: 'icon.png' }))

describe('dragIcon', () => {
  beforeEach(() => {
    vi.resetModules()
    vi.clearAllMocks()
  })

  it('shows the asset’s thumbnail when it has one', async () => {
    const { dragIcon } = await import('./drag')
    expect(dragIcon(new Uint8Array([1, 2, 3]).buffer)).toMatchObject({ from: 'thumbnail' })
  })

  it('shows the app’s icon for an asset without a thumbnail, never the file’s own icon', async () => {
    const { dragIcon } = await import('./drag')
    expect(dragIcon()).toMatchObject({ from: 'icon.png@96' })
    expect(dragIcon(new ArrayBuffer(0))).toMatchObject({ from: 'icon.png@96' })
    expect(createFromPath).toHaveBeenCalledTimes(1)
    expect(getFileIcon).not.toHaveBeenCalled()
  })
})
