import type { Address } from '../api/types'

export type Transport = 'tcp' | 'tls'

export type ParsedAddresses =
  | { ok: true; addresses: Address[]; transport: Transport | null }
  | { ok: false; error: 'empty' }
  | { ok: false; error: 'format'; line: number; text: string }

const tlsSchemes = new Set(['stratum+ssl', 'stratum+tls', 'ssl', 'tls'])
const tcpSchemes = new Set(['stratum+tcp', 'stratum', 'tcp'])

/**
 * Parses the address field: one address per line (commas also split),
 * "host:port", "[ipv6]:port" or a URL pasted from a pool site such as
 * "stratum+tls://eu.pool.com:13339". transport is what the pasted schemes
 * say when they all agree, or null.
 */
export function parseAddresses(text: string): ParsedAddresses {
  const addresses: Address[] = []
  const seen = new Set<Transport>()
  const lines = text
    .split(/[\n,]+/)
    .map((l) => l.trim())
    .filter(Boolean)
  for (const [i, line] of lines.entries()) {
    const scheme = /^([a-z0-9+.-]+):\/\//i.exec(line)?.[1]?.toLowerCase()
    if (scheme && tlsSchemes.has(scheme)) seen.add('tls')
    if (scheme && tcpSchemes.has(scheme)) seen.add('tcp')
    const rest = line.replace(/^[a-z0-9+.-]+:\/\//i, '').replace(/\/.*$/, '')
    const m = /^\[([^\]]+)\]:(\d+)$/.exec(rest) ?? /^([^:\s]+):(\d+)$/.exec(rest)
    if (!m?.[1] || !m[2]) return { ok: false, error: 'format', line: i + 1, text: line }
    addresses.push({ host: m[1], port: Number(m[2]) })
  }
  if (!addresses.length) return { ok: false, error: 'empty' }
  const transport = seen.size === 1 ? [...seen][0]! : null
  return { ok: true, addresses, transport }
}

/** host:port, with brackets around IPv6. */
export function hostPort(a: Address): string {
  return `${a.host.includes(':') ? `[${a.host}]` : a.host}:${a.port}`
}
