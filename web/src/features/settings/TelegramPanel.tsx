import { useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api } from '../../api/client'
import { keys } from '../../api/queries'
import type { TelegramStatus } from '../../api/types'
import { useConfirm, useToast } from '../../context/feedback'
import { errorText } from '../../i18n/messages'

/** The Telegram bot under the alert settings: its token, its state and a test message. */
export function TelegramPanel({ status }: { status: TelegramStatus }) {
  const { t, i18n } = useTranslation()
  const qc = useQueryClient()
  const toast = useToast()
  const confirm = useConfirm()
  const [busy, setBusy] = useState(false)
  const [token, setToken] = useState('')
  const [tokenError, setTokenError] = useState('')

  const act = async (fn: () => Promise<unknown>, done: string) => {
    setBusy(true)
    try {
      await fn()
      toast(done)
    } catch (err) {
      toast(errorText(i18n, err), 'error')
    } finally {
      setBusy(false)
      void qc.invalidateQueries({ queryKey: keys.settings })
    }
  }
  const saveToken = async () => {
    setTokenError('')
    setBusy(true)
    try {
      const st = await api.setTelegramToken(token.trim())
      setToken('')
      toast(t('settings.telegram.tokenSaved', { bot: st.username ? `@${st.username}` : '' }))
    } catch (err) {
      setTokenError(errorText(i18n, err))
    } finally {
      setBusy(false)
      void qc.invalidateQueries({ queryKey: keys.settings })
    }
  }
  const removeToken = async () => {
    const ok = await confirm({
      title: t('settings.telegram.removeTitle'),
      body: t('settings.telegram.removeText'),
      confirmLabel: t('settings.telegram.remove'),
    })
    if (ok) await act(() => api.setTelegramToken(''), t('settings.telegram.tokenRemoved'))
  }

  let bot = t('settings.telegram.off')
  if (status.configured) bot = status.username ? `@${status.username}` : t('settings.telegram.connecting')
  return (
    <div className="panel">
      <div className="kv">
        <span className="muted">{t('settings.telegram.bot')}</span>
        <span className={status.username ? 'mono' : undefined}>{bot}</span>
        <label className="muted" htmlFor="telegram-token">
          {t('settings.telegram.token')}
        </label>
        <div>
          <form
            className="actions"
            onSubmit={(e) => {
              e.preventDefault()
              void saveToken()
            }}
          >
            <input
              id="telegram-token"
              type="password"
              autoComplete="new-password"
              spellCheck={false}
              className="grow"
              placeholder={status.configured ? t('settings.telegram.tokenReplace') : '123456789:AA…'}
              value={token}
              aria-invalid={!!tokenError}
              onChange={(e) => {
                setToken(e.target.value)
                setTokenError('')
              }}
            />
            <button type="submit" disabled={busy || !token.trim()}>
              {t('common.save')}
            </button>
            {status.configured && (
              <button type="button" disabled={busy} onClick={() => void removeToken()}>
                {t('settings.telegram.remove')}
              </button>
            )}
          </form>
          {tokenError && <div className="field-error">{tokenError}</div>}
          <div className="small muted">{t('settings.telegram.tokenHelp')}</div>
        </div>
        <span className="muted">{t('settings.telegram.chats')}</span>
        <span>{status.chats || t('settings.telegram.noChats')}</span>
        {status.error && (
          <>
            <span className="muted">{t('settings.telegram.error')}</span>
            <span className="field-error wrap">{status.error}</span>
          </>
        )}
      </div>
      <p className="small muted">{t(status.configured ? 'settings.telegram.help' : 'settings.telegram.setup')}</p>
      <div className="toolbar">
        <button
          type="button"
          disabled={busy || !status.configured || !status.chats}
          onClick={() => void act(() => api.testTelegram(), t('settings.telegram.sent'))}
        >
          {t('settings.telegram.test')}
        </button>
      </div>
    </div>
  )
}
