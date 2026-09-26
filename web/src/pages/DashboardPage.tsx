import { useCallback, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api } from '../api/client'
import { useEvents, useHistory, useNetwork, usePools, useProfit, useStatus, useTimed } from '../api/queries'
import type { ConnString, Pool, Status } from '../api/types'
import { HealthBadge, ModeBadge, SoloBadge } from '../components/Badges'
import { CopyButton } from '../components/CopyButton'
import { QueryState, StatCard } from '../components/Panel'
import { TimeChart } from '../components/TimeChart'
import { FallbackEditor } from '../features/pools/FallbackEditor'
import { PoolEditor } from '../features/pools/PoolEditor'
import { PoolsTable } from '../features/pools/PoolsTable'
import { SoloOdds } from '../features/network/SoloOdds'
import { ProfitPanel } from '../features/profit/ProfitPanel'
import { TestDialog } from '../features/pools/TestDialog'
import { usePoolActions } from '../features/pools/usePoolActions'
import { HuntPanel } from '../features/timed/HuntPanel'
import { TimedPanel } from '../features/timed/TimedPanel'
import { href } from '../hooks/useHashRoute'
import { locale } from '../i18n'
import { formatDateTime, formatHashrate, formatHashrateHs, formatInt, formatPercent } from '../lib/format'
import { poolNameIn } from '../lib/pools'
import { EventList } from './EventsPage'

function StatusCards({ status, pools }: { status: Status; pools: Pool[] }) {
  const { t } = useTranslation()
  const loc = locale()
  const name = (id: string) => poolNameIn(pools, id) || '—'
  const active = pools.find((p) => p.id === status.active_pool)
  const effective = pools.find((p) => p.id === status.effective_pool)
  const ls = status.last_switch
  const { accepted, rejected } = status.shares
  return (
    <div className="cards">
      <StatCard
        label={t('dashboard.mode')}
        value={<ModeBadge mode={status.mode} />}
        sub={
          status.effective_pool && status.effective_pool !== status.active_pool ? (
            <>
              {t('dashboard.newSessionsTo', { pool: name(status.effective_pool) })} {effective?.solo && <SoloBadge />}
            </>
          ) : undefined
        }
      />
      <StatCard
        label={t('dashboard.activePool')}
        value={
          active ? (
            <>
              {active.name} {active.coin && <span className="muted">{active.coin}</span>}
            </>
          ) : (
            '—'
          )
        }
        sub={
          active ? (
            <>
              <HealthBadge health={active.health} /> {active.solo && <SoloBadge />}
            </>
          ) : (
            t('dashboard.addPoolHint')
          )
        }
      />
      <StatCard
        label={t('dashboard.miners')}
        value={formatInt(status.miners.total, loc)}
        sub={t('dashboard.minersSub', { tcp: status.miners.tcp, tls: status.miners.tls })}
      />
      <StatCard label={t('dashboard.hashrate')} value={formatHashrate(status.hashrate_ths)} sub={t('dashboard.hashrateSub')} />
      <StatCard
        label={t('dashboard.shares')}
        value={formatInt(accepted, loc)}
        sub={t('dashboard.sharesSub', { rejected: formatInt(rejected, loc), pct: formatPercent(rejected, accepted + rejected) })}
      />
      <StatCard
        label={t('dashboard.lastSwitch')}
        value={ls ? formatDateTime(ls.at, loc) : '—'}
        sub={ls ? `${name(ls.from)} → ${name(ls.to)}` : undefined}
      />
    </div>
  )
}

/** Profit switching, shown only when it is on. */
function ProfitSection(props: { pools: Pool[]; hashrateTHs: number; onSwitch: (p: Pool) => void }) {
  const { t } = useTranslation()
  const profit = useProfit()
  const timed = useTimed()
  if (!profit.data || profit.data.mode === 'off') return null
  return (
    <>
      <h2>{t('profit.title')}</h2>
      <ProfitPanel status={profit.data} timed={timed.data} {...props} />
    </>
  )
}

/** Network difficulty and the farm's solo odds. */
function NetworkSection({ pools, hashrateTHs }: { pools: Pool[]; hashrateTHs: number }) {
  const { t } = useTranslation()
  const network = useNetwork()
  const timed = useTimed()
  if (!network.data) return null
  return (
    <>
      <h2>{t('network.title')}</h2>
      <SoloOdds network={network.data} hashrateTHs={hashrateTHs} pools={pools} timed={timed.data} />
    </>
  )
}

