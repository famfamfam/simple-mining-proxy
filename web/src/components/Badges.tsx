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

/** The pool's role, plus "solo" for a solo pool, whatever its role. */
export function RoleBadge({ pool }: { pool: Pool }) {
  const { t } = useTranslation()
  let role = null
  if (pool.role === 'active') role = <span className="badge accent">{t('role.active')}</span>
  else if (pool.role === 'fallback') role = <span className="badge">{t('role.fallback', { n: pool.fallback_position })}</span>
  const solo = pool.solo && <SoloBadge />
  if (!role && !solo) return <span className="muted">—</span>
  return (
    <>
      {role} {solo}
    </>
  )
}

export function SoloBadge() {
  const { t } = useTranslation()
  return (
    <span className="badge solo" title={t('role.soloHint')}>
      {t('role.solo')}
    </span>
  )
}
