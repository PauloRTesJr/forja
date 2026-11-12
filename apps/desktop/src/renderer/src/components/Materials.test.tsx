import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { withEdit, type AssetMaterials } from '../lib/materials'
import { MaterialsView, PAINTED } from './Materials'

const data: AssetMaterials = {
  painted: false,
  materials: [
    { index: 0, texture: 0, color: '#8b5a2b', share: 0.5, roughness: 0.8, metallic: 0 },
    { index: 1, texture: 0, color: '#606268', share: 0.3, roughness: 0.35, metallic: 1 },
  ],
}

const render = (over: Partial<Parameters<typeof MaterialsView>[0]> = {}): string =>
  renderToStaticMarkup(
    <MaterialsView data={data} loading={false} edits={new Map()} replacing={false} onChange={() => {}} onReplace={() => {}} {...over} />,
  )

describe('MaterialsView', () => {
  it('shows a row per material with its swatch, share and two sliders', () => {
    const html = render()
    expect(html).toContain('Materials')
    expect(html.match(/class="mat enter"/g)).toHaveLength(2)
    expect(html).toContain('background:#8b5a2b')
    expect(html).toContain('50%')
    expect(html).toContain('30%')
    expect(html.match(/type="range"/g)).toHaveLength(4)
    expect(html).toContain('aria-label="Roughness of Material 1, #8b5a2b, 50%"')
    expect(html).toContain('aria-label="Metallic of Material 2, #606268, 30%"')
    expect(html).toContain('0.35')
  })

  it('shows the value being moved, not the one the core had', () => {
    const html = render({ edits: withEdit(new Map(), 1, 'roughness', 0.9) })
    expect(html).toContain('aria-valuetext="0.90"')
    expect(html).not.toContain('aria-valuetext="0.35"')
  })

  it('says a model painted the values instead of showing sliders, until they are replaced', () => {
    const painted = { ...data, painted: true }
    const html = render({ data: painted })
    expect(html).toContain(PAINTED.replace("'", '&#x27;'))
    expect(html).toContain('Replace them')
    expect(html).not.toContain('type="range"')
    expect(render({ data: painted, replacing: true })).toContain('type="range"')
  })

  it('says why an asset has no materials to set', () => {
    const html = render({ data: { materials: [], painted: false, reason: 'This asset has no base color texture.' } })
    expect(html).toContain('This asset has no base color texture.')
    expect(html).not.toContain('type="range"')
  })

  it('has a loading and an error state', () => {
    expect(render({ data: undefined, loading: true })).toContain('skeleton')
    expect(render({ data: undefined, loading: false, error: 'The core is not answering.' })).toContain('The core is not answering.')
  })
})
