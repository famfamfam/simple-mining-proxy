import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { errorText } from '../i18n/messages'

export function StatCard({ label, value, sub }: { label: string; value: ReactNode; sub?: ReactNode }) {
  return (
    <div className="panel card">
      <div className="label">{label}</div>
      <div className="value">{value}</div>
      <div className="sub">{sub}</div>
    </div>
  )
}

/** Loading and error states of a query, or its content. */
export function QueryState({ error, pending, children }: { error: unknown; pending: boolean; children: ReactNode }) {
  const { t, i18n } = useTranslation()
  if (pending) return <div className="muted">{t('common.loading')}</div>
  if (error) return <div className="panel field-error">{t('common.loadFailed', { error: errorText(i18n, error) })}</div>
  return children
}
