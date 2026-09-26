import { useQueryClient } from '@tanstack/react-query'
import { useEffect, useLayoutEffect, useRef, useState, type ReactNode, type RefObject } from 'react'
import { useTranslation } from 'react-i18next'
import { ApiError, api } from '../api/client'
import { keys, usePoolNames, useSettings } from '../api/queries'
import type { Msg, SettingMeta, SettingsPayload, SettingValue } from '../api/types'
import { QueryState } from '../components/Panel'
import { useConfirm, useToast } from '../context/feedback'
import { humanValue, rangeErrors, rangeText } from '../features/settings/describe'
import { advancedGroups, groupSummary, matches, serverGroup } from '../features/settings/groups'
import { ServerPanel } from '../features/settings/ServerPanel'
import { SettingControl } from '../features/settings/SettingControl'
import { TelegramPanel } from '../features/settings/TelegramPanel'
import { HuntPanel } from '../features/timed/HuntPanel'
import { TimedPanel } from '../features/timed/TimedPanel'
import { setLeaveGuard } from '../hooks/useHashRoute'
import { useLocalState } from '../hooks/useSessionState'
import { locale } from '../i18n'
import { errorText, translateMsg } from '../i18n/messages'
import { parseGoDuration } from '../lib/duration'
import { formatDuration } from '../lib/format'

type Draft = Record<string, SettingValue>

/** Whether the element's text is cut by its line clamp; checked again on resize. */
function useClipped(ref: RefObject<HTMLElement | null>, text: string) {
  const [clipped, setClipped] = useState(false)
  useLayoutEffect(() => {
    const el = ref.current
    if (!el) return
    const check = () => setClipped(el.scrollHeight > el.clientHeight + 1)
    check()
    if (typeof ResizeObserver === 'undefined') return
    const ro = new ResizeObserver(check)
    ro.observe(el)
    return () => ro.disconnect()
  }, [ref, text])
  return clipped
}

interface RowProps {
  meta: SettingMeta
  value: SettingValue
  error?: string
  onChange: (v: SettingValue) => void
}

function SettingRow({ meta, value, error, onChange }: RowProps) {
  const { t } = useTranslation()
  const loc = locale()
  // Long descriptions show two lines until opened.
  const [more, setMore] = useState(false)
  const descRef = useRef<HTMLDivElement>(null)
  const modified = value !== meta.default
  const item = `settings.items.${meta.key}`
  const desc = t(`${item}.desc`, { defaultValue: '' })
  const long = useClipped(descRef, desc) || more
  return (
    <div className="setting">
      <div>
        <div className="title">
          {t(`${item}.title`, { defaultValue: meta.key })}{' '}
          {modified && (
            <span className="dot" title={t('settings.modified')}>
              ●
            </span>
          )}
        </div>
        <div className="key mono">{meta.key}</div>
      </div>
      <div className="control">
        <SettingControl meta={meta} value={value} invalid={!!error} onChange={onChange} />
        <button
          type="button"
          className="small"
          title={t('settings.resetOne')}
          aria-label={t('settings.resetOne')}
          disabled={!modified}
          onClick={() => onChange(meta.default)}
        >
          ↺
        </button>
        {error && <div className="field-error full">{error}</div>}
      </div>
      <div ref={descRef} className={more ? 'desc' : 'desc clamp'}>
        {desc}
      </div>
      <div className="meta">
        {long && (
          <>
            <button type="button" className="link" aria-expanded={more} onClick={() => setMore(!more)}>
              {more ? t('settings.less') : t('settings.more')}
            </button>{' '}
            ·{' '}
          </>
        )}
        {t('settings.default', { value: humanValue(meta, meta.default, t, loc) })} ·{' '}
        {t('settings.allowed', { value: rangeText(meta, t, loc) })} ·{' '}
        {t('settings.applies', { value: t(`settings.appliesTo.${meta.applies}`) })}
      </div>
    </div>
  )
}

interface GroupProps {
  id: string
  title: string
  summary?: string
  open: boolean
  onToggle: () => void
  modified: number
  unsaved: number
  errors: number
  children: ReactNode
}

/** A settings group: the header opens and closes it and says what is inside. */
function Group({ id, title, summary, open, onToggle, modified, unsaved, errors, children }: GroupProps) {
  const { t } = useTranslation()
  return (
    <section className="group" id={`settings-${id}`}>
      <h3>
        <button type="button" className="group-head" aria-expanded={open} onClick={onToggle}>
          <span className="chev" aria-hidden="true">
            {open ? '▾' : '▸'}
          </span>
          <span className="name">{title}</span>
          {summary && <span className="summary">{summary}</span>}
          <span className="grow" />
          {modified > 0 && (
            <span className="count" title={t('settings.modifiedHint')}>
              ● {modified}
            </span>
          )}
          {unsaved > 0 && <span className="badge warn">{t('settings.badgeUnsaved', { count: unsaved })}</span>}
          {errors > 0 && <span className="badge bad">{t('settings.badgeErrors', { count: errors })}</span>}
        </button>
      </h3>
      {open && children}
    </section>
  )
}

