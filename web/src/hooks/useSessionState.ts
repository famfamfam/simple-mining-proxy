import { useEffect, useState } from 'react'

// Storage can be missing or throw (private windows, blocked site data): the
// state then simply lives in memory.
function useStoredState<T>(storage: () => Storage, key: string, initial: T) {
  const [value, setValue] = useState<T>(() => {
    try {
      const saved = storage().getItem(key)
      return saved === null ? initial : (JSON.parse(saved) as T)
    } catch {
      return initial
    }
  })
  useEffect(() => {
    try {
      storage().setItem(key, JSON.stringify(value))
    } catch {
      // not remembered, nothing else to do
    }
  }, [storage, key, value])
  return [value, setValue] as const
}

const session = () => sessionStorage
const local = () => localStorage

/** State that survives switching screens (kept in sessionStorage). */
export function useSessionState<T>(key: string, initial: T) {
  return useStoredState(session, key, initial)
}

/** State that survives closing the browser (kept in localStorage). */
export function useLocalState<T>(key: string, initial: T) {
  return useStoredState(local, key, initial)
}
