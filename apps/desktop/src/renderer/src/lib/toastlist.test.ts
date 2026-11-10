import { describe, expect, it } from 'vitest'
import { addToast, lifetime, MAX_TOASTS, removeToast, type ToastItem } from './toastlist'

const toast = (id: number, text: string, bad?: boolean): ToastItem => ({ id, text, bad })

describe('addToast', () => {
  it('shows the same words once', () => {
    const first = addToast([], toast(1, "Forja's core is not answering.", true))
    const again = addToast(first.list, toast(2, "Forja's core is not answering.", true))
    expect(again.added).toBe(false)
    expect(again.list).toBe(first.list)
  })

  it('keeps an error and a notice with the same words apart, and different actions too', () => {
    const list = addToast([], toast(1, 'Exported chest')).list
    expect(addToast(list, toast(2, 'Exported chest', true)).added).toBe(true)
    expect(addToast(list, { id: 3, text: 'Exported chest', action: { label: 'Show in folder', run: () => {} } }).added).toBe(true)
  })

  it('makes room by dropping the oldest notice first, so errors stay', () => {
    let list: ToastItem[] = [toast(1, 'failed a', true), toast(2, 'notice b'), toast(3, 'failed c', true), toast(4, 'notice d')]
    list = addToast(list, toast(5, 'failed e', true)).list
    expect(list.map((t) => t.id)).toEqual([1, 3, 4, 5])
    expect(list).toHaveLength(MAX_TOASTS)
    list = addToast(list, toast(6, 'failed f', true)).list
    list = addToast(list, toast(7, 'failed g', true)).list
    expect(list.map((t) => t.id)).toEqual([3, 5, 6, 7])
  })
})

describe('removeToast', () => {
  it('takes back the toast that says the same, and nothing else', () => {
    const list = [toast(1, 'knight added to the queue'), toast(2, 'Exported chest')]
    expect(removeToast(list, toast(9, 'knight added to the queue')).map((t) => t.id)).toEqual([2])
    expect(removeToast(list, toast(9, 'knight added to the queue', true))).toBe(list)
  })
})

describe('lifetime', () => {
  it('keeps an error until it is dismissed', () => {
    expect(lifetime({ text: 'The export failed', bad: true })).toBeNull()
  })

  it('gives a short notice a moment and a long one time to be read', () => {
    expect(lifetime({ text: 'Copied' })).toBe(4200)
    expect(lifetime({ text: 'x'.repeat(100) })).toBe(8500)
    expect(lifetime({ text: 'x'.repeat(1000) })).toBe(12_000)
  })

  it('leaves time to reach a button', () => {
    expect(lifetime({ text: 'Exported', action: { label: 'Show in folder', run: () => {} } })).toBe(8000)
  })
})
