import { useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { useMiners, useWorkers } from '../api/queries'
import type { Miner } from '../api/types'
import { QueryState } from '../components/Panel'
import { MinerDialog } from '../features/miners/MinerDialog'
import { WorkersTable } from '../features/miners/WorkersTable'
import { useSessionState } from '../hooks/useSessionState'
import { locale } from '../i18n'
import { formatAgo, formatDateTime, formatDifficulty, formatHashrateHs, formatInt } from '../lib/format'

type SortKey = keyof Pick<
  Miner,
  | 'worker'
  | 'upstream_user'
  | 'ip'
  | 'transport'
  | 'pool_name'
  | 'user_agent'
  | 'difficulty'
  | 'accepted'
  | 'rejected'
  | 'last_share'
  | 'hashrate_hs'
  | 'connected_at'
>

interface Column {
  key: SortKey
  title: string
  numeric?: boolean
  mono?: boolean
  render: (m: Miner) => ReactNode
}

function compare(a: Miner, b: Miner, key: SortKey, loc: string): number {
  const x = a[key] ?? ''
  const y = b[key] ?? ''
  if (typeof x === 'number' && typeof y === 'number') return x - y
  return String(x).localeCompare(String(y), loc, { numeric: true })
}

/** Miners seen in the last day that are not connected now. */
function Offline({ connected, onOpen }: { connected: Set<string>; onOpen: (name: string) => void }) {
  const { t } = useTranslation()
  const workers = useWorkers(24 * 3600)
  const offline = (workers.data ?? []).filter((w) => !connected.has(w.name))
  if (!offline.length) return null
  return (
    <>
      <h2>{t('miners.offline', { count: offline.length })}</h2>
      <p className="small muted">{t('miners.offlineHelp')}</p>
      <WorkersTable workers={offline.sort((a, b) => b.last_seen - a.last_seen)} onOpen={onOpen} />
    </>
  )
}

export function MinersPage() {
  const { t } = useTranslation()
  const loc = locale()
  const miners = useMiners()
  const [open, setOpen] = useState<string | null>(null)
  const [sort, setSort] = useSessionState<{ key: SortKey; dir: number }>('miners.sort', { key: 'worker', dir: 1 })
  const [filter, setFilter] = useSessionState('miners.filter', '')

  const columns: Column[] = [
    {
      key: 'worker',
      title: t('miners.worker'),
      mono: true,
      render: (m) =>
        m.worker ? (
          <button type="button" className="link" title={t('miners.openStats')} onClick={() => setOpen(m.worker)}>
            {m.worker}
          </button>
        ) : (
          '—'
        ),
    },
    { key: 'upstream_user', title: t('miners.upstreamUser'), mono: true, render: (m) => m.upstream_user || '—' },
    { key: 'ip', title: t('miners.ip'), mono: true, render: (m) => m.ip },
    { key: 'transport', title: t('miners.transport'), render: (m) => m.transport.toUpperCase() },
    {
      key: 'pool_name',
      title: t('miners.pool'),
      render: (m) => (
        <>
          {m.pool_name}
          {m.pool_addr && <div className="small muted mono">{m.pool_addr}</div>}
        </>
      ),
    },
    { key: 'user_agent', title: t('miners.userAgent'), render: (m) => <span className="small">{m.user_agent || '—'}</span> },
    { key: 'difficulty', title: t('miners.difficulty'), numeric: true, render: (m) => formatDifficulty(m.difficulty) },
    { key: 'accepted', title: t('miners.accepted'), numeric: true, render: (m) => formatInt(m.accepted, loc) },
    { key: 'rejected', title: t('miners.rejected'), numeric: true, render: (m) => formatInt(m.rejected, loc) },
    { key: 'last_share', title: t('miners.lastShare'), render: (m) => formatAgo(m.last_share, t) },
    { key: 'hashrate_hs', title: t('miners.hashrate'), numeric: true, render: (m) => formatHashrateHs(m.hashrate_hs) },
    { key: 'connected_at', title: t('miners.connected'), render: (m) => formatDateTime(m.connected_at, loc) },
  ]

  const all = miners.data ?? []
  const q = filter.toLowerCase()
  const rows = all
    .filter((m) => !q || [m.worker, m.upstream_user, m.ip, m.pool_name].some((v) => v.toLowerCase().includes(q)))
    .sort((a, b) => compare(a, b, sort.key, loc) * sort.dir)
  const total = rows.reduce((sum, m) => sum + m.hashrate_hs, 0)

  const sortBy = (key: SortKey) => setSort({ key, dir: sort.key === key ? -sort.dir : 1 })

  return (
    <>
      <h2>{t('miners.title')}</h2>
      <div className="toolbar">
        <input
          type="search"
          className="filter"
          placeholder={t('miners.filter')}
          aria-label={t('miners.filter')}
          value={filter}
          onChange={(e) => setFilter(e.target.value)}
        />
        <span className="muted">
          {t('miners.count', { shown: rows.length, total: all.length })} ·{' '}
          {t('miners.totalHashrate', { value: formatHashrateHs(total) })}
        </span>
      </div>
      <QueryState pending={miners.isPending} error={miners.error}>
        <div className="panel table-wrap">
          <table className="responsive">
            <thead>
              <tr>
                {columns.map((c) => (
                  <th
                    key={c.key}
                    className={c.numeric ? 'num sortable' : 'sortable'}
                    aria-sort={sort.key === c.key ? (sort.dir > 0 ? 'ascending' : 'descending') : 'none'}
                  >
                    <button type="button" className="link plain" onClick={() => sortBy(c.key)}>
                      {c.title}
                      {sort.key === c.key ? (sort.dir > 0 ? ' ↑' : ' ↓') : ''}
                    </button>
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {rows.map((m) => (
                <tr key={m.id}>
                  {columns.map((c, i) => (
                    <td
                      key={c.key}
                      data-label={c.title}
                      className={[i === 0 ? 'primary' : '', c.numeric ? 'num' : '', c.mono ? 'mono' : ''].join(' ').trim()}
                    >
                      {c.render(m)}
                    </td>
                  ))}
                </tr>
              ))}
            </tbody>
          </table>
          {!rows.length && <div className="muted empty">{all.length ? t('miners.nothingFound') : t('miners.empty')}</div>}
        </div>
        <Offline connected={new Set(all.map((m) => m.worker))} onOpen={setOpen} />
      </QueryState>
      {open && <MinerDialog name={open} onClose={() => setOpen(null)} />}
    </>
  )
}
