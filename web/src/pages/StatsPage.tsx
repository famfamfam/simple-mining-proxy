import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useHistory, usePools, useWorkers } from '../api/queries'
import type { History } from '../api/types'
import { QueryState, StatCard } from '../components/Panel'
import { RangePicker } from '../components/RangePicker'
import { TimeChart } from '../components/TimeChart'
import { MinerDialog } from '../features/miners/MinerDialog'
import { WorkersTable } from '../features/miners/WorkersTable'
import { poolLines, rejectRate, summarize, toCSV } from '../features/stats/derive'
import { href } from '../hooks/useHashRoute'
import { useRange } from '../hooks/useRange'
import { locale } from '../i18n'
import { parseGoDuration } from '../lib/duration'
import { formatBytes, formatDateTime, formatDuration, formatHashrateHs, formatInt, formatPercent } from '../lib/format'

const ranges = ['1h', '6h', '24h', '7d', '30d', '90d', '1y'] as const

function download(name: string, text: string) {
  const url = URL.createObjectURL(new Blob([text], { type: 'text/csv' }))
  const a = document.createElement('a')
  a.href = url
  a.download = name
  a.click()
  URL.revokeObjectURL(url)
}

/** The data behind the charts, for reading without a chart and for export. */
function DataTable({ data }: { data: History }) {
  const { t } = useTranslation()
  const loc = locale()
  const s = data.series
  const rows = s.time.map((time, i) => ({ time, i })).reverse()
  return (
    <details className="panel data-table">
      <summary>{t('stats.table')}</summary>
      <div className="toolbar">
        <button type="button" className="small" onClick={() => download('proxy-stats.csv', toCSV(s, data.pool_names))}>
          {t('stats.csv')}
        </button>
      </div>
      <div className="table-wrap scroll">
        <table>
          <thead>
            <tr>
              <th>{t('stats.time')}</th>
              <th className="num">{t('stats.hashrate')}</th>
              <th className="num">{t('miners.accepted')}</th>
              <th className="num">{t('miners.rejected')}</th>
              <th className="num">{t('stats.miners')}</th>
            </tr>
          </thead>
          <tbody>
            {rows.map(({ time, i }) => (
              <tr key={time}>
                <td>{formatDateTime(new Date(time * 1000).toISOString(), loc)}</td>
                <td className="num">{s.hashrate[i] == null ? '—' : formatHashrateHs(s.hashrate[i])}</td>
                <td className="num">{s.accepted[i] == null ? '—' : formatInt(s.accepted[i], loc)}</td>
                <td className="num">{s.rejected[i] == null ? '—' : formatInt(s.rejected[i], loc)}</td>
                <td className="num">{s.miners[i] == null ? '—' : s.miners[i].toFixed(1)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </details>
  )
}

/** Every miner seen in the period, including the ones not connected now. */
function Workers({ seconds }: { seconds: number }) {
  const { t } = useTranslation()
  const workers = useWorkers(seconds)
  const [open, setOpen] = useState<string | null>(null)
  if (!workers.data?.length) return null
  return (
    <>
      <h2>{t('stats.workers', { count: workers.data.length })}</h2>
      <WorkersTable workers={workers.data} onOpen={setOpen} />
      {open && <MinerDialog name={open} onClose={() => setOpen(null)} />}
    </>
  )
}

function Charts({ data, seconds }: { data: History; seconds: number }) {
  const { t } = useTranslation()
  const loc = locale()
  const pools = usePools()
  const s = data.series
  const sum = summarize(s)
  const order = (pools.data ?? []).map((p) => p.id)
  const lines = poolLines(s, order, data.pool_names, t('stats.otherPools'))
  const step = formatDuration(s.step, t)

  if (!sum.hasData) return <div className="panel muted">{t('stats.noData')}</div>
  return (
    <>
      <div className="cards">
        <StatCard label={t('stats.avgHashrate')} value={formatHashrateHs(sum.avgHashrate)} sub={t('stats.pointEvery', { step })} />
        <StatCard label={t('stats.accepted')} value={formatInt(sum.accepted, loc)} />
        <StatCard
          label={t('stats.rejected')}
          value={formatInt(sum.rejected, loc)}
          sub={t('stats.rejectedSub', { pct: formatPercent(sum.rejected, sum.accepted + sum.rejected) })}
        />
        <StatCard
          label={t('stats.peakMiners')}
          value={formatInt(sum.peakMiners, loc)}
          sub={t('stats.avgMiners', { value: sum.avgMiners.toFixed(1) })}
        />
      </div>
      <div className="panel">
        <TimeChart
          title={t('stats.hashrate')}
          time={s.time}
          series={[{ label: t('stats.hashrate'), values: s.hashrate, slot: 1, fill: true }]}
          format={formatHashrateHs}
          height={260}
        />
      </div>
      {lines.length > 1 && (
        <div className="panel">
          <TimeChart title={t('stats.byPool')} time={s.time} series={lines} format={formatHashrateHs} />
        </div>
      )}
      <div className="chart-pair">
        <div className="panel">
          <TimeChart
            title={t('stats.miners')}
            time={s.time}
            series={[{ label: t('stats.miners'), values: s.miners, slot: 1, stepped: true }]}
            format={(v) => (Number.isInteger(v) ? formatInt(v, loc) : v.toFixed(1))}
            height={180}
          />
        </div>
        <div className="panel">
          <TimeChart
            title={t('stats.rejectRate')}
            time={s.time}
            series={[{ label: t('stats.rejectRate'), values: rejectRate(s), slot: 1 }]}
            format={(v) => `${v.toFixed(v < 1 ? 2 : 1)}%`}
            height={180}
          />
        </div>
      </div>
      <Workers seconds={seconds} />
      <DataTable data={data} />
    </>
  )
}

export function StatsPage() {
  const { t } = useTranslation()
  const { range, seconds, setRange } = useRange('stats.range', ranges, '24h')
  const history = useHistory(seconds)
  const data = history.data

  return (
    <>
      <h2>{t('stats.title')}</h2>
      <RangePicker keys={ranges} value={range} onChange={setRange} />
      <QueryState pending={history.isPending} error={history.error}>
        {data && (
          <div className={history.isPlaceholderData ? 'stats refetching' : 'stats'}>
            <Charts data={data} seconds={seconds} />
            <p className="small muted">
              {t('stats.storage', {
                detail: formatDuration(parseGoDuration(data.detail_retention), t),
                total: formatDuration(parseGoDuration(data.retention), t),
                miners: formatDuration(parseGoDuration(data.miner_retention), t),
                size: formatBytes(data.disk_bytes, t),
              })}{' '}
              <a href={href('settings')}>{t('stats.storageLink')}</a>
            </p>
          </div>
        )}
      </QueryState>
    </>
  )
}
