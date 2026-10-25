// Builds the macOS DMGs, for Apple Silicon and Intel, from electron-builder.yml's mac section.
//
// With a Developer ID Application certificate (CSC_LINK with CSC_KEY_PASSWORD, or CSC_NAME for one
// already in the keychain), electron-builder signs under the hardened runtime and notarizes when
// APPLE_ID, APPLE_APP_SPECIFIC_PASSWORD and APPLE_TEAM_ID are set. Without one the app is signed
// ad hoc, which an Apple Silicon Mac needs to open it at all. An ad-hoc signature has no team, so
// the hardened runtime's library validation would refuse Electron's own frameworks: that build
// leaves the runtime off, and there is nothing to notarize.
import { chmodSync, existsSync } from 'node:fs'
import { join } from 'node:path'
import process from 'node:process'
import { URL, fileURLToPath } from 'node:url'
import builder from 'electron-builder'

const project = fileURLToPath(new URL('..', import.meta.url))
const cores = ['forja-darwin-arm64', 'forja-darwin-amd64'].map((name) => join(project, '..', '..', 'core', 'bin', name))
const missing = cores.filter((path) => !existsSync(path))
if (missing.length > 0) {
  process.stderr.write(`dist:mac: ${missing.join(' and ')} missing; run "make core-all" first\n`)
  process.exit(1)
}
// Binaries cross-compiled elsewhere lose their executable bit in CI artifacts.
for (const path of cores) chmodSync(path, 0o755)

const developerId = Boolean(process.env.CSC_LINK || process.env.CSC_NAME)
if (!developerId) process.stderr.write('dist:mac: no Developer ID certificate (CSC_LINK or CSC_NAME): signing ad hoc\n')

await builder.build({
  projectDir: project,
  targets: builder.Platform.MAC.createTarget(),
  // release.yml publishes every OS's files together; electron-builder would publish a tag build itself.
  publish: 'never',
  config: developerId ? undefined : { mac: { identity: '-', hardenedRuntime: false, notarize: false } },
})
