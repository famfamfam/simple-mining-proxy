import { useEffect, useState } from 'react'

export const routes = ['dashboard', 'stats', 'miners', 'events', 'settings'] as const
export type Route = (typeof routes)[number]

function parse(hash: string): Route {
  const name = hash.replace(/^#\/?/, '')
  return (routes as readonly string[]).includes(name) ? (name as Route) : 'dashboard'
}

export const href = (route: Route) => `#/${route}`

// A screen with unsaved edits registers a guard; it returns false to stay.
let leaveGuard: (() => boolean) | null = null

export function setLeaveGuard(guard: (() => boolean) | null) {
  leaveGuard = guard
}

/** The current screen, from the URL hash (#/miners). */
export function useHashRoute(): Route {
  const [route, setRoute] = useState(() => parse(window.location.hash))
  useEffect(() => {
    const onChange = () => {
      const next = parse(window.location.hash)
      if (next === route) return
      if (leaveGuard && !leaveGuard()) {
        // Put the hash back without a new hashchange event.
        window.history.replaceState(null, '', href(route))
        return
      }
      setRoute(next)
    }
    window.addEventListener('hashchange', onChange)
    return () => window.removeEventListener('hashchange', onChange)
  }, [route])
  return route
}
