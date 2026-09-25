import { createInstance } from 'i18next'
import { describe, expect, it } from 'vitest'
import en from './locales/en.json'
import ru from './locales/ru.json'
import { translateMsg } from './messages'

async function instance(lng: string) {
  const i = createInstance()
  await i.init({ lng, resources: { en: { translation: en }, ru: { translation: ru } }, interpolation: { escapeValue: false } })
  return i
}

describe('translateMsg', () => {
  it('translates nested messages', async () => {
    const i = await instance('ru')
    const msg = {
      key: 'pool_address',
      message: 'address 1 (x:0): port must be 1–65535',
      params: { n: 1, address: 'x:0', reason: { key: 'addr_port', message: 'port must be 1–65535' } },
    }
    expect(translateMsg(i, msg)).toBe('адрес 1 (x:0): порт от 1 до 65535')
  })

  it('falls back to the server text for unknown keys', async () => {
    const i = await instance('ru')
    expect(translateMsg(i, { key: 'from_a_newer_server', message: 'Something new' })).toBe('Something new')
  })
})
