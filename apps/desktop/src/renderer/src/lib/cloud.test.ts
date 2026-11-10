import { describe, expect, it } from 'vitest'
import type { ProviderKind, ProviderRow } from './api'
import { badNumber, bodyFor, carryValues, cloudKinds, missing, placeNote, plainProblem, priceNote, providerState } from './cloud'

const runpod: ProviderKind = {
  kind: 'serverless',
  vendor: 'runpod',
  label: 'RunPod serverless',
  description: '',
  pairing: false,
  paid: true,
  fields: [
    { key: 'endpoint_id', label: 'Endpoint ID', type: 'text' },
    { key: 'token', label: 'API key', type: 'secret' },
    { key: 'price_usd_per_second', label: 'GPU price', type: 'number', optional: true },
    { key: 'volume', label: 'Network volume ID', type: 'text', optional: true },
    { key: 'region', label: 'Volume datacenter', type: 'text', optional: true },
    { key: 'volume_key_id', label: 'S3 access key', type: 'text', optional: true },
    { key: 'volume_secret', label: 'S3 secret', type: 'secret', optional: true },
  ],
}

const comfy: ProviderKind = {
  kind: 'comfyui',
  label: 'ComfyUI',
  description: '',
  pairing: false,
  paid: false,
  fields: [
    { key: 'url', label: 'Address', type: 'url' },
    { key: 'workflows', label: 'Workflows', type: 'workflows' },
    { key: 'token', label: 'Access token', type: 'secret', optional: true },
  ],
}

const row = (over: Partial<ProviderRow> = {}): ProviderRow => ({
  id: 'rp',
  label: 'RunPod',
  kind: 'serverless',
  paid: true,
  healthy: true,
  latency_ms: 80,
  editable: true,
  ...over,
})

describe('cloudKinds', () => {
  it('keeps rented GPUs and ComfyUI, not another computer or a model hub', () => {
    const kinds = [
      { ...runpod },
      { ...comfy },
      { ...runpod, kind: 'engine', vendor: undefined, label: 'Another computer' },
      { ...runpod, kind: 'engine', vendor: 'runpod', label: 'RunPod pod' },
      { ...runpod, kind: 'api', vendor: 'fal', label: 'fal.ai' },
    ]
    expect(cloudKinds(kinds).map((k) => k.label)).toEqual(['RunPod serverless', 'ComfyUI', 'RunPod pod'])
  })
})

describe('missing', () => {
  it('asks for required fields only, until a volume is named', () => {
    expect(missing(runpod, { endpoint_id: 'ep' }, {}).map((f) => f.key)).toEqual(['token'])
    expect(missing(runpod, { endpoint_id: 'ep', token: 'k', volume: 'vol' }, {}).map((f) => f.key)).toEqual([
      'region',
      'volume_key_id',
      'volume_secret',
    ])
  })

  it('wants at least one workflow for ComfyUI', () => {
    expect(missing(comfy, { url: 'host' }, {}).map((f) => f.key)).toEqual(['workflows'])
    expect(missing(comfy, { url: 'host' }, { shape: '/w/shape.json' })).toEqual([])
  })
})

describe('bodyFor', () => {
  it('sends numbers as numbers and leaves out what is empty', () => {
    expect(bodyFor(runpod, { endpoint_id: ' ep ', token: 'k', price_usd_per_second: '0.00031', volume: '' }, {})).toEqual({
      kind: 'serverless',
      vendor: 'runpod',
      endpoint_id: 'ep',
      token: 'k',
      price_usd_per_second: 0.00031,
    })
  })

  it('sends the chosen workflows by stage type', () => {
    expect(bodyFor(comfy, { url: 'host', label: 'Studio' }, { shape: '/w/s.json', texture: '' })).toEqual({
      kind: 'comfyui',
      url: 'host',
      workflows: { shape: '/w/s.json' },
      label: 'Studio',
    })
  })
})

describe('badNumber', () => {
  it('finds a price that is not a number', () => {
    expect(badNumber(runpod, { price_usd_per_second: 'cheap' })?.key).toBe('price_usd_per_second')
    expect(badNumber(runpod, { price_usd_per_second: '0.0004' })).toBeUndefined()
    expect(badNumber(runpod, { price_usd_per_second: '-1' })?.key).toBe('price_usd_per_second')
  })
})

