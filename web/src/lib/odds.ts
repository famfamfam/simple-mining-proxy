/** Hashes behind difficulty 1 on SHA-256: a block at network difficulty D takes D × 2³² hashes on average. */
const HASHES_PER_DIFFICULTY = 2 ** 32

/** Blocks per second a hashrate (H/s) finds on average at a network difficulty. */
export function blockRate(hashrateHs: number, difficulty: number): number {
  return hashrateHs > 0 && difficulty > 0 ? hashrateHs / (difficulty * HASHES_PER_DIFFICULTY) : 0
}

/** Chance to find at least one block within the given seconds. Blocks are a Poisson process, so 1 − e^(−rate·t). */
export function blockChance(hashrateHs: number, difficulty: number, seconds: number): number {
  return -Math.expm1(-blockRate(hashrateHs, difficulty) * seconds)
}
