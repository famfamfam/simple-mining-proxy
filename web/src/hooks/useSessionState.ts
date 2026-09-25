import { useEffect, useState } from 'react'

/** State that survives switching screens (kept in sessionStorage). */
export function useSessionState<T>(key: string, initial: T) {
  const [value, setValue] = useState<T>(() => {
    try {
      const saved = sessionStorage.getItem(key)
      return saved === null ? initial : (JSON.parse(saved) as T)
    } catch {
      return initial
    }
  })
  useEffect(() => {
    sessionStorage.setItem(key, JSON.stringify(value))
  }, [key, value])
  return [value, setValue] as const
}
