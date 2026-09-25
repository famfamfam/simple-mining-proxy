import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api } from '../../api/client'
import { useRefreshLive } from '../../api/queries'
import type { Pool } from '../../api/types'
import { HealthBadge } from '../../components/Badges'
import { Modal } from '../../components/Modal'
import { useToast } from '../../context/feedback'
import { errorText } from '../../i18n/messages'

/** Orders the fallback pools: new sessions try them top to bottom. */
export function FallbackEditor({ pools, onClose }: { pools: Pool[]; onClose: () => void }) {
  const { t, i18n } = useTranslation()
  const toast = useToast()
  const refresh = useRefreshLive()
  const candidates = pools.filter((p) => p.role !== 'active')
  const [order, setOrder] = useState(() =>
    candidates
      .filter((p) => p.role === 'fallback')
      .sort((a, b) => (a.fallback_position ?? 0) - (b.fallback_position ?? 0))
      .map((p) => p.id),
  )
  const byId = new Map(candidates.map((p) => [p.id, p]))
  const rest = candidates.filter((p) => !order.includes(p.id))

  const move = (i: number, delta: number) =>
    setOrder((o) => {
      const next = [...o]
      const [id] = next.splice(i, 1)
      if (id) next.splice(i + delta, 0, id)
      return next
    })

  const save = async () => {
    try {
      await api.setFallback(order)
      toast(t('fallback.saved'))
      refresh()
      onClose()
    } catch (err) {
      toast(errorText(i18n, err), 'error')
    }
  }

  return (
    <Modal
      title={t('fallback.title')}
      onClose={onClose}
      footer={
        <>
          <button type="button" onClick={onClose}>
            {t('common.cancel')}
          </button>
          <button type="button" className="primary" onClick={() => void save()}>
            {t('common.save')}
          </button>
        </>
      }
    >
      <p className="small muted">{t('fallback.help')}</p>
      {order.length === 0 && <p className="muted">{t('fallback.empty')}</p>}
      {order.map((id, i) => {
        const p = byId.get(id)
        if (!p) return null
        return (
          <div key={id} className="fb-row">
            <b>#{i + 1}</b>
            <span className="grow">
              {p.name} <span className="muted">{p.coin}</span>
            </span>
            <HealthBadge health={p.health} />
            <button type="button" className="small" aria-label={t('fallback.up')} disabled={i === 0} onClick={() => move(i, -1)}>
              ↑
            </button>
            <button
              type="button"
              className="small"
              aria-label={t('fallback.down')}
              disabled={i === order.length - 1}
              onClick={() => move(i, 1)}
            >
              ↓
            </button>
            <button type="button" className="small" onClick={() => setOrder((o) => o.filter((x) => x !== id))}>
              {t('fallback.remove')}
            </button>
          </div>
        )
      })}
      {rest.map((p) => (
        <div key={p.id} className="fb-row muted">
          <span className="grow">
            {p.name} {p.coin}
          </span>
          <HealthBadge health={p.health} />
          <button type="button" className="small" onClick={() => setOrder((o) => [...o, p.id])}>
            {t('fallback.add')}
          </button>
        </div>
      ))}
    </Modal>
  )
}
