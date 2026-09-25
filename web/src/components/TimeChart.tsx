import { useEffect, useMemo, useRef, useState } from 'react'
import uPlot from 'uplot'
import 'uplot/dist/uPlot.min.css'
import { useDarkMode } from '../hooks/useDarkMode'
import { locale } from '../i18n'

export interface ChartSeries {
  label: string
  values: (number | null)[]
  /** Categorical slot 1–8 (--series-N); a pool keeps its slot in every range. */
  slot: number
  /** A light wash under the line: for a single series only. */
  fill?: boolean
  /** Hold each value until the next one (counts such as miners). */
  stepped?: boolean
}

interface TimeChartProps {
  title: string
  /** Unix seconds, one per value. */
  time: number[]
  series: ChartSeries[]
  /** Value text for the axis and the tooltip. */
  format: (v: number) => string
  height?: number
}

const css = (name: string) => getComputedStyle(document.documentElement).getPropertyValue(name).trim()

function withAlpha(hex: string, alpha: number): string {
  const n = parseInt(hex.replace('#', ''), 16)
  return `rgba(${(n >> 16) & 255}, ${(n >> 8) & 255}, ${n & 255}, ${alpha})`
}

/** Tick label for a time axis: clock time within a day, dates beyond. */
function tickLabel(t: number, incr: number, loc: string): string {
  const d = new Date(t * 1000)
  if (incr < 86400) return d.toLocaleTimeString(loc, { hour: '2-digit', minute: '2-digit' })
  if (incr < 28 * 86400) return d.toLocaleDateString(loc, { day: '2-digit', month: '2-digit' })
  return d.toLocaleDateString(loc, { month: 'short', year: '2-digit' })
}

/**
 * A line chart over time (uPlot on canvas): a crosshair snaps to the nearest
 * step and a tooltip lists every series there; nulls are gaps. With two or
 * more series a legend names them and toggles each one.
 */
export function TimeChart({ title, time, series, format, height = 220 }: TimeChartProps) {
  const box = useRef<HTMLDivElement>(null)
  const tip = useRef<HTMLDivElement>(null)
  const plot = useRef<uPlot | null>(null)
  const dark = useDarkMode()
  const [hidden, setHidden] = useState<Set<number>>(new Set())

  const data = useMemo(() => [time, ...series.map((s) => s.values)] as uPlot.AlignedData, [time, series])
  // The chart is rebuilt only when the set of series or the theme changes;
  // new data goes through setData.
  const shape = series.map((s) => `${s.label}:${s.slot}:${s.fill}:${s.stepped}`).join('|')
  const latest = useRef({ data, series, format })
  useEffect(() => {
    latest.current = { data, series, format }
  })

  useEffect(() => {
    const el = box.current
    if (!el) return
    const loc = locale()
    const colors = latest.current.series.map((s) => css(`--series-${s.slot}`))
    const muted = css('--muted')
    const grid = css('--border')
    const surface = css('--panel')
    const font = '12px system-ui, sans-serif'

    const showTip = (u: uPlot) => {
      const t = tip.current
      const i = u.cursor.idx
      if (!t) return
      if (i == null || u.cursor.left == null || u.cursor.left < 0) {
        t.hidden = true
        return
      }
      const { series: current, format: fmt, data: d } = latest.current
      const rows: HTMLElement[] = []
      const head = document.createElement('div')
      head.className = 'chart-tip-time'
      head.textContent = new Date((d[0][i] ?? 0) * 1000).toLocaleString(loc, {
        day: '2-digit', month: '2-digit', hour: '2-digit', minute: '2-digit',
      })
      rows.push(head)
      current.forEach((s, k) => {
        if (!u.series[k + 1]?.show) return
        const v = d[k + 1]?.[i]
        const row = document.createElement('div')
        row.className = 'chart-tip-row'
        const key = document.createElement('span')
        key.className = 'line-key'
        key.style.background = colors[k] ?? muted
        const value = document.createElement('b')
        value.textContent = v == null ? '—' : fmt(v)
        const label = document.createElement('span')
        label.className = 'muted'
        label.textContent = s.label
        row.append(key, value, label)
        rows.push(row)
      })
      t.replaceChildren(...rows)
      t.hidden = false
      // Beside the crosshair, flipped left near the right edge.
      const x = u.over.offsetLeft + u.cursor.left
      const flip = x + t.offsetWidth + 16 > el.clientWidth
      t.style.left = `${flip ? x - t.offsetWidth - 12 : x + 12}px`
      t.style.top = `${u.over.offsetTop + 8}px`
    }

    const opts: uPlot.Options = {
      width: el.clientWidth,
      height,
      legend: { show: false },
      padding: [12, 8, 0, 0],
      cursor: {
        y: false,
        drag: { x: false, y: false },
        points: {
          size: 8,
          width: 2,
          stroke: () => surface,
          fill: (_u, k) => colors[k - 1] ?? muted,
        },
      },
      scales: {
        x: { time: true },
        y: { range: (_u, _min, max) => [0, max > 0 ? max * 1.1 : 1] },
      },
      axes: [
        {
          stroke: muted,
          font,
          size: 28,
          grid: { stroke: grid, width: 1 },
          ticks: { show: false },
          values: (_u, splits, _axis, _space, incr) => splits.map((v) => tickLabel(v, incr, loc)),
        },
        {
          stroke: muted,
          font,
          size: 76,
          grid: { stroke: grid, width: 1 },
          ticks: { show: false },
          values: (_u, splits) => splits.map((v) => latest.current.format(v)),
        },
      ],
      series: [
        {},
        ...latest.current.series.map((s, k) => ({
          label: s.label,
          stroke: colors[k],
          width: 2,
          fill: s.fill && colors[k] ? withAlpha(colors[k], 0.1) : undefined,
          paths: s.stepped ? uPlot.paths.stepped?.({ align: 1 }) : undefined,
          points: { show: false },
        })),
      ],
      hooks: { setCursor: [showTip] },
    }
    const u = new uPlot(opts, latest.current.data, el)
    plot.current = u
    const resize = new ResizeObserver(() => u.setSize({ width: el.clientWidth, height }))
    resize.observe(el)
    const hideTip = () => {
      if (tip.current) tip.current.hidden = true
    }
    u.over.addEventListener('mouseleave', hideTip)
    return () => {
      resize.disconnect()
      u.destroy()
      plot.current = null
    }
  }, [shape, height, dark])

  useEffect(() => {
    plot.current?.setData(data)
  }, [data])

  // A new set of series starts with every one visible.
  const [shapeSeen, setShapeSeen] = useState(shape)
  if (shapeSeen !== shape) {
    setShapeSeen(shape)
    setHidden(new Set())
  }

  const toggle = (k: number) => {
    const next = new Set(hidden)
    if (next.has(k)) next.delete(k)
    else next.add(k)
    setHidden(next)
    plot.current?.setSeries(k + 1, { show: !next.has(k) })
  }

  return (
    <figure className="chart" aria-label={title}>
      <figcaption className="chart-title">{title}</figcaption>
      {series.length > 1 && (
        <div className="chart-legend">
          {series.map((s, k) => (
            <button
              key={s.label}
              type="button"
              className={hidden.has(k) ? 'legend-item off' : 'legend-item'}
              aria-pressed={!hidden.has(k)}
              onClick={() => toggle(k)}
            >
              <span className="line-key" style={{ background: `var(--series-${s.slot})` }} />
              {s.label}
            </button>
          ))}
        </div>
      )}
      <div className="chart-plot" ref={box}>
        <div className="chart-tip" ref={tip} hidden />
      </div>
    </figure>
  )
}
