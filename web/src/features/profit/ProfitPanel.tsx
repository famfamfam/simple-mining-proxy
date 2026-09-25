import { useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api } from '../../api/client'
import type { CoinView, Pool, ProfitReport, ProfitStatus, TimedStatus } from '../../api/types'
import { useToast } from '../../context/feedback'
import { locale } from '../../i18n'
import { errorText } from '../../i18n/messages'
import { formatAgo, formatDateTime, formatDifficulty, formatTime } from '../../lib/format'

function usd(v: number, loc: string, digits = 4) {
  return v.toLocaleString(loc, {
    style: 'currency',
    currency: 'USD',
    currencyDisplay: 'narrowSymbol',
    maximumSignificantDigits: digits,
  })
}

function pct(v: number, loc: string) {
  const s = Math.abs(v).toLocaleString(loc, { maximumFractionDigits: 1, minimumFractionDigits: 1 })
  return `${v >= 0 ? '+' : '−'}${s}%`
}

/** The sentence that explains the latest decision. */
function Decision({ report, poolName }: { report: ProfitReport; poolName: (id: string) => string }) {
  const { t } = useTranslation()
  const loc = locale()
  const text = t(`profit.decision.${report.decision}`, {
    coin: report.active_coin,
    best: report.best,
    adv: pct(report.advantage, loc).replace('+', ''),
    margin: report.margin,
    pool: poolName(report.target || report.active),
    error: report.error ?? '',
  })
  const tone = { switched: 'ok', recommend: 'warn', switch_failed: 'bad', no_data: 'bad' }[report.decision as string] ?? ''
  return <p className={`profit-decision ${tone}`}>{text}</p>
}

interface ProfitPanelProps {
  status: ProfitStatus
  pools: Pool[]
  hashrateTHs: number
  onSwitch: (p: Pool) => void
  /** Timed switching: while it holds the farm on its pool, coins are compared for the pool the farm returns to. */
  timed?: TimedStatus
}

/** Profit switching: the coins compared, the decision and the schedule. */
export function ProfitPanel({ status: st, pools, hashrateTHs, onSwitch, timed }: ProfitPanelProps) {
  const { t, i18n } = useTranslation()
  const loc = locale()
  const qc = useQueryClient()
  const toast = useToast()
  const [checking, setChecking] = useState(false)
  const rep = st.report
  const reported = rep?.coins ?? [] // empty when there was no market data
  const poolName = (id: string) => pools.find((p) => p.id === id)?.name ?? id
  const target = rep?.decision === 'recommend' && rep.target ? pools.find((p) => p.id === rep.target) : undefined
  const current = reported.find((c) => c.tag === rep?.active_coin)
  // The farm is on the timer pool now; the report's coin is the one it returns to.
  const timerPool = timed?.home ? pools.find((p) => p.id === timed.target) : undefined

  const check = async () => {
    setChecking(true)
    try {
      qc.setQueryData(['profit'], await api.profitCheck())
    } catch (err) {
      toast(errorText(i18n, err), 'error')
    } finally {
      setChecking(false)
    }
  }

  // Coins with pools that take part first, then the rest of the market.
  const coins = [...reported].sort(
    (a, b) => Number(b.pools.length > 0) - Number(a.pools.length > 0) || b.revenue_btc - a.revenue_btc,
  )
  const vsCurrent = (c: CoinView) =>
    current && current.revenue_btc > 0 && c.tag !== current.tag ? pct((c.revenue_btc / current.revenue_btc - 1) * 100, loc) : ''

  return (
    <div className="panel profit">
      <div className="profit-head">
        <span className={st.mode === 'auto' ? 'badge accent' : 'badge'}>{t(`profit.mode.${st.mode}`)}</span>
        <span className="small muted">
          {rep ? t('profit.checked', { ago: formatAgo(rep.at, t) }) : t('profit.neverChecked')}
          {st.next_run && ` · ${t('profit.next', { at: formatDateTime(st.next_run, loc) })}`}
          {` · ${t('profit.margin', { margin: st.margin })}`}
        </span>
        <span className="grow" />
        <button type="button" className="small" disabled={checking} onClick={() => void check()}>
          {t('profit.checkNow')}
        </button>
      </div>
      {timerPool && timed?.home && (
        <p className="small">
          {t('profit.onTimer', { pool: timerPool.name, until: formatTime(timed.until, loc), home: poolName(timed.home) })}
        </p>
      )}
      {rep && <Decision report={rep} poolName={poolName} />}
      {target && (
        <button type="button" className="primary small" onClick={() => onSwitch(target)}>
          {t('profit.switchTo', { pool: target.name })}
        </button>
      )}
      {coins.length > 0 && (
        <div className="table-wrap">
          <table className="responsive">
            <thead>
              <tr>
                <th>{t('profit.coin')}</th>
                <th className="num">{t('profit.price')}</th>
                <th className="num">{t('profit.difficulty')}</th>
                <th className="num">{t('profit.reward')}</th>
                <th className="num">{t('profit.revenue')}</th>
                <th className="num">{t('profit.vsCurrent')}</th>
                <th>{t('profit.pools')}</th>
              </tr>
            </thead>
            <tbody>
              {coins.map((c) => (
                <tr key={c.tag} className={c.pools.length || c.tag === rep?.active_coin ? undefined : 'muted'}>
                  <td className="primary">
                    <b>{c.tag}</b>{' '}
                    {c.tag === rep?.active_coin && (
                      <span className="badge accent">{timerPool ? t('profit.homeCoin') : t('profit.current')}</span>
                    )}{' '}
                    {timerPool && c.tag === timerPool.coin && <span className="badge warn">{t('profit.timerNow')}</span>}{' '}
                    {c.tag === rep?.best && c.tag !== rep?.active_coin && <span className="badge ok">{t('profit.best')}</span>}
                    {c.stale && <span className="badge warn">{t('profit.stale')}</span>}
                  </td>
                  <td data-label={t('profit.price')} className="num">
                    {c.price_usd ? usd(c.price_usd, loc, 5) : '—'}
                  </td>
                  <td data-label={t('profit.difficulty')} className="num">
                    {formatDifficulty(c.difficulty)}
                  </td>
                  <td data-label={t('profit.reward')} className="num">
                    {c.block_reward.toLocaleString(loc, { maximumSignificantDigits: 5 })}
                  </td>
                  <td data-label={t('profit.revenue')} className="num">
                    {c.revenue_usd ? usd(c.revenue_usd * 1000, loc) : `${(c.revenue_btc * 1000).toPrecision(3)} BTC`}
                  </td>
                  <td data-label={t('profit.vsCurrent')} className="num">
                    {vsCurrent(c)}
                  </td>
                  <td data-label={t('profit.pools')} className="small">
                    {c.pools.map(poolName).join(', ') || '—'}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {current && current.revenue_usd > 0 && hashrateTHs > 0 && (
        <p className="small muted">
          {t('profit.farm', { revenue: usd(current.revenue_usd * hashrateTHs, loc, 3), coin: current.tag })}
        </p>
      )}
      <p className="small muted">
        {t('profit.source')}{' '}
        <a href="https://whattomine.com" target="_blank" rel="noreferrer">
          WhatToMine
        </a>
      </p>
    </div>
  )
}
