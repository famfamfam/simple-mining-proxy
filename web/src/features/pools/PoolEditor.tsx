import { useCallback, useId, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { ApiError, api } from '../../api/client'
import { useRefreshLive } from '../../api/queries'
import type { Pool, PoolInput } from '../../api/types'
import { Modal } from '../../components/Modal'
import { useToast } from '../../context/feedback'
import { errorText, translateMsg } from '../../i18n/messages'
import { hostPort, parseAddresses, type ParsedAddresses } from '../../lib/addresses'
import { TestDialog } from './TestDialog'

const coins = ['BTC', 'BCH', 'BSV', 'XEC', 'DGB', 'FB']

type Errors = Partial<Record<'name' | 'coin' | 'addresses' | 'username' | 'password' | 'tls_skip_verify', string>>

function Field({ label, error, help, children }: { label: string; error?: string; help?: ReactNode; children: ReactNode }) {
  return (
    <>
      <span className="form-label">{label}</span>
      <div className="form-field">
        {children}
        {error && <div className="field-error">{error}</div>}
        {help && <div className="small muted">{help}</div>}
      </div>
    </>
  )
}

/** Create (pool = null) or edit a pool. */
export function PoolEditor({ pool, onClose }: { pool: Pool | null; onClose: () => void }) {
  const { t, i18n } = useTranslation()
  const toast = useToast()
  const refresh = useRefreshLive()
  const coinList = useId()

  const [name, setName] = useState(pool?.name ?? '')
  const [coin, setCoin] = useState(pool?.coin ?? 'BTC')
  const [addresses, setAddresses] = useState(pool ? pool.addresses.map(hostPort).join('\n') : '')
  const [tls, setTls] = useState(pool?.tls ?? false)
  const [skipVerify, setSkipVerify] = useState(pool?.tls_skip_verify ?? false)
  const [username, setUsername] = useState(pool?.username ?? '')
  // A new pool or one without a password shows the input right away.
  const [changePassword, setChangePassword] = useState(!pool?.password_set)
  const [password, setPassword] = useState(pool ? '' : 'x')
  const [reconnect, setReconnect] = useState(false)
  const [profitSwitch, setProfitSwitch] = useState(pool?.profit_switch ?? false)
  const [timedTarget, setTimedTarget] = useState(pool?.timed_target ?? false)
  const [errors, setErrors] = useState<Errors>({})
  const [schemeNote, setSchemeNote] = useState('')
  const [busy, setBusy] = useState(false)
  const [testRun, setTestRun] = useState<PoolInput | null>(null)

  const addressError = (p: ParsedAddresses) =>
    p.ok ? '' : p.error === 'empty' ? t('editor.addressEmpty') : t('editor.addressLine', { line: p.line, text: p.text })

  const onAddresses = (text: string) => {
    setAddresses(text)
    // A URL pasted from the pool site tells the transport.
    const parsed = parseAddresses(text)
    if (parsed.ok && parsed.transport) {
      setTls(parsed.transport === 'tls')
      setSchemeNote(t('editor.schemeDetected', { transport: parsed.transport.toUpperCase() }))
    }
  }

  /** The form as an API body, or null with the address error shown. */
  const collect = (): PoolInput | null => {
    const parsed = parseAddresses(addresses)
    if (!parsed.ok) {
      setErrors({ addresses: addressError(parsed) })
      return null
    }
    const body: PoolInput = {
      name,
      coin,
      addresses: parsed.addresses,
      tls,
      tls_skip_verify: tls && skipVerify,
      username,
      profit_switch: profitSwitch,
      timed_target: timedTarget,
    }
    if (changePassword) body.password = password
    return body
  }

  const showApiErrors = useCallback(
    (err: unknown) => {
      if (err instanceof ApiError && Object.keys(err.fields).length) {
        const next: Errors = {}
        for (const [k, msg] of Object.entries(err.fields)) next[k as keyof Errors] = translateMsg(i18n, msg)
        setErrors(next)
        return true
      }
      return false
    },
    [i18n],
  )

  const save = async () => {
    const body = collect()
    if (!body) return
    setErrors({})
    setBusy(true)
    try {
      if (pool) {
        const r = await api.updatePool(pool.id, body, reconnect)
        toast(r.reconnecting ? t('editor.savedReconnecting', { count: r.reconnecting }) : t('editor.saved'))
      } else {
        await api.createPool(body)
        toast(t('editor.added'))
      }
      refresh()
      onClose()
    } catch (err) {
      if (!showApiErrors(err)) toast(errorText(i18n, err), 'error')
    } finally {
      setBusy(false)
    }
  }

  const test = () => {
    const body = collect()
    if (!body) return
    setErrors({})
    setTestRun(pool ? { ...body, id: pool.id } : body)
  }
  const runTest = useCallback(() => api.testConfig(testRun ?? {}), [testRun])

  const preview = (username || 'account.{worker}').replaceAll('{worker}', 'S21-0042').replaceAll('{login}', 'farm.S21-0042')

  return (
    <Modal
      title={pool ? t('editor.editTitle', { pool: pool.name }) : t('editor.newTitle')}
      onClose={onClose}
      footer={
        <>
          <button type="button" onClick={test} title={t('editor.testHelp')} disabled={busy}>
            {t('editor.test')}
          </button>
          <span className="grow" />
          <button type="button" onClick={onClose}>
            {t('common.cancel')}
          </button>
          <button type="button" className="primary" onClick={() => void save()} disabled={busy}>
            {t('common.save')}
          </button>
        </>
      }
    >
      <form
        className="form"
        onSubmit={(e) => {
          e.preventDefault()
          void save()
        }}
      >
        <Field label={t('editor.name')} error={errors.name}>
          <input type="text" value={name} onChange={(e) => setName(e.target.value)} placeholder="EMCD BTC" />
        </Field>
        <Field label={t('editor.coin')} error={errors.coin}>
          <input type="text" value={coin} list={coinList} onChange={(e) => setCoin(e.target.value)} placeholder="BTC" />
          <datalist id={coinList}>
            {coins.map((c) => (
              <option key={c} value={c} />
            ))}
          </datalist>
        </Field>
        <Field label={t('editor.addresses')} error={errors.addresses} help={schemeNote || t('editor.addressesHelp')}>
          <textarea
            rows={3}
            spellCheck={false}
            className="mono"
            value={addresses}
            onChange={(e) => onAddresses(e.target.value)}
            placeholder={'eu.pool.example.com:3333\nus.pool.example.com:3333'}
          />
        </Field>
        <Field label={t('editor.transport')} error={errors.tls_skip_verify}>
          <div className="actions">
            <label>
              <input type="radio" name="transport" checked={!tls} onChange={() => setTls(false)} /> TCP
            </label>
            <label>
              <input type="radio" name="transport" checked={tls} onChange={() => setTls(true)} /> TLS
            </label>
            <label className={tls ? '' : 'muted'}>
              <input type="checkbox" disabled={!tls} checked={skipVerify} onChange={(e) => setSkipVerify(e.target.checked)} />{' '}
              {t('editor.skipVerify')}
            </label>
          </div>
          {tls && skipVerify && <div className="warnbox">{t('editor.skipVerifyWarning')}</div>}
        </Field>
        <Field
          label={t('editor.username')}
          error={errors.username}
          help={
            <>
              <div className="mono">farm.S21-0042 → {preview}</div>
              {t('editor.usernameHelp')}
            </>
          }
        >
          <input type="text" value={username} onChange={(e) => setUsername(e.target.value)} placeholder="account.{worker}" />
        </Field>
        <Field label={t('editor.password')} error={errors.password} help={t('editor.passwordHelp')}>
          {changePassword ? (
            <input
              type="text"
              value={password}
              autoComplete="off"
              placeholder="x"
              onChange={(e) => setPassword(e.target.value)}
            />
          ) : (
            <span>
              ••••• ({t('editor.passwordSet')}){' '}
              <button type="button" className="link" onClick={() => setChangePassword(true)}>
                {t('editor.passwordChange')}
              </button>
            </span>
          )}
        </Field>
        <span className="form-label">{t('editor.profit')}</span>
        <div className="form-field">
          <label>
            <input type="checkbox" checked={profitSwitch} onChange={(e) => setProfitSwitch(e.target.checked)} />{' '}
            {t('editor.profitSwitch')}
          </label>
          <div className="small muted">{t('editor.profitHelp')}</div>
        </div>
        <span className="form-label">{t('editor.timed')}</span>
        <div className="form-field">
          <label>
            <input type="checkbox" checked={timedTarget} onChange={(e) => setTimedTarget(e.target.checked)} />{' '}
            {t('editor.timedTarget')}
          </label>
          <div className="small muted">{t('editor.timedHelp')}</div>
        </div>
        {pool && pool.sessions > 0 && (
          <>
            <span />
            <label>
              <input type="checkbox" checked={reconnect} onChange={(e) => setReconnect(e.target.checked)} />{' '}
              {t('editor.reconnect', { count: pool.sessions })}
            </label>
          </>
        )}
        <button type="submit" hidden />
      </form>
      {testRun && (
        <TestDialog
          title={pool ? t('test.title', { pool: name || pool.name }) : t('test.unsaved')}
          run={runTest}
          onError={showApiErrors}
          onClose={() => setTestRun(null)}
        />
      )}
    </Modal>
  )
}
