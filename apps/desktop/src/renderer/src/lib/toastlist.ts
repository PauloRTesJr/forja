// Which toasts show and for how long (lib/toast.tsx draws them).

export interface ToastItem {
  id: number
  text: string
  /** An error: it stays until it is dismissed, so it cannot be missed by looking away. */
  bad?: boolean
  action?: { label: string; run: () => void }
}

/** No more than this many at once: older notices make room, and then older errors. */
export const MAX_TOASTS = 4

/**
 * The list with `next` added. The same words twice say nothing more (ten requests failing at
 * once are one toast), so a duplicate changes nothing; `added` says which it was.
 */
const sameWords = (a: ToastItem, b: ToastItem): boolean => a.text === b.text && !!a.bad === !!b.bad && a.action?.label === b.action?.label

export function addToast(list: ToastItem[], next: ToastItem): { list: ToastItem[]; added: boolean } {
  if (list.some((t) => sameWords(t, next))) return { list, added: false }
  const all = [...list, next]
  while (all.length > MAX_TOASTS) {
    const notice = all.findIndex((t) => !t.bad && t !== next)
    all.splice(notice >= 0 ? notice : 0, 1)
  }
  return { list: all, added: true }
}

/** The list without the toast that says what `like` says; for taking back a notice that stopped being true. */
export function removeToast(list: ToastItem[], like: ToastItem): ToastItem[] {
  return list.some((t) => sameWords(t, like)) ? list.filter((t) => !sameWords(t, like)) : list
}

/** How long a toast shows, in ms; null for one that waits to be dismissed. */
export function lifetime(t: Pick<ToastItem, 'text' | 'bad' | 'action'>): number | null {
  if (t.bad) return null
  // Reading takes time: about 60 ms a character on top of a moment to notice it. One with a
  // button stays longer, so there is time to reach it.
  const reading = 2500 + t.text.length * 60
  return Math.min(Math.max(reading, t.action ? 8000 : 4200), 12_000)
}
