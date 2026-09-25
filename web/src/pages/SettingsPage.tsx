import { useQueryClient } from '@tanstack/react-query'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { ApiError, api } from '../api/client'
import { keys, useSettings } from '../api/queries'
import type { Msg, ServerInfo, SettingMeta, SettingsPayload, SettingValue } from '../api/types'
import { QueryState } from '../components/Panel'
import { useConfirm, useToast } from '../context/feedback'
import { humanValue, rangeErrors, rangeText } from '../features/settings/describe'
import { SettingControl } from '../features/settings/SettingControl'
import { setLeaveGuard } from '../hooks/useHashRoute'
import { locale } from '../i18n'
import { errorText, translateMsg } from '../i18n/messages'
import { parseGoDuration } from '../lib/duration'
import { formatDate, formatDuration } from '../lib/format'

type Draft = Record<string, SettingValue>

interface RowProps {
  meta: SettingMeta
  value: SettingValue
  error?: string
  onChange: (v: SettingValue) => void
}

function SettingRow({ meta, value, error, onChange }: RowProps) {
  const { t } = useTranslation()
  const loc = locale()
  const modified = value !== meta.default
  const item = `settings.items.${meta.key}`
  return (
    <div className="setting">
      <div>
        <div className="title">
          {t(`${item}.title`, { defaultValue: meta.key })}{' '}
          {modified && (
            <span className="dot" title={t('settings.modified')}>
              ●
            </span>
          )}
        </div>
        <div className="key mono">{meta.key}</div>
      </div>
      <div className="control">
        <SettingControl meta={meta} value={value} invalid={!!error} onChange={onChange} />
        <button
          type="button"
          className="small"
          title={t('settings.resetOne')}
          aria-label={t('settings.resetOne')}
          disabled={!modified}
          onClick={() => onChange(meta.default)}
        >
          ↺
        </button>
        {error && <div className="field-error full">{error}</div>}
      </div>
      <div className="desc">{t(`${item}.desc`, { defaultValue: '' })}</div>
      <div className="meta">
        {t('settings.default', { value: humanValue(meta, meta.default, t, loc) })} ·{' '}
        {t('settings.allowed', { value: rangeText(meta, t, loc) })} ·{' '}
        {t('settings.applies', { value: t(`settings.appliesTo.${meta.applies}`) })}
      </div>
    </div>
  )
}

function ServerPanel({ server }: { server: ServerInfo }) {
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

function SettingsForm({ data }: { data: SettingsPayload }) {
  const { t, i18n } = useTranslation()
  const qc = useQueryClient()
  const toast = useToast()
  const confirm = useConfirm()
  const [draft, setDraft] = useState<Draft>({})
  const [errors, setErrors] = useState<Record<string, Msg>>({})
  const [saving, setSaving] = useState(false)

  const current = (m: SettingMeta) => (m.key in draft ? draft[m.key]! : m.value)
  const dirty = Object.keys(draft).length
  const modified = data.settings.filter((m) => current(m) !== m.default).length

  // Unsaved edits: ask before leaving the screen or the page.
  useEffect(() => {
    if (!dirty) return
    setLeaveGuard(() => window.confirm(t('settings.unsavedLeave')))
    const onUnload = (e: BeforeUnloadEvent) => e.preventDefault()
    window.addEventListener('beforeunload', onUnload)
    return () => {
      setLeaveGuard(null)
      window.removeEventListener('beforeunload', onUnload)
    }
  }, [dirty, t])

  const change = (m: SettingMeta, v: SettingValue) =>
    setDraft((d) => {
      const next = { ...d }
      if (v === m.value) delete next[m.key]
      else next[m.key] = v
      return next
    })

  const resetAll = () => {
    const next: Draft = {}
    for (const m of data.settings) if (m.value !== m.default) next[m.key] = m.default
    setDraft(next)
  }

  const errorFor = (m: SettingMeta) => {
    const msg = errors[m.key]
    if (!msg) return undefined
    if (rangeErrors.has(msg.key)) return t('settings.outOfRange', { range: rangeText(m, t, locale()) })
    return translateMsg(i18n, msg)
  }

  const save = async () => {
    const body: Record<string, SettingValue | null> = {}
    for (const m of data.settings) {
      // Saving the default removes the override, so a better default in a
      // later version applies.
      if (m.key in draft) body[m.key] = draft[m.key] === m.default ? null : draft[m.key]!
    }
    setSaving(true)
    try {
      const r = await api.saveSettings(body)
      qc.setQueryData<SettingsPayload>(keys.settings, r)
      setDraft({})
      setErrors({})
      const titles = r.changes.map((c) => `${t(`settings.items.${c.key}.title`)} ${c.old} → ${c.new}`)
      toast(titles.length ? t('settings.saved', { changes: titles.join('; ') }) : t('settings.noChanges'))
      if (r.reconnect_suggested) {
        const ok = await confirm({
          title: t('settings.applyTitle'),
          body: t('settings.applyText', { count: r.sessions }),
          confirmLabel: t('dashboard.reconnectAll'),
        })
        if (ok) {
          const d = await api.reconnectAll()
          toast(t('pools.reconnecting', { count: d.sessions, window: formatDuration(parseGoDuration(d.window), t) }))
        }
      }
    } catch (err) {
      if (err instanceof ApiError && Object.keys(err.fields).length) setErrors(err.fields)
      toast(errorText(i18n, err), 'error')
    } finally {
      setSaving(false)
    }
  }

  return (
    <>
      {data.groups.map((g) => {
        const items = data.settings.filter((m) => m.group === g)
        if (!items.length) return null
        return (
          <section key={g}>
            <h2>{t(`settings.groups.${g}`, { defaultValue: g })}</h2>
            <div className="panel">
              {items.map((m) => (
                <SettingRow key={m.key} meta={m} value={current(m)} error={errorFor(m)} onChange={(v) => change(m, v)} />
              ))}
            </div>
          </section>
        )
      })}
      <h2>{t('settings.server.title')}</h2>
      <ServerPanel server={data.server} />
      <div className="savebar">
        <span className="muted grow">
          {t('settings.modifiedCount', { count: modified })}
          {dirty > 0 && <b> · {t('settings.unsavedCount', { count: dirty })}</b>}
        </span>
        <button type="button" disabled={!modified} onClick={resetAll}>
          {t('settings.resetAll')}
        </button>
        <button
          type="button"
          disabled={!dirty}
          onClick={() => {
            setDraft({})
            setErrors({})
          }}
        >
          {t('settings.discard')}
        </button>
        <button type="button" className="primary" disabled={!dirty || saving} onClick={() => void save()}>
          {t('common.save')}
        </button>
      </div>
    </>
  )
}

export function SettingsPage() {
  const { t } = useTranslation()
  const settings = useSettings()
  return (
    <>
      <h2>{t('settings.title')}</h2>
      <p className="small muted">{t('settings.help')}</p>
      <QueryState pending={settings.isPending} error={settings.error}>
        {settings.data && <SettingsForm data={settings.data} />}
      </QueryState>
    </>
  )
}
