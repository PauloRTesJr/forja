import { describe, expect, it } from 'vitest'
import { coreFileNames } from './binary'

describe('coreFileNames', () => {
  it('looks for the binary make core-all built for this Mac first', () => {
    expect(coreFileNames('darwin', 'arm64')).toEqual(['forja-darwin-arm64', 'forja'])
    expect(coreFileNames('darwin', 'x64')).toEqual(['forja-darwin-amd64', 'forja'])
  })

  it('uses Go’s names on Windows and Linux too', () => {
    expect(coreFileNames('win32', 'x64')).toEqual(['forja-windows-amd64.exe', 'forja.exe'])
    expect(coreFileNames('linux', 'arm64')).toEqual(['forja-linux-arm64', 'forja'])
  })
})
