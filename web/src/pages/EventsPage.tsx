import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useEvents } from '../api/queries'
import type { EventItem } from '../api/types'
import { QueryState } from '../components/Panel'
import { locale } from '../i18n'
import { formatDateTime } from '../lib/format'

/** Event texts come from the server in English, like the container log. */
export function EventList({ events }: { events: EventItem[] }) {
  const { t } = useTranslation()
  const loc = locale()
  if (!events.length) return <div className="panel muted">{t('events.empty')}</div>
  return (
    <div className="panel events">
      {events.map((e) => (
        <div key={e.seq} className="ev">
          <span className="muted small">{formatDateTime(e.time, loc)}</span>
          <span className={`small lvl-${e.level}`}>{e.level}</span>
          <span className="wrap">{e.message}</span>
        </div>
      ))}
    </div>
  )
}

type Level = 'all' | 'warn' | 'error'

const shown: Record<Level, (e: EventItem) => boolean> = {
  all: () => true,
  warn: (e) => e.level !== 'info',
  error: (e) => e.level === 'error',
}

export function EventsPage() {
  const { t } = useTranslation()
  const events = useEvents(500)
  const [level, setLevel] = useState<Level>('all')
  const filters: [Level, string][] = [
    ['all', t('events.all')],
    ['warn', t('events.warnings')],
    ['error', t('events.errors')],
  ]
  return (
    <>
      <h2>{t('events.title')}</h2>
      <p className="small muted">{t('events.help')}</p>
      <div className="toolbar" role="group">
        {filters.map(([value, label]) => (
          <button
            key={value}
            type="button"
            className={level === value ? 'chip active' : 'chip'}
            aria-pressed={level === value}
            onClick={() => setLevel(value)}
          >
            {label}
          </button>
        ))}
      </div>
      <QueryState pending={events.isPending} error={events.error}>
        <EventList events={(events.data ?? []).filter(shown[level])} />
      </QueryState>
    </>
  )
}
