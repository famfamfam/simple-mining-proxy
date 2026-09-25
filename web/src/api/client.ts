import type {
  ActivateResult,
  ApiErrorBody,
  DrainResult,
  EventItem,
  History,
  Miner,
  Msg,
  Pool,
  PoolInput,
  ProfitStatus,
  SettingsPayload,
  SettingsSaved,
  SettingValue,
  Status,
  TestResult,
  TimedStatus,
  WorkerSeries,
  WorkerSummary,
} from './types'

/** A failed API call with the server's message and per-field messages. */
export class ApiError extends Error {
  readonly status: number
  readonly code: string
  readonly msg: Msg
  readonly fields: Record<string, Msg>

  constructor(status: number, body: Partial<ApiErrorBody> | null) {
    const msg: Msg = body?.key
      ? { key: body.key, params: body.params, message: body.message ?? '' }
      : { key: '', message: body?.message ?? `HTTP ${status}` }
    super(msg.message)
    this.name = 'ApiError'
    this.status = status
    this.code = body?.error ?? ''
    this.msg = msg
    this.fields = body?.fields ?? {}
  }
}

type Method = 'GET' | 'POST' | 'PUT' | 'DELETE'

// Paths are relative so the UI also works behind a reverse proxy prefix.
async function request<T>(method: Method, path: string, body?: unknown): Promise<T> {
  const init: RequestInit = { method, credentials: 'same-origin' }
  if (method !== 'GET') {
    // The API requires JSON for every state-changing request (CSRF guard).
    init.headers = { 'Content-Type': 'application/json' }
    init.body = JSON.stringify(body ?? {})
  }
  const res = await fetch(path, init)
  const data: unknown = await res.json().catch(() => null)
  if (!res.ok) throw new ApiError(res.status, data as Partial<ApiErrorBody> | null)
  return data as T
}

const pool = (id: string) => `api/pools/${encodeURIComponent(id)}`

export const api = {
  me: () => request<{ username: string }>('GET', 'api/auth/me'),
  login: (username: string, password: string) =>
    request<{ username: string }>('POST', 'api/auth/login', { username, password }),
  logout: () => request<{ ok: boolean }>('POST', 'api/auth/logout'),

  status: () => request<Status>('GET', 'api/status'),
  miners: () => request<{ miners: Miner[] }>('GET', 'api/miners').then((r) => r.miners),
  events: (limit: number) =>
    request<{ events: EventItem[] }>('GET', `api/events?limit=${limit}`).then((r) => r.events),
  reconnectAll: () => request<DrainResult>('POST', 'api/miners/reconnect'),

  pools: () => request<{ pools: Pool[] }>('GET', 'api/pools').then((r) => r.pools),
  createPool: (input: PoolInput) => request<Pool>('POST', 'api/pools', input),
  updatePool: (id: string, input: PoolInput, reconnect: boolean) =>
    request<{ pool: Pool; reconnecting: number }>('PUT', `${pool(id)}${reconnect ? '?reconnect=true' : ''}`, input),
  deletePool: (id: string) => request<{ reconnecting: number }>('DELETE', pool(id)),
  testPool: (id: string) => request<TestResult>('POST', `${pool(id)}/test`),
  /** Checks editor fields before saving; id names the saved pool they apply to. */
  testConfig: (input: PoolInput) => request<TestResult>('POST', 'api/pools/test', input),
  activatePool: (id: string, force: boolean) =>
    request<ActivateResult>('POST', `${pool(id)}/activate${force ? '?force=true' : ''}`),
  setFallback: (ids: string[]) => request<{ pools: Pool[] }>('PUT', 'api/fallback', { pools: ids }),

  /** The last `seconds` of statistics in at most about `points` steps. */
  history: (seconds: number, points: number) => {
    const to = Math.floor(Date.now() / 1000)
    return request<History>('GET', `api/history?from=${to - seconds}&to=${to}&points=${points}`)
  },

  /** One ASIC login over the last `seconds`. */
  workerHistory: (name: string, seconds: number, points: number) => {
    const to = Math.floor(Date.now() / 1000)
    return request<{ name: string; series: WorkerSeries }>(
      'GET',
      `api/history/worker?name=${encodeURIComponent(name)}&from=${to - seconds}&to=${to}&points=${points}`,
    )
  },
  /** Every ASIC login seen over the last `seconds`, connected now or not. */
  workers: (seconds: number) => {
    const to = Math.floor(Date.now() / 1000)
    return request<{ workers: WorkerSummary[] }>('GET', `api/history/workers?from=${to - seconds}&to=${to}`).then(
      (r) => r.workers,
    )
  },

  profit: () => request<ProfitStatus>('GET', 'api/profit'),
  /** Compares the coins now; never switches. */
  profitCheck: () => request<ProfitStatus>('POST', 'api/profit/check'),
  timed: () => request<TimedStatus>('GET', 'api/timed'),

  settings: () => request<SettingsPayload>('GET', 'api/settings'),
  /** null resets a setting to its default. */
  saveSettings: (changes: Record<string, SettingValue | null>) =>
    request<SettingsSaved>('PUT', 'api/settings', changes),
}
