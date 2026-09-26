import type { Pool } from '../api/types'

/** The name of the pool with this id, or the id itself when it is gone. */
export function poolNameIn(pools: Pool[], id: string): string {
  return pools.find((p) => p.id === id)?.name ?? id
}
