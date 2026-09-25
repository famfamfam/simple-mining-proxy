import { useTranslation } from 'react-i18next'
import type { Pool } from '../../api/types'
import { HealthBadge, RoleBadge } from '../../components/Badges'
import { locale } from '../../i18n'
import { hostPort } from '../../lib/addresses'
import { formatHashrate, formatInt, formatMs } from '../../lib/format'

interface PoolsTableProps {
  pools: Pool[]
  onSwitch: (p: Pool) => void
  onTest: (p: Pool) => void
  onEdit: (p: Pool) => void
  onDelete: (p: Pool) => void
}

/** Addresses of a pool: ★ marks where new sessions go first. */
function AddressList({ pool }: { pool: Pool }) {
  const { t } = useTranslation()
  const multi = pool.addresses.length > 1
  return (
    <>
      {pool.addresses.map((a) => (
        <div
          key={hostPort(a)}
          className={a.health === 'DOWN' ? 'small mono addr down' : 'small mono addr'}
          title={a.last_error || (a.preferred && multi ? t('pools.fastest') : undefined)}
        >
          {multi && <span className="star">{a.preferred ? '★' : ''}</span>}
          <span className="host">{`${pool.tls ? 'tls' : 'tcp'}://${hostPort(a)}`}</span>
          {a.latency_ms != null && <span className="muted"> · {formatMs(a.latency_ms, t)}</span>}
          {a.health === 'DOWN' && <span className="lvl-error"> · DOWN</span>}
        </div>
      ))}
    </>
  )
}

export function PoolsTable({ pools, onSwitch, onTest, onEdit, onDelete }: PoolsTableProps) {
  const { t } = useTranslation()
  const loc = locale()
  return (
    <div className="panel table-wrap">
      <table className="responsive">
        <thead>
          <tr>
            <th>{t('pools.pool')}</th>
            <th>{t('pools.coin')}</th>
            <th>{t('pools.role')}</th>
            <th>{t('pools.state')}</th>
            <th className="num">{t('pools.sessions')}</th>
            <th className="num">{t('pools.hashrate')}</th>
            <th className="num">{t('pools.shares')}</th>
            <th>
              <span className="sr-only">{t('pools.actions')}</span>
            </th>
          </tr>
        </thead>
        <tbody>
          {pools.map((p) => (
            <tr key={p.id}>
              <td className="primary">
                <b>{p.name}</b>{' '}
                {p.profit_switch && (
                  <span className="badge" title={t('pools.profitSwitchHint')}>
                    {t('pools.profitSwitch')}
                  </span>
                )}{' '}
                {p.timed_target && (
                  <span className="badge" title={t('pools.timedTargetHint')}>
                    {t('pools.timedTarget')}
                  </span>
                )}
                <AddressList pool={p} />
              </td>
              <td data-label={t('pools.coin')}>{p.coin || '—'}</td>
              <td data-label={t('pools.role')}>
                <RoleBadge pool={p} />
              </td>
              <td data-label={t('pools.state')} title={p.last_error ?? undefined}>
                <HealthBadge health={p.health} />
                {p.last_error && <div className="small muted clip">{p.last_error}</div>}
              </td>
              <td data-label={t('pools.sessions')} className="num">
                {formatInt(p.sessions, loc)}
              </td>
              <td data-label={t('pools.hashrate')} className="num">
                {formatHashrate(p.hashrate_ths)}
              </td>
              <td data-label={t('pools.shares')} className="num">
                {formatInt(p.accepted, loc)} / {formatInt(p.rejected, loc)}
              </td>
              <td className="actions-cell">
                <div className="actions">
                  {p.role !== 'active' && (
                    <button type="button" className="small primary" onClick={() => onSwitch(p)}>
                      {t('pools.switch')}
                    </button>
                  )}
                  <button type="button" className="small" onClick={() => onTest(p)}>
                    {t('pools.test')}
                  </button>
                  <button type="button" className="small" onClick={() => onEdit(p)}>
                    {t('pools.edit')}
                  </button>
                  {p.role !== 'active' && (
                    <button type="button" className="small danger" onClick={() => onDelete(p)}>
                      {t('pools.delete')}
                    </button>
                  )}
                </div>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}
