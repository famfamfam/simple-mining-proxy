import { useQueryClient } from '@tanstack/react-query'
import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { api } from '../api/client'
import { keys } from '../api/queries'
import { LanguageSelect } from '../components/LanguageSelect'
import { errorText } from '../i18n/messages'

export function LoginPage() {
  const { t, i18n } = useTranslation()
  const qc = useQueryClient()
  const [username, setUsername] = useState('admin')
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setError('')
    setBusy(true)
    try {
      qc.setQueryData(keys.me, await api.login(username, password))
    } catch (err) {
      setError(errorText(i18n, err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <main className="login-page">
      <form className="login-box panel" onSubmit={(e) => void submit(e)}>
        <div className="login-head">
          <h1>{t('app.brand')}</h1>
          <LanguageSelect />
        </div>
        <label htmlFor="login-user">{t('login.username')}</label>
        <input
          id="login-user"
          type="text"
          autoComplete="username"
          value={username}
          onChange={(e) => setUsername(e.target.value)}
        />
        <label htmlFor="login-pass">{t('login.password')}</label>
        <input
          id="login-pass"
          type="password"
          autoComplete="current-password"
          autoFocus
          value={password}
          onChange={(e) => setPassword(e.target.value)}
        />
        {error && (
          <div className="field-error" role="alert">
            {error}
          </div>
        )}
        <button type="submit" className="primary" disabled={busy}>
          {t('login.submit')}
        </button>
      </form>
    </main>
  )
}