describe('the provider list', () => {
  it('prices a rented GPU by the hour and places a RunPod endpoint by its id', () => {
    expect(priceNote(row({ price_usd_per_second: 0.00031 }))).toBe('$1.12 per GPU hour')
    expect(priceNote(row())).toBeUndefined()
    expect(placeNote(row({ endpoint_id: 'abc' }))).toBe('RunPod endpoint abc')
    expect(placeNote(row({ url: 'https://x.modal.run' }))).toBe('https://x.modal.run')
  })
})

describe('providerState', () => {
  it('says ready with the answer time, and idle for an engine that only waits for work', () => {
    expect(providerState(row({ healthy: true, latency_ms: 12 }))).toEqual({ label: 'Ready', tone: 'ok', detail: '12 ms' })
    expect(providerState(row({ healthy: false, error: 'idle' }))).toMatchObject({ label: 'Idle' })
    expect(providerState(row({ healthy: false, error: 'idle' })).tone).toBeUndefined()
  })

  it('says checking while a first answer is awaited, and keeps the last known state meanwhile', () => {
    expect(providerState(row({ healthy: false, checking: true }))).toMatchObject({ label: 'Checking', live: true })
    expect(providerState(row({ healthy: true, latency_ms: 3, checking: true })).label).toBe('Ready')
    expect(providerState(row({ healthy: true, latency_ms: 3, checking: true }))).toMatchObject({ tone: 'ok', detail: '3 ms' })
  })

  it('keeps a known fault on show while it is checked again', () => {
    const state = providerState(row({ kind: 'engine', healthy: false, checking: true, error: 'dial tcp 127.0.0.1:9: connectex: refused' }))
    expect(state).toMatchObject({ label: 'Not reachable', tone: 'bad' })
    expect(state.problem).toBeTruthy()
    expect(providerState(row({ healthy: false, checking: true }))).toMatchObject({ label: 'Checking' })
  })

  it('turns a transport error into what to do', () => {
    const refused = providerState(row({ kind: 'engine', healthy: false, error: 'Get "https://127.0.0.1:9/v1/health": dial tcp 127.0.0.1:9: connectex: Nenhuma conexão pôde ser feita' }))
    expect(refused).toMatchObject({ label: 'Not reachable', tone: 'bad' })
    expect(plainProblem('cannot reach https://127.0.0.1:9: Post "https://127.0.0.1:9/v1/pair": dial tcp 127.0.0.1:9: connectex: refused')).toMatch(/^Nothing answers/)
    expect(refused.problem).toMatch(/^Nothing answers at its address/)
    expect(providerState(row({ healthy: false, kind: 'serverless', error: 'dial tcp 127.0.0.1:9: connectex: refused' })).problem).toBe('Nothing answers at that address. Check the address and your connection.')
    expect(plainProblem('Get "https://h/v1/health": context deadline exceeded')).toMatch(/did not answer in time/)
    expect(plainProblem('the engine refused the token')).toMatch(/Replace the key/)
    expect(plainProblem('Head "http://127.0.0.1:9/v1/x": dial tcp 127.0.0.1:9: connectex: refused', true)).toBe('Nothing answers at that address. Check the address and your connection.')
    expect(plainProblem('lookup studio.local: no such host')).toMatch(/not found on the network/)
  })

  it('leaves a sentence the core already wrote as it is', () => {
    expect(plainProblem('Studio PC is switched off or not on this network.')).toBe('Studio PC is switched off or not on this network.')
    expect(plainProblem('')).toBeUndefined()
  })
})

describe('carryValues', () => {
  it('keeps what two kinds share when the kind changes, but never a key', () => {
    expect(carryValues(comfy, { url: 'host', token: 'k', endpoint_id: 'ep', label: 'Mine', price_usd_per_second: '' })).toEqual({ url: 'host', label: 'Mine' })
    expect(carryValues(runpod, { url: 'host', price_usd_per_second: '0.0004' })).toEqual({ price_usd_per_second: '0.0004' })
  })
})
