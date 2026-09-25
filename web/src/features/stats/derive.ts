import type { HistorySeries } from '../../api/types'

type Values = (number | null)[]

const present = (v: Values) => v.filter((x): x is number => x != null)

/** Totals over the shown range; averages count only steps with data. */
export function summarize(s: HistorySeries) {
  const hashrate = present(s.hashrate)
  const miners = present(s.miners)
  const sum = (v: Values) => present(v).reduce((a, b) => a + b, 0)
  const accepted = sum(s.accepted)
  const rejected = sum(s.rejected)
  return {
    hasData: hashrate.length > 0,
    avgHashrate: hashrate.length ? hashrate.reduce((a, b) => a + b, 0) / hashrate.length : 0,
    accepted,
    rejected,
    peakMiners: miners.length ? Math.max(...miners) : 0,
    avgMiners: miners.length ? miners.reduce((a, b) => a + b, 0) / miners.length : 0,
  }
}

/** Rejected shares as a percentage of all shares in each step. */
export function rejectRate(s: { accepted: Values; rejected: Values }): Values {
  return s.accepted.map((a, i) => {
    const r = s.rejected[i]
    if (a == null || r == null || a + r === 0) return null
    return (r / (a + r)) * 100
  })
}

export interface PoolLine {
  label: string
  slot: number
  values: Values
}

const SLOTS = 8

/**
 * One line per pool that had hashrate in the range. A pool's color slot is
 * its position in the pool list, so it keeps its color whatever range is
 * shown; pools past the eight slots are added up into one "other" line.
 */
export function poolLines(s: HistorySeries, order: string[], names: Record<string, string>, other: string): PoolLine[] {
  const rank = (id: string) => {
    const i = order.indexOf(id)
    return i < 0 ? order.length : i
  }
  const ids = Object.keys(s.pools)
    .filter((id) => present(s.pools[id] ?? []).some((v) => v > 0))
    .sort((a, b) => rank(a) - rank(b) || a.localeCompare(b))
  const lines: PoolLine[] = []
  const rest: string[] = []
  for (const id of ids) {
    const slot = rank(id) + 1
    if (slot < SLOTS) lines.push({ label: names[id] ?? id, slot, values: s.pools[id] ?? [] })
    else rest.push(id)
  }
  if (rest.length) {
    const values = s.time.map((_, i) => {
      const parts = rest.map((id) => s.pools[id]?.[i]).filter((v): v is number => v != null)
      return parts.length ? parts.reduce((a, b) => a + b, 0) : null
    })
    lines.push({ label: rest.length === 1 ? (names[rest[0]!] ?? rest[0]!) : other, slot: SLOTS, values })
  }
  return lines
}

/** The steps as CSV, one column per pool. */
export function toCSV(s: HistorySeries, names: Record<string, string>): string {
  const ids = Object.keys(s.pools)
  const quote = (v: string) => (/[",\n]/.test(v) ? `"${v.replaceAll('"', '""')}"` : v)
  const head = ['time', 'hashrate_hs', 'accepted', 'rejected', 'miners', ...ids.map((id) => `hashrate_hs_${names[id] ?? id}`)]
  const cell = (v: number | null | undefined) => (v == null ? '' : String(Math.round(v * 100) / 100))
  const rows = s.time.map((t, i) =>
    [
      new Date(t * 1000).toISOString(),
      cell(s.hashrate[i]),
      cell(s.accepted[i]),
      cell(s.rejected[i]),
      cell(s.miners[i]),
      ...ids.map((id) => cell(s.pools[id]?.[i])),
    ].join(','),
  )
  return [head.map(quote).join(','), ...rows].join('\n') + '\n'
}
