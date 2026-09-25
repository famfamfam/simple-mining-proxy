import { useTranslation } from 'react-i18next'
import { ApiError, api } from '../../api/client'
import { useRefreshLive } from '../../api/queries'
import type { ActivateResult, Pool } from '../../api/types'
import { useConfirm, useToast } from '../../context/feedback'
import { errorText } from '../../i18n/messages'
import { parseGoDuration } from '../../lib/duration'
import { formatDuration } from '../../lib/format'

/** Switch, delete and reconnect with their confirmations and results. */
export function usePoolActions(minersTotal: number) {
  const { t, i18n } = useTranslation()
  const toast = useToast()
  const confirm = useConfirm()
  const refresh = useRefreshLive()

  const fail = (err: unknown) => toast(errorText(i18n, err), 'error')
  const drainWindow = (w: string) => formatDuration(parseGoDuration(w), t)

  const switched = (p: Pool, r: ActivateResult) =>
    toast(t('pools.switched', { pool: p.name, count: r.reconnecting, window: drainWindow(r.window) }))

  async function switchPool(p: Pool) {
    const ok = await confirm({
      title: t('pools.switchTitle'),
      body: t('pools.switchText', { count: minersTotal, pool: p.name }),
      confirmLabel: t('pools.switchConfirm'),
    })
    if (!ok) return
    try {
      switched(p, await api.activatePool(p.id, false))
    } catch (err) {
      if (!(err instanceof ApiError) || err.status !== 422) {
        fail(err)
        return
      }
      const force = await confirm({
        title: t('pools.checkFailedTitle'),
        body: (
          <>
            <p className="mono small">{errorText(i18n, err)}</p>
            <p>{t('pools.switchAnyway')}</p>
          </>
        ),
        confirmLabel: t('pools.switchForce'),
        danger: true,
      })
      if (force) {
        try {
          switched(p, await api.activatePool(p.id, true))
        } catch (err2) {
          fail(err2)
        }
      }
    } finally {
      refresh()
    }
  }

  async function deletePool(p: Pool) {
    const body = [
      t('pools.deleteText', { pool: p.name }),
      p.sessions ? t('pools.deleteSessions', { count: p.sessions }) : '',
    ].join(' ')
    if (!(await confirm({ title: t('pools.deleteTitle'), body, confirmLabel: t('pools.delete'), danger: true }))) return
    try {
      await api.deletePool(p.id)
      toast(t('pools.deleted'))
    } catch (err) {
      fail(err)
    }
    refresh()
  }

  async function reconnectAll() {
    const ok = await confirm({
      title: t('pools.reconnectTitle'),
      body: t('pools.reconnectText', { count: minersTotal }),
      confirmLabel: t('pools.reconnectConfirm'),
    })
    if (!ok) return
    try {
      const r = await api.reconnectAll()
      toast(t('pools.reconnecting', { count: r.sessions, window: drainWindow(r.window) }))
    } catch (err) {
      fail(err)
    }
    refresh()
  }

  return { switchPool, deletePool, reconnectAll }
}
