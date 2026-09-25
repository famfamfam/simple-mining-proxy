import { useTranslation } from 'react-i18next'
import { useWorkerHistory } from '../../api/queries'
import { Modal } from '../../components/Modal'
import { QueryState, StatCard } from '../../components/Panel'
import { RangePicker } from '../../components/RangePicker'
import { TimeChart } from '../../components/TimeChart'
import { useRange } from '../../hooks/useRange'
import { locale } from '../../i18n'
import { formatHashrateHs, formatInt, formatPercent } from '../../lib/format'
import { rejectRate } from '../stats/derive'

// No 1h: a miner has a point per ten minutes. No 1y: miners are kept for
// 90 days by default.
const ranges = ['6h', '24h', '7d', '30d', '90d'] as const

/** The history of one ASIC login: hashrate, time online and rejects. */
export function MinerDialog({ name, onClose }: { name: string; onClose: () => void }) {
  const { t } = useTranslation()
  const loc = locale()
  const { range, seconds, setRange } = useRange('miner.range', ranges, '24h')
  const history = useWorkerHistory(name, seconds)
  const s = history.data?.series
  const sum = s?.summary

  return (
    <Modal
      wide
      title={t('minerStats.title', { name })}
      onClose={onClose}
      footer={
        <button type="button" className="primary" onClick={onClose}>
          {t('common.close')}
        </button>
      }
    >
      <RangePicker keys={ranges} value={range} onChange={setRange} />
      <QueryState pending={history.isPending} error={history.error}>
        {s && sum && !sum.last_seen && <p className="muted">{t('minerStats.noData')}</p>}
        {s && sum && sum.last_seen > 0 && (
          <div className={history.isPlaceholderData ? 'stats refetching' : 'stats'}>
            <div className="cards">
              <StatCard label={t('minerStats.hashrate')} value={formatHashrateHs(sum.hashrate)} sub={t('minerStats.whileOnline')} />
              <StatCard label={t('minerStats.online')} value={formatPercent(sum.online, 1)} />
              <StatCard
                label={t('stats.accepted')}
                value={formatInt(sum.accepted, loc)}
                sub={t('stats.rejectedSub', { pct: formatPercent(sum.rejected, sum.accepted + sum.rejected) })}
              />
            </div>
            <TimeChart
              title={t('stats.hashrate')}
              time={s.time}
              series={[{ label: t('stats.hashrate'), values: s.hashrate, slot: 1, fill: true }]}
              format={formatHashrateHs}
              height={200}
            />
            <div className="chart-pair">
              <TimeChart
                title={t('minerStats.online')}
                time={s.time}
                series={[
                  { label: t('minerStats.online'), values: s.online.map((v) => (v == null ? null : v * 100)), slot: 1, stepped: true },
                ]}
                format={(v) => `${Math.round(v)}%`}
                height={150}
              />
              <TimeChart
                title={t('stats.rejectRate')}
                time={s.time}
                series={[{ label: t('stats.rejectRate'), values: rejectRate(s), slot: 1 }]}
                format={(v) => `${v.toFixed(v < 1 ? 2 : 1)}%`}
                height={150}
              />
            </div>
          </div>
        )}
      </QueryState>
    </Modal>
  )
}
