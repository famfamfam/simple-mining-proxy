import { keepPreviousData, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from './client'

/** Live screens refresh every few seconds while the tab is visible. */
export const POLL_MS = 5000

export const keys = {
  me: ['me'] as const,
  status: ['status'] as const,
  pools: ['pools'] as const,
  miners: ['miners'] as const,
  events: (limit: number) => ['events', limit] as const,
  settings: ['settings'] as const,
  timed: ['timed'] as const,
}

export const useStatus = () => useQuery({ queryKey: keys.status, queryFn: api.status, refetchInterval: POLL_MS })

export const usePools = () => useQuery({ queryKey: keys.pools, queryFn: api.pools, refetchInterval: POLL_MS })

export const useMiners = () => useQuery({ queryKey: keys.miners, queryFn: api.miners, refetchInterval: POLL_MS })

export const useEvents = (limit: number) =>
  useQuery({ queryKey: keys.events(limit), queryFn: () => api.events(limit), refetchInterval: POLL_MS })

/** History points are per minute: refreshing more often shows nothing new. */
export const HISTORY_POLL_MS = 60_000

// A new range keeps the old chart on screen (dimmed) until its data arrives.
export const useHistory = (seconds: number, points = 800) =>
  useQuery({
    queryKey: ['history', seconds, points],
    queryFn: () => api.history(seconds, points),
    refetchInterval: HISTORY_POLL_MS,
    placeholderData: keepPreviousData,
  })

export const useWorkerHistory = (name: string, seconds: number, points = 600) =>
  useQuery({
    queryKey: ['history', 'worker', name, seconds, points],
    queryFn: () => api.workerHistory(name, seconds, points),
    refetchInterval: HISTORY_POLL_MS,
    placeholderData: keepPreviousData,
  })

export const useWorkers = (seconds: number) =>
  useQuery({
    queryKey: ['history', 'workers', seconds],
    queryFn: () => api.workers(seconds),
    refetchInterval: HISTORY_POLL_MS,
    placeholderData: keepPreviousData,
  })

export const useProfit = () => useQuery({ queryKey: ['profit'], queryFn: api.profit, refetchInterval: HISTORY_POLL_MS })

export const useTimed = () => useQuery({ queryKey: keys.timed, queryFn: api.timed, refetchInterval: POLL_MS })

// Settings are edited in place: loaded when the screen opens, never polled.
export const useSettings = () =>
  useQuery({ queryKey: keys.settings, queryFn: api.settings, refetchOnWindowFocus: false })

/** Refetches everything that a pool or session change can affect. */
export function useRefreshLive() {
  const qc = useQueryClient()
  return () => {
    void qc.invalidateQueries({ queryKey: keys.status })
    void qc.invalidateQueries({ queryKey: keys.pools })
    void qc.invalidateQueries({ queryKey: keys.timed })
    void qc.invalidateQueries({ queryKey: ['events'] })
  }
}
