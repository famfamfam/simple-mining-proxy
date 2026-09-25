import { type RangeKey, rangeSeconds } from '../lib/ranges'
import { useSessionState } from './useSessionState'

/** The chosen chart period of a screen, kept while switching screens. */
export function useRange(storageKey: string, keys: readonly RangeKey[], initial: RangeKey) {
  const [saved, setRange] = useSessionState<RangeKey>(storageKey, initial)
  const range = keys.includes(saved) ? saved : initial
  return { range, seconds: rangeSeconds[range], setRange }
}