function SettingsForm({ data }: { data: SettingsPayload }) {
  const { t, i18n } = useTranslation()
  const loc = locale()
  const qc = useQueryClient()
  const toast = useToast()
  const confirm = useConfirm()
  const pools = usePoolNames()
  const [draft, setDraft] = useState<Draft>({})
  const [errors, setErrors] = useState<Record<string, Msg>>({})
  const [saving, setSaving] = useState(false)
  const [opened, setOpened] = useLocalState<Record<string, boolean>>('settings.open', {})
  const [query, setQuery] = useState('')
  const q = query.trim().toLowerCase()

  const current = (m: SettingMeta) => (m.key in draft ? draft[m.key]! : m.value)
  const byKey = new Map(data.settings.map((m) => [m.key, m]))
  const dirty = Object.keys(draft).length
  const modified = data.settings.filter((m) => current(m) !== m.default).length

  // Unsaved edits: ask before leaving the screen or the page.
  useEffect(() => {
    if (!dirty) return
    setLeaveGuard(() => window.confirm(t('settings.unsavedLeave')))
    const onUnload = (e: BeforeUnloadEvent) => e.preventDefault()
    window.addEventListener('beforeunload', onUnload)
    return () => {
      setLeaveGuard(null)
      window.removeEventListener('beforeunload', onUnload)
    }
  }, [dirty, t])

  const groups = data.groups.filter((g) => data.settings.some((m) => m.group === g))
  const main = groups.filter((g) => !advancedGroups.has(g))
  const advanced = groups.filter((g) => advancedGroups.has(g))
  const all = [...main, ...advanced, serverGroup]
  const title = (g: string) => (g === serverGroup ? t('settings.server.title') : t(`settings.groups.${g}`, { defaultValue: g }))
  const isOpen = (g: string) => opened[g] ?? (!advancedGroups.has(g) && g !== serverGroup)
  const setOpen = (list: string[], open: boolean) =>
    setOpened((o) => ({ ...o, ...Object.fromEntries(list.map((g) => [g, open])) }))
  const allOpen = all.every(isOpen)

  const jump = (g: string) => {
    setQuery('')
    setOpen([g], true)
    requestAnimationFrame(() => document.getElementById(`settings-${g}`)?.scrollIntoView({ behavior: 'smooth', block: 'start' }))
  }

  const change = (m: SettingMeta, v: SettingValue) =>
    setDraft((d) => {
      const next = { ...d }
      if (v === m.value) delete next[m.key]
      else next[m.key] = v
      return next
    })

  const resetAll = () => {
    const next: Draft = {}
    for (const m of data.settings) if (m.value !== m.default) next[m.key] = m.default
    setDraft(next)
  }

  const errorFor = (m: SettingMeta) => {
    const msg = errors[m.key]
    if (!msg) return undefined
    if (rangeErrors.has(msg.key)) return t('settings.outOfRange', { range: rangeText(m, t, loc) })
    return translateMsg(i18n, msg)
  }

  const save = async () => {
    const body: Record<string, SettingValue | null> = {}
    for (const m of data.settings) {
      // Saving the default removes the override, so a better default in a
      // later version applies.
      if (m.key in draft) body[m.key] = draft[m.key] === m.default ? null : draft[m.key]!
    }
    setSaving(true)
    try {
      const r = await api.saveSettings(body)
      qc.setQueryData<SettingsPayload>(keys.settings, r)
      setDraft({})
      setErrors({})
      const titles = r.changes.map((c) => `${t(`settings.items.${c.key}.title`)} ${c.old} → ${c.new}`)
      toast(titles.length ? t('settings.saved', { changes: titles.join('; ') }) : t('settings.noChanges'))
      if (r.reconnect_suggested) {
        const ok = await confirm({
          title: t('settings.applyTitle'),
          body: t('settings.applyText', { count: r.sessions }),
          confirmLabel: t('dashboard.reconnectAll'),
        })
        if (ok) {
          const d = await api.reconnectAll()
          toast(t('pools.reconnecting', { count: d.sessions, window: formatDuration(parseGoDuration(d.window), t) }))
        }
      }
    } catch (err) {
      if (err instanceof ApiError && Object.keys(err.fields).length) {
        setErrors(err.fields)
        // The groups with a wrong value open, so it can be seen.
        setOpen(
          Object.keys(err.fields).flatMap((k) => byKey.get(k)?.group ?? []),
          true,
        )
      }
      toast(errorText(i18n, err), 'error')
    } finally {
      setSaving(false)
    }
  }

  // While searching, a group shows its matching settings, or all of them
  // when its own title matches.
  const itemsOf = (g: string) => {
    const items = data.settings.filter((m) => m.group === g)
    if (!q || title(g).toLowerCase().includes(q)) return items
    return items.filter((m) => matches(m, q, t))
  }

  const renderGroup = (g: string) => {
    const items = itemsOf(g)
    if (!items.length) return null
    const inGroup = data.settings.filter((m) => m.group === g)
    const summary = groupSummary({
      group: g,
      value: (k) => {
        const m = byKey.get(k)
        return m && current(m)
      },
      meta: (k) => byKey.get(k),
      telegram: data.server.telegram,
      t,
      loc,
    })
    return (
      <Group
        key={g}
        id={g}
        title={title(g)}
        summary={summary}
        open={!!q || isOpen(g)}
        onToggle={() => setOpen([g], !isOpen(g))}
        modified={inGroup.filter((m) => current(m) !== m.default).length}
        unsaved={inGroup.filter((m) => m.key in draft).length}
        errors={inGroup.filter((m) => errors[m.key]).length}
      >
        <div className="panel">
          {items.map((m) => (
            <SettingRow key={m.key} meta={m} value={current(m)} error={errorFor(m)} onChange={(v) => change(m, v)} />
          ))}
        </div>
        {!q && g === 'timed' && <TimedPanel pools={pools.data ?? []} />}
        {!q && g === 'hunt' && <HuntPanel pools={pools.data ?? []} />}
        {!q && g === 'alerts' && <TelegramPanel status={data.server.telegram} />}
      </Group>
    )
  }

  const found = groups.some((g) => itemsOf(g).length > 0)
  const advancedShown = advanced.filter((g) => itemsOf(g).length > 0)

  return (
    <>
      <div className="settings-tools">
        <input
          type="search"
          className="filter"
          placeholder={t('settings.search')}
          aria-label={t('settings.search')}
          value={query}
          onChange={(e) => setQuery(e.target.value)}
        />
        <button type="button" className="small" disabled={!!q} onClick={() => setOpen(all, !allOpen)}>
          {allOpen ? t('settings.collapseAll') : t('settings.expandAll')}
        </button>
      </div>
      {!q && (
        <nav className="chips" aria-label={t('settings.groupsNav')}>
          {all.map((g) => (
            <button key={g} type="button" className={advancedGroups.has(g) || g === serverGroup ? 'chip muted' : 'chip'} onClick={() => jump(g)}>
              {title(g)}
            </button>
          ))}
        </nav>
      )}
      {main.map(renderGroup)}
      {advancedShown.length > 0 && (
        <div className="advanced-head">
          <h3>{t('settings.advanced')}</h3>
          {!q && <p className="small muted">{t('settings.advancedHelp')}</p>}
        </div>
      )}
      {advancedShown.map(renderGroup)}
      {!q && (
        <Group
          id={serverGroup}
          title={title(serverGroup)}
          open={isOpen(serverGroup)}
          onToggle={() => setOpen([serverGroup], !isOpen(serverGroup))}
          modified={0}
          unsaved={0}
          errors={0}
        >
          <ServerPanel server={data.server} />
        </Group>
      )}
      {q && !found && <div className="panel muted">{t('settings.nothingFound')}</div>}
      <div className="savebar">
        <span className="muted grow">
          {t('settings.modifiedCount', { count: modified })}
          {dirty > 0 && <b> · {t('settings.unsavedCount', { count: dirty })}</b>}
        </span>
        <button type="button" disabled={!modified} onClick={resetAll}>
          {t('settings.resetAll')}
        </button>
        <button
          type="button"
          disabled={!dirty}
          onClick={() => {
            setDraft({})
            setErrors({})
          }}
        >
          {t('settings.discard')}
        </button>
        <button type="button" className="primary" disabled={!dirty || saving} onClick={() => void save()}>
          {t('common.save')}
        </button>
      </div>
    </>
  )
}

export function SettingsPage() {
  const { t } = useTranslation()
  const settings = useSettings()
  return (
    <>
      <h2>{t('settings.title')}</h2>
      <p className="small muted">{t('settings.help')}</p>
      <QueryState pending={settings.isPending} error={settings.error}>
        {settings.data && <SettingsForm data={settings.data} />}
      </QueryState>
    </>
  )
}
