import type { TFunction } from 'i18next'
import type { SettingMeta, SettingValue, TelegramStatus } from '../../api/types'
import { humanValue } from './describe'

/** Groups most operators never need: under "Advanced", collapsed at first. */
export const advancedGroups: ReadonlySet<string> = new Set(['logins', 'timeouts', 'limits', 'tls', 'history', 'logging'])

/** The read-only block of what .env sets, collapsed like an advanced group. */
export const serverGroup = 'server'

interface SummaryInput {
  group: string
  value: (key: string) => SettingValue | undefined
  meta: (key: string) => SettingMeta | undefined
  telegram: TelegramStatus
  t: TFunction
  loc: string
}

/** One line about a group for its header, from the values being edited: "Advise, every 24 h". */
export function groupSummary({ group, value, meta, telegram, t, loc }: SummaryInput): string {
  const show = (key: string) => {
    const m = meta(key)
    const v = value(key)
    return m && v !== undefined ? humanValue(m, v, t, loc) : ''
  }
  switch (group) {
    case 'switching':
      return t('settings.summary.switching', { drain: show('switch_drain'), failback: show('failback_delay') })
    case 'profit':
      return value('profit_switch') === 'off'
        ? show('profit_switch')
        : t('settings.summary.profit', { mode: show('profit_switch'), every: show('profit_interval') })
    case 'timed':
      return value('timed_switch') === 'off'
        ? show('timed_switch')
        : t('settings.summary.timed', { duration: show('timed_duration'), period: show('timed_period') })
    case 'hunt':
      return show('hunt_switch')
    case 'alerts': {
      let bot = t('settings.summary.noBot')
      if (telegram.configured) bot = telegram.username ? `@${telegram.username}` : t('settings.telegram.connecting')
      return t('settings.summary.alerts', { after: show('offline_after'), bot })
    }
    case 'history':
      return t('settings.summary.history', { detail: show('history_detail_retention'), total: show('history_retention') })
  }
  return ''
}

/** Whether a setting matches the search text q (lower case): its title, key or description. */
export function matches(m: SettingMeta, q: string, t: TFunction): boolean {
  const item = `settings.items.${m.key}`
  return [t(`${item}.title`, { defaultValue: m.key }), m.key, t(`${item}.desc`, { defaultValue: '' })].some((s) =>
    s.toLowerCase().includes(q),
  )
}
