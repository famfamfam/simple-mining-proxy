import { useTranslation } from 'react-i18next'
import type { WorkerSummary } from '../../api/types'
import { locale } from '../../i18n'
import { formatAgo, formatHashrateHs, formatInt, formatPercent } from '../../lib/format'

interface WorkersTableProps {
  workers: WorkerSummary[]
  onOpen: (name: string) => void
}

/** ASIC logins over a period, with a link to each one's history. */
export function WorkersTable({ workers, onOpen }: WorkersTableProps) {
  const { t } = useTranslation()
  const loc = locale()
  return (
    <div className="panel table-wrap">
      <table className="responsive">
        <thead>
          <tr>
            <th>{t('miners.worker')}</th>
            <th className="num">{t('minerStats.hashrate')}</th>
            <th className="num">{t('miners.accepted')}</th>
            <th className="num">{t('minerStats.rejectedPct')}</th>
            <th className="num">{t('minerStats.online')}</th>
            <th>{t('minerStats.lastSeen')}</th>
          </tr>
        </thead>
        <tbody>
          {workers.map((w) => (
            <tr key={w.name}>
              <td className="primary mono">
                <button type="button" className="link" onClick={() => onOpen(w.name)}>
                  {w.name}
                </button>
              </td>
              <td data-label={t('minerStats.hashrate')} className="num">
                {formatHashrateHs(w.hashrate)}
              </td>
              <td data-label={t('miners.accepted')} className="num">
                {formatInt(w.accepted, loc)}
              </td>
              <td data-label={t('minerStats.rejectedPct')} className="num">
                {formatPercent(w.rejected, w.accepted + w.rejected)}
              </td>
              <td data-label={t('minerStats.online')} className="num">
                {formatPercent(w.online, 1)}
              </td>
              <td data-label={t('minerStats.lastSeen')}>{formatAgo(new Date(w.last_seen * 1000).toISOString(), t)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}
