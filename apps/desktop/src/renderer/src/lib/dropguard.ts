// A file dropped outside a drop target must do nothing. Left alone, the browser answers a
// dropped file by opening it in place of the app.

type DragLike = { defaultPrevented: boolean; preventDefault(): void; dataTransfer: { dropEffect: string } | null }
type Target = {
  addEventListener(type: 'dragover' | 'drop', fn: (e: DragLike) => void): void
  removeEventListener(type: 'dragover' | 'drop', fn: (e: DragLike) => void): void
}

/**
 * Refuses every drag that no element took. Drop targets handle their own events first (and
 * call preventDefault), so by the time one reaches the window nobody wanted it: the cursor
 * says "not here" and the drop is swallowed.
 */
export function guardDrops(target: Target): () => void {
  const over = (e: DragLike): void => {
    if (e.defaultPrevented) return
    e.preventDefault()
    if (e.dataTransfer) e.dataTransfer.dropEffect = 'none'
  }
  const drop = (e: DragLike): void => e.preventDefault()
  target.addEventListener('dragover', over)
  target.addEventListener('drop', drop)
  return () => {
    target.removeEventListener('dragover', over)
    target.removeEventListener('drop', drop)
  }
}
