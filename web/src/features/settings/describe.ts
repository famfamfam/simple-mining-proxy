import type { TFunction } from 'i18next'
import type { SettingMeta, SettingValue } from '../../api/types'
import { parseGoDuration } from '../../lib/duration'
import { formatDuration, formatInt } from '../../lib/format'

/** The label of an enum option: translated when the UI has a text for it. */
export function optionLabel(meta: SettingMeta, option: string, t: TFunction): string {
  return t(`settings.items.${meta.key}.options.${option}`, { defaultValue: option })
}

/** A value as the operator reads it: "2 min", "5,000 KiB", "0 (off)". */
export function humanValue(meta: SettingMeta, v: SettingValue, t: TFunction, loc: string): string {
  if (meta.type === 'enum') return optionLabel(meta, String(v), t)
  if (meta.type === 'duration') return formatDuration(parseGoDuration(String(v)), t)
  if (meta.type === 'int') {
    if (meta.key === 'max_conn_per_ip' && v === 0) return t('settings.off')
    return formatInt(Number(v), loc) + (meta.unit ? ` ${meta.unit}` : '')
  }
  return v === '' ? t('settings.emptyValue') : String(v)
}

/** The allowed values, from the limits the server sends plus a note. */
export function rangeText(meta: SettingMeta, t: TFunction, loc: string): string {
  let range = ''
  if (meta.type === 'enum') range = (meta.options ?? []).map((o) => optionLabel(meta, o, t)).join(', ')
  else if (meta.type === 'string') range = t('settings.length', { min: meta.min_len ?? 0, max: meta.max_len })
  else if (meta.min !== undefined && meta.max !== undefined)
    range = `${humanValue(meta, meta.min, t, loc)} – ${humanValue(meta, meta.max, t, loc)}`
  const noteKey = `settings.items.${meta.key}.note`
  return t(noteKey, { defaultValue: '' }) ? `${range}, ${t(noteKey)}` : range
}

/** Keys of server errors that only say "out of the allowed range". */
export const rangeErrors = new Set(['setting_range', 'setting_option', 'setting_length'])
