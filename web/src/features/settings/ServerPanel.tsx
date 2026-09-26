import { useTranslation } from 'react-i18next'
import type { ServerInfo } from '../../api/types'
import { locale } from '../../i18n'
import { formatDate } from '../../lib/format'

/** What .env on the server sets: read only in the UI. */
export function ServerPanel({ server }: { server: ServerInfo }) {
  const { t } = useTranslation()
  const loc = locale()
  const cert = server.certificate
  const conn = (k: 'tcp' | 'tls') => {
    const c = server.connections[k]
    if (!c.enabled) return <span className="muted">{t('dashboard.listenerOff')}</span>
    return (
      <span className="mono wrap">
        {c.url ?? `${c.scheme}://${t('dashboard.hostPlaceholder')}:${c.port}`}{' '}
        <span className="muted">({t('settings.server.inContainer', { listen: c.listen })})</span>
      </span>
    )
  }
  return (
    <div className="panel">
      <p className="small muted">{t('settings.server.help')}</p>
      <div className="kv">
        <span className="muted">{t('settings.server.tcp')}</span>
        {conn('tcp')}
        <span className="muted">{t('settings.server.tls')}</span>
        {conn('tls')}
        <span className="muted">{t('settings.server.certificate')}</span>
        {cert ? (
          <span>
            {cert.type === 'self-signed' ? t('settings.server.certSelfSigned') : t('settings.server.certLoaded')} ·{' '}
            {t('settings.server.certUntil', { date: formatDate(cert.not_after, loc) })}
            {cert.dns_names?.length ? ` · ${cert.dns_names.join(', ')}` : ''}
            <div className="mono small muted wrap">SHA-256 {cert.sha256}</div>
          </span>
        ) : (
          <span className="muted">{t('settings.server.tlsOff')}</span>
        )}
        <span className="muted">{t('settings.server.admin')}</span>
        <span>
          {server.admin_username} ·{' '}
          {t('settings.server.apiToken', {
            state: server.api_token_set ? t('settings.server.tokenSet') : t('settings.server.tokenNotSet'),
          })}
        </span>
        <span className="muted">{t('settings.server.adminListen')}</span>
        <span className="mono">{server.admin_listen}</span>
        <span className="muted">{t('settings.server.dataDir')}</span>
        <span className="mono">{server.data_dir}</span>
        <span className="muted">{t('settings.server.logFormat')}</span>
        <span className="mono">{server.log_format}</span>
      </div>
    </div>
  )
}
