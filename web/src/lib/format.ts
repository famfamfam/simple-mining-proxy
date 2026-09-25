import type { TFunction } from 'i18next'

/** Hashrate in TH/s with a unit that keeps 1–3 digits before the point. */
export function formatHashrate(ths: number): string {
  if (!ths || ths < 0) return '0 H/s'
  const units: [string, number][] = [
    ['EH/s', 1e6],
    ['PH/s', 1e3],
    ['TH/s', 1],
    ['GH/s', 1e-3],
    ['MH/s', 1e-6],
  ]
  for (const [unit, k] of units) {
    if (ths >= k) {
      const v = ths / k
      return `${v.toFixed(v >= 100 ? 0 : 2)} ${unit}`
    }
  }
  return `${(ths * 1e12).toFixed(0)} H/s`
}

/** Hashrate given in H/s, as the history and miners API report it. */
export function formatHashrateHs(hs: number): string {
  return formatHashrate(hs / 1e12)
}

/** Share difficulty in short form: 524288 → 524.3K. */
export function formatDifficulty(d: number): string {
  if (!d) return '—'
  if (d >= 1e12) return `${(d / 1e12).toFixed(1)}T`
  if (d >= 1e9) return `${(d / 1e9).toFixed(1)}G`
  if (d >= 1e6) return `${(d / 1e6).toFixed(1)}M`
  if (d >= 1e3) return `${(d / 1e3).toFixed(1)}K`
  return String(Math.round(d))
}

export function formatInt(n: number, locale: string): string {
  return (n || 0).toLocaleString(locale)
}

export function formatPercent(part: number, total: number): string {
  return total ? `${((part / total) * 100).toFixed(2)}%` : '0%'
}

export function formatBytes(n: number, t: TFunction): string {
  if (n < 1024) return t('units.bytes', { value: n })
  if (n < 1024 * 1024) return t('units.kb', { value: (n / 1024).toFixed(1) })
  if (n < 1024 ** 3) return t('units.mb', { value: (n / 1024 ** 2).toFixed(1) })
  return t('units.gb', { value: (n / 1024 ** 3).toFixed(2) })
}

export function formatMs(ms: number, t: TFunction): string {
  if (ms < 0.1) return t('units.ms', { value: '<0.1' })
  return t('units.ms', { value: ms >= 100 ? Math.round(ms) : ms.toFixed(1) })
}

export function formatDateTime(iso: string | null | undefined, locale: string): string {
  if (!iso) return '—'
  const d = new Date(iso)
  return `${d.toLocaleDateString(locale, { day: '2-digit', month: '2-digit' })} ${d.toLocaleTimeString(locale)}`
}

/** Hours and minutes only: "14:30". */
export function formatTime(iso: string | null | undefined, locale: string): string {
  if (!iso) return '—'
  return new Date(iso).toLocaleTimeString(locale, { hour: '2-digit', minute: '2-digit' })
}

export function formatDate(iso: string, locale: string): string {
  return new Date(iso).toLocaleDateString(locale)
}

/**
 * Human duration from seconds, largest units first: 90 → "1 min 30 s",
 * 7200 → "2 h". Days appear only for long uptimes.
 */
export function formatDuration(totalSec: number, t: TFunction): string {
  let sec = Math.max(0, Math.round(totalSec))
  if (sec === 0) return t('units.s', { value: 0 })
  const parts: string[] = []
  const steps: [string, number][] = [
    ['d', 86400],
    ['h', 3600],
    ['min', 60],
    ['s', 1],
  ]
  for (const [unit, size] of steps) {
    if (sec >= size) {
      parts.push(t(`units.${unit}`, { value: Math.floor(sec / size) }))
      sec %= size
    }
  }
  return parts.slice(0, 2).join(' ')
}

/** A long average time: "40 min", "3 h 20 min", "45 d", "1.5 years". */
export function formatLongDuration(sec: number, t: TFunction, locale: string): string {
  if (!Number.isFinite(sec) || sec <= 0) return '—'
  const year = 365.25 * 86400
  if (sec >= year) {
    const years = sec / year
    const rounded = years >= 10 ? Math.round(years) : Math.round(years * 10) / 10
    return t('units.y', { count: rounded, value: rounded.toLocaleString(locale) })
  }
  if (sec >= 2 * 86400) return t('units.d', { value: Math.round(sec / 86400) })
  return formatDuration(sec >= 600 ? Math.round(sec / 60) * 60 : sec, t)
}

/** A probability: "12%", "0.034%", or "1 in 52,000" when tiny. */
export function formatChance(p: number, t: TFunction, locale: string): string {
  if (!(p > 0)) return '0%'
  if (p >= 0.00001) return `${(p * 100).toLocaleString(locale, { maximumSignificantDigits: 2 })}%`
  return t('units.oneIn', { n: Math.round(1 / p).toLocaleString(locale) })
}

export function formatAgo(iso: string | null | undefined, t: TFunction, now = Date.now()): string {
  if (!iso) return '—'
  const sec = Math.max(0, Math.round((now - new Date(iso).getTime()) / 1000))
  const step = sec < 60 ? sec : sec < 3600 ? Math.floor(sec / 60) * 60 : Math.floor(sec / 3600) * 3600
  return t('units.ago', { value: formatDuration(step, t) })
}
