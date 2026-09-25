import type { i18n as I18n } from 'i18next'
import { ApiError } from '../api/client'
import type { Msg } from '../api/types'

function isMsg(v: unknown): v is Msg {
  return typeof v === 'object' && v !== null && 'key' in v && 'message' in v
}

/**
 * Translates a server message: errors.<key> with its params (nested
 * messages are translated too). Unknown keys fall back to the English text
 * the server sent, so a newer server never shows raw keys.
 */
export function translateMsg(i18n: I18n, msg: Msg | undefined): string {
  if (!msg) return ''
  const key = `errors.${msg.key}`
  if (!msg.key || !i18n.exists(key)) return msg.message
  const params: Record<string, unknown> = {}
  for (const [name, value] of Object.entries(msg.params ?? {})) {
    params[name] = isMsg(value) ? translateMsg(i18n, value) : value
  }
  return i18n.t(key, params)
}

/** Text for any thrown error. */
export function errorText(i18n: I18n, err: unknown): string {
  if (err instanceof ApiError) return translateMsg(i18n, err.msg)
  return err instanceof Error ? err.message : String(err)
}
