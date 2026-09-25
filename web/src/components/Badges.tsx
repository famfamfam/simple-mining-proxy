import { useTranslation } from 'react-i18next'
import type { Health, Mode, Pool } from '../api/types'

export function HealthBadge({ health }: { health: Health }) {
  const cls = health === 'UP' ? 'ok' : health === 'DOWN' ? 'bad' : ''
  return <span className={`badge ${cls}`}>{health}</span>
}

const modeClass: Record<Mode, string> = { NORMAL: 'ok', DEGRADED: 'warn', DOWN: 'bad', SWITCHING: 'accent' }

export function ModeBadge({ mode }: { mode: Mode }) {
  const { t } = useTranslation()
  return <span className={`badge ${modeClass[mode]}`}>{t(`mode.${mode}`)}</span>
}

export function RoleBadge({ pool }: { pool: Pool }) {
  const { t } = useTranslation()
  if (pool.role === 'active') return <span className="badge accent">{t('role.active')}</span>
  if (pool.role === 'fallback') return <span className="badge">{t('role.fallback', { n: pool.fallback_position })}</span>
  return <span className="muted">—</span>
}
