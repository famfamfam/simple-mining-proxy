import { readdirSync, readFileSync, statSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'
import en from './locales/en.json'
import ru from './locales/ru.json'

type Tree = { [key: string]: string | Tree }

const pluralSuffix = /_(zero|one|two|few|many|other)$/

function flatten(tree: Tree, prefix = ''): Map<string, string> {
  const out = new Map<string, string>()
  for (const [k, v] of Object.entries(tree)) {
    const key = prefix ? `${prefix}.${k}` : k
    if (typeof v === 'string') out.set(key, v)
    else for (const [k2, v2] of flatten(v, key)) out.set(k2, v2)
  }
  return out
}

const enKeys = flatten(en)
const ruKeys = flatten(ru)
const base = (keys: Map<string, string>) => new Set([...keys.keys()].map((k) => k.replace(pluralSuffix, '')))

const placeholders = (s: string) => [...s.matchAll(/\{\{(\w+)\}\}/g)].map((m) => m[1]).sort()

describe('locales', () => {
  it('have the same keys', () => {
    expect([...base(ruKeys)].sort()).toEqual([...base(enKeys)].sort())
  })

  it('use the same placeholders', () => {
    for (const [key, text] of enKeys) {
      const b = key.replace(pluralSuffix, '')
      const ruText = ruKeys.get(key) ?? ruKeys.get(`${b}_many`) ?? ''
      expect(placeholders(ruText), key).toEqual(placeholders(text))
    }
  })

  it('give Russian plurals every form', () => {
    for (const key of enKeys.keys()) {
      if (!key.endsWith('_one')) continue
      const b = key.slice(0, -'_one'.length)
      for (const form of ['one', 'few', 'many']) expect(ruKeys.has(`${b}_${form}`), `${b}_${form}`).toBe(true)
    }
  })
})

// The server sends message keys (internal/apierr) and setting keys; every
// one of them needs a translation.
describe('server keys', () => {
  const root = join(import.meta.dirname, '../../../internal')
  const goFiles: string[] = []
  const walk = (dir: string) => {
    for (const name of readdirSync(dir)) {
      const path = join(dir, name)
      if (statSync(path).isDirectory()) walk(path)
      else if (name.endsWith('.go') && !name.endsWith('_test.go')) goFiles.push(path)
    }
  }
  walk(root)
  const source = goFiles.map((f) => readFileSync(f, 'utf8')).join('\n')

  it('translate every error key', () => {
    const keys = new Set([...source.matchAll(/apierr\.M\("([a-z_]+)"/g)].map((m) => m[1]))
    expect(keys.size).toBeGreaterThan(20)
    for (const key of keys) {
      expect(enKeys.has(`errors.${key}`), `errors.${key}`).toBe(true)
    }
  })

  it('describe every setting and group', () => {
    const registry = readFileSync(join(root, 'settings/registry.go'), 'utf8')
    const settingKeys = [...registry.matchAll(/Key: "([a-z_]+)"/g)].map((m) => m[1])
    expect(settingKeys.length).toBeGreaterThan(10)
    for (const key of settingKeys) {
      expect(enKeys.has(`settings.items.${key}.title`), key).toBe(true)
      expect(enKeys.has(`settings.items.${key}.desc`), key).toBe(true)
    }
    const groups = /var Groups = \[\]string\{([^}]*)\}/.exec(registry)?.[1] ?? ''
    for (const [, g] of groups.matchAll(/"([a-z_]+)"/g)) {
      expect(enKeys.has(`settings.groups.${g}`), g).toBe(true)
    }
  })
})
