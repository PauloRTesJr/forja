import { describe, expect, it } from 'vitest'
import { gridMetrics, gridWindow, moveInGrid, scrollToShow } from './gridWindow'

describe('gridMetrics', () => {
  it('fits as many columns as the minimum card width allows and shares the rest', () => {
    // 4 x 168 + 3 x 16 = 720: four fit in 720..903, five from 904.
    expect(gridMetrics(720, 168, 16, 44)).toEqual({ columns: 4, cell: 168, row: 212, gap: 16 })
    expect(gridMetrics(903, 168, 16, 44).columns).toBe(4)
    expect(gridMetrics(904, 168, 16, 44).columns).toBe(5)
    expect(gridMetrics(800, 168, 16, 44).cell).toBe(188)
  })
  it('always has one column, even before it has been measured', () => {
    expect(gridMetrics(0, 168, 16, 44).columns).toBe(1)
    expect(gridMetrics(120, 168, 16, 44)).toMatchObject({ columns: 1, cell: 120 })
  })
})

describe('gridWindow', () => {
  const m = gridMetrics(720, 168, 16, 44) // 4 columns, rows of 212 + 16
  it('mounts the rows on screen and two around them', () => {
    const w = gridWindow(1000, m, 228 * 10, 600)
    // Rows 10..12 are on screen; 8..14 are mounted.
    expect(w).toEqual({ from: 32, to: 60, offset: 228 * 8, height: 250 * 228 - 16 })
  })
  it('starts at the first row while the grid is still below the fold', () => {
    expect(gridWindow(1000, m, -300, 600)).toMatchObject({ from: 0, offset: 0, to: 16 })
  })
  it('ends at the last item, and holds a short last row', () => {
    const w = gridWindow(10, m, 0, 2000)
    expect(w).toEqual({ from: 0, to: 10, offset: 0, height: 3 * 228 - 16 })
  })
  it('keeps the last rows mounted when scrolled past the end', () => {
    const w = gridWindow(40, m, 99_999, 600)
    expect(w.to).toBe(40)
    expect(w.from).toBeLessThan(40)
  })
  it('is empty for an empty list', () => {
    expect(gridWindow(0, m, 0, 600)).toEqual({ from: 0, to: 0, offset: 0, height: 0 })
  })
})

describe('scrollToShow', () => {
  const m = gridMetrics(720, 168, 16, 44)
  it('leaves the scroll alone when the card is in view', () => {
    expect(scrollToShow(5, m, 0, 600)).toBeUndefined()
  })
  it('scrolls up to a card above and down to one below', () => {
    expect(scrollToShow(0, m, 500, 600)).toBe(-8)
    // Row 5 spans 1140..1352: its bottom edge plus the margin lands on the viewport's end.
    expect(scrollToShow(20, m, 0, 600)).toBe(1352 + 8 - 600)
  })
})

describe('moveInGrid', () => {
  it('moves by one card or one row and stops at the edges', () => {
    expect(moveInGrid(5, 'ArrowRight', 10, 4)).toBe(6)
    expect(moveInGrid(9, 'ArrowRight', 10, 4)).toBe(9)
    expect(moveInGrid(0, 'ArrowLeft', 10, 4)).toBe(0)
    expect(moveInGrid(5, 'ArrowUp', 10, 4)).toBe(1)
    expect(moveInGrid(1, 'ArrowUp', 10, 4)).toBe(1)
    expect(moveInGrid(1, 'ArrowDown', 10, 4)).toBe(5)
    expect(moveInGrid(8, 'ArrowDown', 10, 4)).toBe(8)
  })
  it('goes to the last card from above a short last row', () => {
    // 10 cards in rows of 4: the last row holds two. Down from the fourth column of row two.
    expect(moveInGrid(7, 'ArrowDown', 10, 4)).toBe(9)
  })
  it('jumps to either end, and starts from the first card when none had the focus', () => {
    expect(moveInGrid(5, 'Home', 10, 4)).toBe(0)
    expect(moveInGrid(5, 'End', 10, 4)).toBe(9)
    expect(moveInGrid(-1, 'ArrowRight', 10, 4)).toBe(1)
    expect(moveInGrid(0, 'ArrowDown', 0, 4)).toBe(-1)
  })
})
