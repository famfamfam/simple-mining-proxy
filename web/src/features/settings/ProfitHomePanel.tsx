import { useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api } from '../../api/client'
import { keys } from '../../api/queries'
import type { Pool } from '../../api/types'
import { useToast } from '../../context/feedback'
import { errorText } from '../../i18n/messages'

/** The main pool of profit switching, under its settings: saved at once. */
export function ProfitHomePanel({ pools }: { pools: Pool[] }) {
  const { t, i18n } = useTranslation()
  const qc = useQueryClient()
  const toast = useToast()
  const [busy, setBusy] = useState(false)
  const home = pools.find((p) => p.profit_home)?.id ?? ''
  const candidates = pools.filter((p) => p.profit_switch && !p.solo)

  const choose = async (id: string) => {
    setBusy(true)
    try {
      await api.setProfitHome(id)
      const name = pools.find((p) => p.id === id)?.name
      toast(name ? t('settings.profitHome.saved', { pool: name }) : t('settings.profitHome.cleared'))
    } catch (err) {
      toast(errorText(i18n, err), 'error')
    } finally {
      setBusy(false)
      void qc.invalidateQueries({ queryKey: keys.pools })
      void qc.invalidateQueries({ queryKey: ['profit'] })
    }
  }

  return (
    <div className="panel">
      <div className="kv">
        <label className="muted" htmlFor="profit-home">
          {t('settings.profitHome.title')}
        </label>
        <div>
          <select id="profit-home" value={home} disabled={busy} onChange={(e) => void choose(e.target.value)}>
            <option value="">{t('settings.profitHome.none')}</option>
            {candidates.map((p) => (
              <option key={p.id} value={p.id}>
                {p.coin ? `${p.name} (${p.coin})` : p.name}
              </option>
            ))}
          </select>
          <div className="small muted">
            {candidates.length ? t('settings.profitHome.help') : t('settings.profitHome.noCandidates')}
          </div>
        </div>
      </div>
    </div>
  )
}
