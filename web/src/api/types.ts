// Shapes of the admin API responses (internal/admin/server.go).

export type Health = 'UP' | 'DOWN' | 'UNKNOWN'
export type Mode = 'NORMAL' | 'DEGRADED' | 'DOWN' | 'SWITCHING'
export type Role = 'active' | 'fallback' | 'none'

/** An operator-facing message: the UI translates key with params; message is the English text. */
export interface Msg {
  key: string
  params?: Record<string, unknown>
  message: string
}

export interface ApiErrorBody extends Msg {
  error: string
  fields?: Record<string, Msg>
}

export interface ConnString {
  enabled: boolean
  listen: string
  scheme: string
  host: string
  port: string
  url?: string
}

export interface Connections {
  tcp: ConnString
  tls: ConnString
}

export interface Status {
  uptime_seconds: number
  mode: Mode
  active_pool: string
  effective_pool: string
  fallback_pools: string[]
  last_switch: { at: string; from: string; to: string; reason: string } | null
  miners: { total: number; tcp: number; tls: number }
  shares: { accepted: number; rejected: number; reject_reasons: Record<string, number> }
  hashrate_ths: number
  connections: Connections
  errors: { tls_handshake: number; upstream_invalid_json: number }
}

export interface Address {
  host: string
  port: number
}

export interface PoolAddress extends Address {
  health: Health
  /** Median TCP connect time of the last probes. */
  latency_ms: number | null
  last_error?: string
  /** New sessions try this address first. */
  preferred: boolean
}

export interface Pool {
  id: string
  name: string
  coin: string
  addresses: PoolAddress[]
  tls: boolean
  tls_skip_verify: boolean
  username: string
  password_set: boolean
  /** Profit switching may choose this pool. */
  profit_switch: boolean
  role: Role
  fallback_position?: number
  health: Health
  last_check: string | null
  last_error: string | null
  sessions: number
  accepted: number
  rejected: number
  hashrate_ths: number
}

/** Pool create/update body; omitted fields stay unchanged. */
export interface PoolInput {
  id?: string
  name?: string
  coin?: string
  addresses?: Address[]
  tls?: boolean
  tls_skip_verify?: boolean
  username?: string
  password?: string
  profit_switch?: boolean
}

export interface TestResult {
  ok: boolean
  elapsed_ms: number
  addresses: (Address & { ok: boolean; latency_ms: number | null; error?: string })[]
}

export interface Miner {
  id: number
  worker: string
  upstream_user: string
  ip: string
  transport: 'tcp' | 'tls'
  pool_id: string
  pool_name: string
  pool_addr: string
  user_agent: string
  difficulty: number
  accepted: number
  rejected: number
  last_share: string | null
  hashrate_hs: number
  connected_at: string
}

export interface EventItem {
  seq: number
  time: string
  level: 'info' | 'warn' | 'error'
  type: string
  message: string
}

export type SettingType = 'duration' | 'int' | 'enum' | 'string'
export type Applies = 'immediately' | 'new_connections' | 'next_drain'
/** Durations are Go duration strings ("20s", "2m"). */
export type SettingValue = string | number

export interface SettingMeta {
  key: string
  group: string
  type: SettingType
  unit?: string
  value: SettingValue
  default: SettingValue
  min?: SettingValue
  max?: SettingValue
  options?: string[]
  min_len?: number
  max_len?: number
  applies: Applies
  modified: boolean
}

export interface CertInfo {
  type: 'self-signed' | 'loaded'
  sha256: string
  subject: string
  dns_names: string[] | null
  not_after: string
}

export interface ServerInfo {
  connections: Connections
  public_host: string
  admin_listen: string
  admin_username: string
  api_token_set: boolean
  data_dir: string
  log_format: string
  certificate: CertInfo | null
}

export interface SettingsPayload {
  groups: string[]
  settings: SettingMeta[]
  modified: number
  server: ServerInfo
}

export interface SettingChange {
  key: string
  old: string
  new: string
  applies: Applies
}

export interface SettingsSaved extends SettingsPayload {
  changes: SettingChange[]
  reconnect_suggested: boolean
  sessions: number
}

export interface DrainResult {
  sessions: number
  window: string
}

export interface ActivateResult {
  reconnecting: number
  window: string
}

/** Statistics resampled to a fixed step; null is a step without data. */
export interface HistorySeries {
  step: number
  time: number[]
  hashrate: (number | null)[] // H/s
  accepted: (number | null)[]
  rejected: (number | null)[]
  miners: (number | null)[]
  pools: Record<string, (number | null)[]> // hashrate per pool id, H/s
}

export interface History {
  from: number
  to: number
  series: HistorySeries
  pool_names: Record<string, string>
  detail_retention: string
  retention: string
  miner_retention: string
  disk_bytes: number
}

/** One ASIC login over time; online is the part of each step it was connected (0–1). */
export interface WorkerSeries {
  step: number
  time: number[]
  hashrate: (number | null)[]
  accepted: (number | null)[]
  rejected: (number | null)[]
  online: (number | null)[]
  /** The whole range, computed like the miners list on the Stats page. */
  summary: WorkerSummary
}

export interface WorkerSummary {
  name: string
  /** H/s averaged over the time it was connected. */
  hashrate: number
  accepted: number
  rejected: number
  /** Part of the recorded time it was connected, 0–1. */
  online: number
  /** Unix seconds: end of the last interval it was connected or sent a share; 0 if never. */
  last_seen: number
}

export type ProfitMode = 'off' | 'advise' | 'auto'

export type ProfitDecision =
  | 'no_data'
  | 'no_candidates'
  | 'manual'
  | 'best'
  | 'below_margin'
  | 'recommend'
  | 'switched'
  | 'switch_failed'

/** One coin; revenue is for 1 TH/s over a day. */
export interface CoinView {
  tag: string
  name: string
  price_btc: number
  price_usd: number
  difficulty: number
  block_reward: number
  revenue_btc: number
  revenue_usd: number
  stale: boolean
  pools: string[]
}

export interface ProfitReport {
  at: string
  scheduled: boolean
  mode: ProfitMode
  margin: number
  coins: CoinView[]
  active: string
  active_coin: string
  best: string
  advantage: number
  decision: ProfitDecision
  target?: string
  error?: string
}

export interface ProfitStatus {
  mode: ProfitMode
  interval: string
  margin: number
  last_run: string | null
  next_run: string | null
  btc_usd: number
  report: ProfitReport | null
}
