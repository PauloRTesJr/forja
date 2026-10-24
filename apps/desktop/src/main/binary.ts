// make core-all names the core's binaries for Go's OS and architecture (forja-darwin-arm64), and
// a package may carry several: each macOS DMG holds both darwin binaries, since electron-builder
// cannot pick one by Go's architecture names. A package that renamed its binary has plain forja.
const GOOS: Partial<Record<NodeJS.Platform, string>> = { win32: 'windows' }
const GOARCH: Partial<Record<NodeJS.Architecture, string>> = { x64: 'amd64', ia32: '386' }

/** The core binary's file names inside a package, the one for this OS and architecture first. */
export function coreFileNames(platform: NodeJS.Platform, arch: NodeJS.Architecture): string[] {
  const ext = platform === 'win32' ? '.exe' : ''
  return [`forja-${GOOS[platform] ?? platform}-${GOARCH[arch] ?? arch}${ext}`, `forja${ext}`]
}
