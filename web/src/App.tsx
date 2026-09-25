import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { api } from './api/client'
import { keys } from './api/queries'
import { Header } from './components/Header'
import { useHashRoute, type Route } from './hooks/useHashRoute'
import { DashboardPage } from './pages/DashboardPage'
import { EventsPage } from './pages/EventsPage'
import { LoginPage } from './pages/LoginPage'
import { MinersPage } from './pages/MinersPage'
import { SettingsPage } from './pages/SettingsPage'
import { StatsPage } from './pages/StatsPage'

const pages: Record<Route, () => React.JSX.Element> = {
  dashboard: DashboardPage,
  stats: StatsPage,
  miners: MinersPage,
  events: EventsPage,
  settings: SettingsPage,
}

function Shell() {
  const route = useHashRoute()
  const Page = pages[route]
  return (
    <>
      <Header route={route} />
      <main className="content">
        <Page />
      </main>
    </>
  )
}

/** Login screen until the session cookie works; a 401 anywhere returns here. */
export function App() {
  const { t } = useTranslation()
  const me = useQuery({ queryKey: keys.me, queryFn: api.me, retry: false, staleTime: Infinity })
  if (me.isPending) return <div className="boot muted">{t('common.loading')}</div>
  if (!me.data) return <LoginPage />
  return <Shell />
}
