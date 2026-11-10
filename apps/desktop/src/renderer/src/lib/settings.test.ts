import { describe, expect, it } from 'vitest'
import { countdown, defaultText, engineNote, envName, isLocked, pairingDigits, rangeText, readNumber, ruleFor, sourceNote } from './settings'

describe('readNumber', () => {
  const reserve = { min: 0, max: 64 }
  const minutes = { min: 1, max: 1440, integer: true }

  it('never reads an emptied field as 0', () => {
    expect(readNumber('', 2.5, reserve)).toEqual({ kind: 'empty' })
    expect(readNumber('   ', 2.5, reserve)).toEqual({ kind: 'empty' })
  })

  it('reads a changed value and leaves the saved one alone', () => {
    expect(readNumber('3', 2.5, reserve)).toEqual({ kind: 'value', value: 3 })
    expect(readNumber(' 2.50 ', 2.5, reserve)).toEqual({ kind: 'unchanged' })
    expect(readNumber('0', 2.5, reserve)).toEqual({ kind: 'value', value: 0 })
  })

  it('takes a decimal comma', () => {
    expect(readNumber('1,5', 1, reserve)).toEqual({ kind: 'value', value: 1.5 })
    expect(readNumber('1,5,2', 1, reserve).kind).toBe('invalid')
  })

  it('refuses what is outside the range, with the range in words', () => {
    expect(readNumber('999', 1, reserve)).toEqual({ kind: 'invalid', message: 'Enter a number between 0 and 64.' })
    expect(readNumber('-3', 1, reserve).kind).toBe('invalid')
    expect(readNumber('0', 15, minutes)).toEqual({ kind: 'invalid', message: 'Enter a whole number between 1 and 1440.' })
    expect(readNumber('1e20', 15, minutes).kind).toBe('invalid')
  })

  it('refuses a fraction where a whole number is needed, and text', () => {
    expect(readNumber('2.5', 15, minutes).kind).toBe('invalid')
    expect(readNumber('abc', 15, minutes).kind).toBe('invalid')
    expect(readNumber('Infinity', 1, { min: 0 }).kind).toBe('invalid')
  })
})

describe('rangeText', () => {
  it('names an open range', () => {
    expect(rangeText({ min: 0 })).toBe('Enter a number, 0 or more.')
  })
})

describe('ruleFor', () => {
  it('lets the limits the core reports win over the app’s own', () => {
    expect(ruleFor({ min: 1, max: 600 }, { min: 1, max: 1440, integer: true })).toEqual({ min: 1, max: 600, integer: true })
    expect(ruleFor({}, { min: 0, max: 64 })).toEqual({ min: 0, max: 64 })
    expect(ruleFor(undefined, { min: 0 })).toEqual({ min: 0 })
  })
})

describe('where a locked value comes from', () => {
  it('names the variable of a nested key with every level', () => {
    expect(envName('routing.budget_usd.per_day')).toBe('FORJA_ROUTING__BUDGET_USD__PER_DAY')
    expect(sourceNote({ key: 'routing.budget_usd.per_day', source: 'env' })).toBe('Set by the environment variable FORJA_ROUTING__BUDGET_USD__PER_DAY.')
  })

  it('prefers the name the core gives', () => {
    expect(sourceNote({ key: 'engine.fit_mode', source: 'env', source_detail: 'FORJA_ENGINE__FIT_MODE' })).toMatch(/FORJA_ENGINE__FIT_MODE\.$/)
    expect(sourceNote({ key: 'routing.policy', source: 'project', source_detail: 'D:/game/forja.toml' })).toBe('Set by the project file D:/game/forja.toml.')
  })

  it('locks only what a higher layer set', () => {
    expect(isLocked({ source: 'env' })).toBe(true)
    expect(isLocked({ source: 'flag' })).toBe(true)
    expect(isLocked({ source: 'user' })).toBe(false)
    expect(isLocked({ source: 'system' })).toBe(false)
    expect(sourceNote({ key: 'engine.fit_mode', source: 'user' })).toBeUndefined()
  })
})

describe('engineNote', () => {
  it('says only what the core did', () => {
    expect(engineNote('saved')).toBeUndefined()
    expect(engineNote(undefined)).toBeUndefined()
    expect(engineNote('restarted')).toMatch(/stopped/)
    expect(engineNote('pending')).toMatch(/when the running job ends/)
    // An older core cannot tell a stopped engine from a busy one: no promise about running jobs.
    expect(engineNote('next-start')).toBe('Saved. The engine takes the change the next time it starts.')
  })
})

describe('defaultText', () => {
  it('names a default as the reset button says it', () => {
    expect(defaultText(15, 'minutes')).toBe('15 minutes')
    expect(defaultText('')).toBe('automatic')
    expect(defaultText(false)).toBe('off')
    expect(defaultText('prefer-local')).toBe('prefer-local')
  })
})

describe('pairingDigits', () => {
  it('takes six digits however they are spaced, and nothing else', () => {
    expect(pairingDigits('482 913')).toBe('482913')
    expect(pairingDigits('482-913')).toBe('482913')
    expect(pairingDigits('48291')).toBeUndefined()
    expect(pairingDigits('abc')).toBeUndefined()
    expect(pairingDigits('4829134')).toBeUndefined()
  })
})

describe('countdown', () => {
  it('counts down to a moment and ends', () => {
    const now = Date.parse('2026-11-09T10:00:00Z')
    expect(countdown('2026-11-09T10:09:41Z', now)).toBe('9:41')
    expect(countdown('2026-11-09T10:00:05Z', now)).toBe('0:05')
    expect(countdown('2026-11-09T10:00:00Z', now)).toBeUndefined()
    expect(countdown('not a date', now)).toBeUndefined()
  })
})
