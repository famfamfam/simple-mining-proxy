import { useTranslation } from 'react-i18next'
import { useTimed } from '../../api/queries'
import type { Pool } from '../../api/types'
import { locale } from '../../i18n'
import { formatDuration, formatFactor, formatTime } from '../../lib/format'
import { poolNameIn } from '../../lib/pools'

/** Block hunting on eCash now and over the last day; shown only when it is on. */
export function HuntPanel({ pools }: { pools: Pool[] }) {
  const { t } = useTranslation()
  const loc = locale()
  const timed = useTimed()
  const h = timed.data?.hunt
  if (!h || h.mode === 'off') return null
  const name = (id: string) => poolNameIn(pools, id)
  const factor = formatFactor(h.hardness, loc)

  let text: string
  if (!h.target) text = t('hunt.noTarget')
  else if (h.since && h.home) text = t('hunt.now', { pool: name(h.target), since: formatTime(h.since, loc), home: name(h.home) })
  else if (!h.live) text = t('hunt.blind')
  else if (h.hold) text = t('hunt.hold')
  // A failed switch pauses hunting for a few minutes: say so instead of
  // "ready to hunt" right above the failure box, which would contradict it.
  else if (h.retry) text = t('hunt.retrying', { at: formatTime(h.retry, loc) })
  else if (timed.data?.home) text = t('hunt.timer')
  else if (h.hardness > 1.2) text = t('hunt.waiting', { factor })
  else text = t('hunt.ready', { pool: name(h.target) })

  return (
    <div className="panel timed">
      <span className={h.since ? 'badge solo' : 'badge'}>{t('hunt.badge')}</span> {text}
      {h.stints > 0 && (
        <div className="small muted">
          {t('hunt.day', { time: formatDuration(h.seconds, t), n: h.stints })}
        </div>
      )}
      {h.error && <div className="warnbox">{t('hunt.failed', { error: h.error })}</div>}
    </div>
  )
}
