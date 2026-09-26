import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api } from '../../api/client'
import { useRefreshLive, useTimed } from '../../api/queries'
import type { Pool } from '../../api/types'
import { useConfirm, useToast } from '../../context/feedback'
import { locale } from '../../i18n'
import { errorText } from '../../i18n/messages'
import { parseGoDuration } from '../../lib/duration'
import { formatDuration, formatFactor, formatTime } from '../../lib/format'
import { poolNameIn } from '../../lib/pools'

/** Timed switching now, with "Start now"; shown only when it is on. */
export function TimedPanel({ pools }: { pools: Pool[] }) {
  const { t, i18n } = useTranslation()
  const loc = locale()
  const timed = useTimed()
  const toast = useToast()
  const confirm = useConfirm()
  const refresh = useRefreshLive()
  const [busy, setBusy] = useState(false)
  const s = timed.data
  if (!s || s.mode === 'off') return null
  const name = (id: string) => poolNameIn(pools, id)

  // Restarts the schedule from now: the farm goes to the timer pool without
  // waiting for the next period.
  const startNow = async () => {
    const vars = {
      pool: name(s.target),
      length: formatDuration(parseGoDuration(s.duration), t),
      every: formatDuration(parseGoDuration(s.period), t),
    }
    const ok = await confirm({
      title: t('timed.startTitle', vars),
      body: t(s.home ? 'timed.restartText' : 'timed.startText', vars),
      confirmLabel: t('timed.startConfirm'),
    })
    if (!ok) return
    setBusy(true)
    try {
      await api.timedStart()
      toast(t('timed.started', vars))
    } catch (err) {
      toast(errorText(i18n, err), 'error')
    } finally {
      setBusy(false)
      refresh()
    }
  }

  let text: string
  if (!s.target) {
    text = t('timed.noTarget')
  } else if (s.home && s.until) {
    text = t('timed.now', { pool: name(s.target), until: formatTime(s.until, loc), home: name(s.home) })
  } else if (s.waiting) {
    text = t('timed.waiting', {
      pool: name(s.target),
      factor: formatFactor(s.hardness, loc),
      left: formatDuration(s.left, t),
    })
  } else {
    text = t('timed.plan', {
      pool: name(s.target),
      length: formatDuration(parseGoDuration(s.duration), t),
      every: formatDuration(parseGoDuration(s.period), t),
      next: formatTime(s.next, loc),
    })
  }
  return (
    <div className="panel timed">
      <div className="split">
        <span className="grow">
          <span className={s.home ? 'badge accent' : 'badge'}>{t('timed.badge')}</span> {text}
        </span>
        {s.target && (
          <button type="button" className="small" disabled={busy} title={t('timed.startHint')} onClick={() => void startNow()}>
            {t('timed.startNow')}
          </button>
        )}
      </div>
      {s.error && <div className="warnbox">{t('timed.failed', { error: s.error })}</div>}
    </div>
  )
}
