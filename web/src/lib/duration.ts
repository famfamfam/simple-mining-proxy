// Settings send durations as Go duration strings ("20s", "2m", "168h").

const unitSeconds: Record<string, number> = { ms: 0.001, s: 1, m: 60, h: 3600 }

/** Seconds in a Go duration string; unknown parts are ignored. */
export function parseGoDuration(s: string): number {
  let total = 0
  for (const m of s.matchAll(/(\d+(?:\.\d+)?)(ms|h|m|s)/g)) {
    total += parseFloat(m[1] ?? '0') * (unitSeconds[m[2] ?? 's'] ?? 0)
  }
  return total
}

/**
 * The canonical form the server stores (settings.FormatDuration): whole
 * hours as "168h", whole minutes as "2m", otherwise seconds.
 */
export function toGoDuration(seconds: number): string {
  const s = Math.round(seconds)
  if (s > 0 && s % 3600 === 0) return `${s / 3600}h`
  if (s > 0 && s % 60 === 0) return `${s / 60}m`
  return `${s}s`
}

export type DurationUnit = 's' | 'm' | 'h' | 'd'

export const unitSize: Record<DurationUnit, number> = { s: 1, m: 60, h: 3600, d: 86400 }

/** Units that make sense for a setting: none larger than its maximum. */
export function unitsUpTo(maxSeconds: number): DurationUnit[] {
  return (['s', 'm', 'h', 'd'] as const).filter((u) => u === 's' || unitSize[u] <= maxSeconds)
}

/** The largest of the units that shows the value as a whole number. */
export function bestUnit(seconds: number, units: DurationUnit[]): DurationUnit {
  for (const u of [...units].reverse()) {
    if (seconds > 0 && seconds % unitSize[u] === 0) return u
  }
  return 's'
}