/** The last 24 hours of hashrate; the charts screen has the rest. */
function HashrateDay() {
  const { t } = useTranslation()
  const history = useHistory(24 * 3600, 288)
  const s = history.data?.series
  if (!s?.hashrate.some((v) => v != null)) return null
  return (
    <div className="panel">
      <TimeChart
        title={t('dashboard.hashrateDay')}
        time={s.time}
        series={[{ label: t('stats.hashrate'), values: s.hashrate, slot: 1, fill: true }]}
        format={formatHashrateHs}
        height={150}
      />
      <a className="small" href={href('stats')}>
        {t('dashboard.allCharts')}
      </a>
    </div>
  )
}

function ConnectionRow({ label, conn }: { label: string; conn: ConnString }) {
  const { t } = useTranslation()
  if (!conn.enabled) {
    return (
      <div className="conn">
        <span className="muted">{label}</span>
        <span className="muted">{t('dashboard.listenerOff')}</span>
      </div>
    )
  }
  const host = conn.host || t('dashboard.hostPlaceholder')
  const url = `${conn.scheme}://${host}:${conn.port}`
  return (
    <div className="conn">
      <span className="muted">{label}</span>
      <span className="mono wrap">{url}</span>
      {conn.host && <CopyButton text={url} />}
    </div>
  )
}

function ConnectionPanel({ status }: { status: Status }) {
  const { t } = useTranslation()
  const { tcp, tls } = status.connections
  const alt = `stratum+ssl://${tls.host || t('dashboard.hostPlaceholder')}:${tls.port}`
  return (
    <div className="panel">
      <ConnectionRow label="Stratum TCP" conn={tcp} />
      <ConnectionRow label="Stratum TLS" conn={tls} />
      {tls.enabled && <p className="small muted">{t('dashboard.tlsSchemeHint', { alt })}</p>}
      <p className="small muted">{t('dashboard.loginHint')}</p>
      {!tcp.host && <p className="small muted">{t('dashboard.hostHint')}</p>}
    </div>
  )
}

export function DashboardPage() {
  const { t } = useTranslation()
  const status = useStatus()
  const pools = usePools()
  const events = useEvents(10)
  const [editing, setEditing] = useState<Pool | 'new' | null>(null)
  const [testing, setTesting] = useState<Pool | null>(null)
  const [fallback, setFallback] = useState(false)
  const minersTotal = status.data?.miners.total ?? 0
  const actions = usePoolActions(minersTotal)
  const runTest = useCallback(() => api.testPool(testing?.id ?? ''), [testing])

  return (
    <QueryState pending={status.isPending || pools.isPending} error={status.error ?? pools.error}>
      {status.data && pools.data && (
        <>
          <StatusCards status={status.data} pools={pools.data} />
          <HashrateDay />

          <h2>{t('dashboard.connection')}</h2>
          <ConnectionPanel status={status.data} />

          <h2>{t('dashboard.pools')}</h2>
          <div className="toolbar">
            <button type="button" className="primary" onClick={() => setEditing('new')}>
              {t('dashboard.addPool')}
            </button>
            <button type="button" onClick={() => setFallback(true)} disabled={pools.data.length < 2}>
              {t('dashboard.fallbackOrder')}
            </button>
            <button type="button" onClick={() => void actions.reconnectAll()} disabled={!minersTotal}>
              {t('dashboard.reconnectAll')}
            </button>
          </div>
          {pools.data.length ? (
            <PoolsTable
              pools={pools.data}
              onSwitch={(p) => void actions.switchPool(p)}
              onTest={setTesting}
              onEdit={setEditing}
              onDelete={(p) => void actions.deletePool(p)}
            />
          ) : (
            <div className="panel muted">{t('dashboard.noPools')}</div>
          )}
          <TimedPanel pools={pools.data} />
          <HuntPanel pools={pools.data} />

          <ProfitSection pools={pools.data} hashrateTHs={status.data.hashrate_ths} onSwitch={(p) => void actions.switchPool(p)} />
          <NetworkSection pools={pools.data} hashrateTHs={status.data.hashrate_ths} />

          <h2>
            {t('dashboard.recentEvents')}{' '}
            <a className="small" href={href('events')}>
              {t('dashboard.allEvents')}
            </a>
          </h2>
          <EventList events={events.data ?? []} />
        </>
      )}
      {editing && <PoolEditor pool={editing === 'new' ? null : editing} onClose={() => setEditing(null)} />}
      {testing && <TestDialog title={t('test.title', { pool: testing.name })} run={runTest} onClose={() => setTesting(null)} />}
      {fallback && pools.data && <FallbackEditor pools={pools.data} onClose={() => setFallback(false)} />}
    </QueryState>
  )
}
