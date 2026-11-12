import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import type { Pick } from '../lib/api'
import { ViewsNotice } from './ViewsNotice'

const render = (plan: Pick[]): string => renderToStaticMarkup(createElement(ViewsNotice, { plan, onOpenModels: () => {} }))

const triposg: Pick = {
  stage: 'shape',
  use: 'shape',
  model: 'triposg',
  model_name: 'TripoSG',
  unread_views: ['left', 'back', 'right'],
  read_by: { model: 'hunyuan3d2-mv', model_name: 'Hunyuan3D-2mv', install_on: 'local', reads: ['left', 'back', 'right'] },
}

describe('ViewsNotice', () => {
  it('says the views will not change the shape, rising in, with a way to the Models screen', () => {
    const html = render([{ stage: 'bg', use: 'background-removal', model: 'rmbg' }, triposg])
    expect(html).toContain('TripoSG shapes from the front alone: the left, back and right views will not change the shape.')
    expect(html).toContain('Hunyuan3D-2mv reads them.')
    expect(html).toContain('class="views-notice enter"')
    expect(html).toContain('Open Models')
  })

  it('offers no install when the reading model cannot be installed, and none when there is nothing to say', () => {
    const html = render([{ ...triposg, read_by: undefined }])
    expect(html).toContain('will not change the shape.')
    expect(html).not.toContain('Open Models')
    expect(render([{ stage: 'shape', use: 'shape', model: 'mv' }])).toBe('')
  })
})
