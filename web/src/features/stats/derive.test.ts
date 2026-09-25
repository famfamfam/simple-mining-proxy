import { describe, expect, it } from 'vitest'
import type { HistorySeries } from '../../api/types'
import { poolLines, rejectRate, summarize, toCSV } from './derive'

const series: HistorySeries = {
  step: 60,
  time: [0, 60, 120],
  hashrate: [100, null, 300],
  accepted: [9, null, 30],
  rejected: [1, null, 0],
  miners: [2, null, 4],
  pools: { b: [0, null, 300], a: [100, null, 0], idle: [0, null, 0] },
}

describe('stats', () => {
  it('summarizes only steps with data', () => {
    expect(summarize(series)).toEqual({
      hasData: true,
      avgHashrate: 200,
      accepted: 39,
      rejected: 1,
      peakMiners: 4,
      avgMiners: 3,
    })
  })

  it('computes the reject rate per step', () => {
    expect(rejectRate(series)).toEqual([10, null, 0])
  })

  it('keeps pool colors by pool order and skips idle pools', () => {
    const lines = poolLines(series, ['a', 'b', 'idle'], { a: 'Pool A', b: 'Pool B' }, 'Other')
    expect(lines.map((l) => [l.label, l.slot])).toEqual([
      ['Pool A', 1],
      ['Pool B', 2],
    ])
  })

  it('adds up pools past the eighth slot', () => {
    const many = Object.fromEntries(Array.from({ length: 10 }, (_, i) => [`p${i}`, [1, null, 1]]))
    const lines = poolLines({ ...series, pools: many }, Object.keys(many), {}, 'Other')
    expect(lines).toHaveLength(8)
    expect(lines[7]).toEqual({ label: 'Other', slot: 8, values: [3, null, 3] })
  })

  it('exports CSV with a column per pool', () => {
    const csv = toCSV(series, { a: 'Pool, A' }).split('\n')
    expect(csv[0]).toBe('time,hashrate_hs,accepted,rejected,miners,hashrate_hs_b,"hashrate_hs_Pool, A",hashrate_hs_idle')
    expect(csv[1]).toBe('1970-01-01T00:00:00.000Z,100,9,1,2,0,100,0')
    expect(csv[2]).toBe('1970-01-01T00:01:00.000Z,,,,,,,')
  })
})
