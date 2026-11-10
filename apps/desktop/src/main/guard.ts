// What the main process accepts from the page, and where the window may go. The page is the
// app's own, but everything it sends is checked as if it were not.

/** True when `url` is the app's own page (`entry`): same scheme, host and file; the query and fragment may differ. */
export function isAppURL(url: string, entry: string): boolean {
  let a: URL
  let b: URL
  try {
    a = new URL(url)
    b = new URL(entry)
  } catch {
    return false
  }
  const path = (u: URL): string => (u.protocol === 'file:' ? decodeURIComponent(u.pathname).toLowerCase() : u.pathname)
  return a.protocol === b.protocol && a.host === b.host && path(a) === path(b)
}

// The core's ids are ULIDs: 26 characters of Crockford base32.
const ULID = /^[0-9A-HJKMNP-TV-Z]{26}$/

export function isId(value: unknown): value is string {
  return typeof value === 'string' && ULID.test(value)
}

/** The asset ids in a message from the page: anything that is not a list of ids is no ids. */
export function assetIds(value: unknown, max = 500): string[] {
  if (!Array.isArray(value)) return []
  return value.filter(isId).slice(0, max)
}

/** A short piece of text for a dialog (a file type's name, a suggested file name), without control characters. */
export function label(value: unknown, max = 120): string {
  // eslint-disable-next-line no-control-regex
  return (typeof value === 'string' ? value : '').replace(/[\u0000-\u001f]/g, ' ').slice(0, max)
}

/** A file name without the characters no file system takes, so a suggested name cannot carry a path. */
export function fileStem(value: unknown): string {
  return label(value).replace(/[\\/:*?"<>|]/g, '_').replace(/^\.+/, '') || 'untitled'
}

/** File extensions for a dialog's filter: letters and digits only, without the dot. */
export function extensions(value: unknown): string[] {
  if (!Array.isArray(value)) return []
  return value
    .map((x) => (typeof x === 'string' ? x.replace(/^\./, '').toLowerCase() : ''))
    .filter((x) => /^[a-z0-9]{1,10}$/.test(x))
    .slice(0, 20)
}

/** An absolute path the page may ask to be shown in the file manager; null for anything else. */
export function absolutePath(value: unknown, isAbsolute: (p: string) => boolean): string | null {
  if (typeof value !== 'string' || value.length === 0 || value.length > 4096 || value.includes('\0')) return null
  return isAbsolute(value) ? value : null
}
