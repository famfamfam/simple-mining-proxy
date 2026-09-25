const hour = 3600
const day = 24 * hour

/** Chart periods in seconds. The keys are also the texts stats.ranges.*. */
export const rangeSeconds = {
  '1h': hour,
  '6h': 6 * hour,
  '24h': day,
  '7d': 7 * day,
  '30d': 30 * day,
  '90d': 90 * day,
  '1y': 365 * day,
} as const

export type RangeKey = keyof typeof rangeSeconds
