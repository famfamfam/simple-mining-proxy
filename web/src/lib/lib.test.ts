import type { TFunction } from 'i18next'
import { describe, expect, it } from 'vitest'
import { hostPort, parseAddresses } from './addresses'
import { bestUnit, parseGoDuration, toGoDuration, unitsUpTo } from './duration'
import {
  formatAgo,
  formatChance,
  formatDifficulty,
  formatDuration,
  formatHashrate,
  formatHashrateHs,
  formatLongDuration,
} from './format'
import { blockChance, blockRate } from './odds'

// A stand-in for i18next with English units.
const t = ((key: string, o?: { value?: unknown }) => {
  const units: Record<string, string> = { d: 'd', h: 'h', min: 'min', s: 's', ms: 'ms' }
  const unit = key.replace('units.', '')
  if (unit === 'ago') return `${String(o?.value)} ago`
  return `${String(o?.value)} ${units[unit] ?? unit}`
}) as unknown as TFunction

describe('parseAddresses', () => {
  it('reads host:port lines, IPv6 and pasted URLs', () => {
    const r = parseAddresses('eu.pool.com:3333\n[2001:db8::1]:443, stratum+tcp://us.pool.com:3333/')
    expect(r).toEqual({
      ok: true,
      transport: 'tcp',
      addresses: [
        { host: 'eu.pool.com', port: 3333 },
        { host: '2001:db8::1', port: 443 },
        { host: 'us.pool.com', port: 3333 },
      ],
    })
  })

  it('detects TLS from stratum+tls and stratum+ssl', () => {
    for (const scheme of ['stratum+tls', 'stratum+ssl']) {
      const r = parseAddresses(`${scheme}://eu.emcd.network:13339`)
      expect(r.ok && r.transport).toBe('tls')
    }
  })

  it('gives no transport for mixed or missing schemes', () => {
    const mixed = parseAddresses('stratum+tcp://a.com:1\nstratum+ssl://b.com:2')
    expect(mixed.ok && mixed.transport).toBeNull()
    const plain = parseAddresses('a.com:1')
    expect(plain.ok && plain.transport).toBeNull()
  })

  it('reports the bad line', () => {
    expect(parseAddresses('a.com:1\nno-port')).toEqual({ ok: false, error: 'format', line: 2, text: 'no-port' })
    expect(parseAddresses('  \n ')).toEqual({ ok: false, error: 'empty' })
  })

  it('formats IPv6 with brackets', () => {
    expect(hostPort({ host: '2001:db8::1', port: 443 })).toBe('[2001:db8::1]:443')
  })
})

describe('durations', () => {
  it('round-trips Go durations', () => {
    expect(parseGoDuration('1h30m')).toBe(5400)
    expect(parseGoDuration('500ms')).toBe(0.5)
    // The same canonical form as settings.FormatDuration on the server.
    expect(toGoDuration(120)).toBe('2m')
    expect(toGoDuration(90)).toBe('90s')
    expect(toGoDuration(0)).toBe('0s')
    expect(toGoDuration(3600)).toBe('1h')
    expect(toGoDuration(7 * 86400)).toBe('168h')
  })

  it('offers units up to the maximum and picks the largest whole one', () => {
    expect(unitsUpTo(60)).toEqual(['s', 'm'])
    expect(unitsUpTo(90 * 86400)).toEqual(['s', 'm', 'h', 'd'])
    expect(bestUnit(7 * 86400, ['s', 'm', 'h', 'd'])).toBe('d')
    expect(bestUnit(90, ['s', 'm'])).toBe('s')
    expect(bestUnit(0, ['s', 'm'])).toBe('s')
  })

  it('reads like a person would say it', () => {
    expect(formatDuration(0, t)).toBe('0 s')
    expect(formatDuration(90, t)).toBe('1 min 30 s')
    expect(formatDuration(7200, t)).toBe('2 h')
    expect(formatDuration(3 * 86400 + 14 * 3600 + 5, t)).toBe('3 d 14 h')
  })

  it('says how long ago', () => {
    const now = Date.parse('2026-01-01T12:00:00Z')
    expect(formatAgo('2026-01-01T11:59:15Z', t, now)).toBe('45 s ago')
    expect(formatAgo('2026-01-01T11:57:50Z', t, now)).toBe('2 min ago')
    expect(formatAgo(null, t, now)).toBe('—')
  })
})

describe('solo odds', () => {
  it('computes block rate and chance', () => {
    const d = 1e15 / 2 ** 32 // a block takes 1e15 hashes
    const hs = 1e15 / 3600 // finds one block per hour on average
    expect(blockRate(hs, d) * 3600).toBeCloseTo(1, 10)
    expect(blockChance(hs, d, 3600)).toBeCloseTo(1 - Math.exp(-1), 10)
    expect(blockChance(0, d, 3600)).toBe(0)
    expect(blockChance(hs, 0, 3600)).toBe(0)
    // Tiny chances keep their precision.
    expect(blockChance(1, 1e14, 3600)).toBeGreaterThan(0)
  })

  it('formats chances and long times', () => {
    const tt = ((key: string, o?: { value?: unknown; n?: unknown; count?: number }) =>
      key === 'units.oneIn' ? `1 in ${String(o?.n)}` : key === 'units.y' ? `${String(o?.value)} y` : t(key, o)) as TFunction
    expect(formatChance(0.1234, tt, 'en')).toBe('12%')
    expect(formatChance(0.00034, tt, 'en')).toBe('0.034%')
    expect(formatChance(1 / 52000 / 10, tt, 'en')).toBe('1 in 520,000')
    expect(formatChance(0, tt, 'en')).toBe('0%')
    expect(formatLongDuration(2400, tt, 'en')).toBe('40 min')
    expect(formatLongDuration(34 * 60 + 11, tt, 'en')).toBe('34 min')
    expect(formatLongDuration(45 * 86400, tt, 'en')).toBe('45 d')
    expect(formatLongDuration(1.5 * 365.25 * 86400, tt, 'en')).toBe('1.5 y')
    expect(formatLongDuration(Infinity, tt, 'en')).toBe('—')
  })
})

describe('numbers', () => {
  it('scales hashrate and difficulty', () => {
    expect(formatHashrate(114.49)).toBe('114 TH/s')
    expect(formatHashrate(23810.5)).toBe('23.81 PH/s')
    expect(formatHashrate(0)).toBe('0 H/s')
    expect(formatHashrateHs(198.4e12)).toBe('198 TH/s')
    expect(formatDifficulty(524288)).toBe('524.3K')
    expect(formatDifficulty(0)).toBe('—')
  })
})
