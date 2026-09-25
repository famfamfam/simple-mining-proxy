import { useEffect, useState } from 'react'

const query = '(prefers-color-scheme: dark)'

/** Follows the system color scheme; charts redraw with the other palette. */
export function useDarkMode(): boolean {
  const [dark, setDark] = useState(() => window.matchMedia(query).matches)
  useEffect(() => {
    const m = window.matchMedia(query)
    const onChange = () => setDark(m.matches)
    m.addEventListener('change', onChange)
    return () => m.removeEventListener('change', onChange)
  }, [])
  return dark
}
