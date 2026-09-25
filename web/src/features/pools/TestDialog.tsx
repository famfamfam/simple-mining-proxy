import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { ApiError } from '../../api/client'
import type { TestResult } from '../../api/types'
import { Modal } from '../../components/Modal'
import { errorText, translateMsg } from '../../i18n/messages'
import { hostPort } from '../../lib/addresses'
import { formatMs } from '../../lib/format'

interface TestDialogProps {
  title: string
  run: () => Promise<TestResult>
  onClose: () => void
  onError?: (err: unknown) => void
}

/** Runs a pool check once and shows the result of every address. */
export function TestDialog({ title, run, onClose, onError }: TestDialogProps) {
  const { t, i18n } = useTranslation()
  const [result, setResult] = useState<TestResult | null>(null)
  const [error, setError] = useState<unknown>(null)
  const started = useRef(false)

  useEffect(() => {
    if (started.current) return // StrictMode runs effects twice in development
    started.current = true
    run().then(setResult, (err: unknown) => {
      setError(err)
      onError?.(err)
    })
  }, [run, onError])

  const fields = error instanceof ApiError ? Object.entries(error.fields) : []

  return (
    <Modal
      title={title}
      onClose={onClose}
      footer={
        <button type="button" className="primary" onClick={onClose}>
          {t('common.close')}
        </button>
      }
    >
      {!result && !error && <div className="muted">{t('test.running')}</div>}
      {result && (
        <>
          <p>
            <span className="badge ok">{t('test.passed')}</span>{' '}
            <span className="muted small">{t('test.elapsed', { value: formatMs(result.elapsed_ms, t) })}</span>
          </p>
          <ul className="plain">
            {result.addresses.map((a) => (
              <li key={hostPort(a)} className="test-row">
                <span className="mono">{hostPort(a)}</span>
                {a.ok ? (
                  <span className="lvl-ok">{a.latency_ms != null ? formatMs(a.latency_ms, t) : t('test.ok')}</span>
                ) : (
                  <span className="lvl-error small">{a.error}</span>
                )}
              </li>
            ))}
          </ul>
        </>
      )}
      {error != null && (
        <>
          <p>
            <span className="badge bad">{t('test.failed')}</span>
          </p>
          <p className="mono small wrap">{errorText(i18n, error)}</p>
          {fields.length > 0 && (
            <ul className="plain small">
              {fields.map(([name, msg]) => (
                <li key={name} className="field-error">
                  {translateMsg(i18n, msg)}
                </li>
              ))}
            </ul>
          )}
        </>
      )}
    </Modal>
  )
}
