import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import type { SettingMeta, SettingValue } from '../../api/types'
import { bestUnit, parseGoDuration, toGoDuration, unitSize, unitsUpTo, type DurationUnit } from '../../lib/duration'
import { optionLabel } from './describe'

interface ControlProps {
  meta: SettingMeta
  value: SettingValue
  invalid: boolean
  onChange: (v: SettingValue) => void
}

/**
 * Keeps the text the operator is typing (it may be empty or incomplete)
 * while following outside changes of the value, such as a reset.
 */
function useDraftText(value: SettingValue, toText: (v: SettingValue) => string) {
  const [text, setText] = useState(() => toText(value))
  const [seen, setSeen] = useState(value)
  if (seen !== value) {
    setSeen(value)
    setText(toText(value))
  }
  return [text, setText] as const
}

const unitLabel: Record<DurationUnit, string> = { s: 'units.sName', m: 'units.minName', h: 'units.hName', d: 'units.dName' }

function DurationControl({ meta, value, invalid, onChange }: ControlProps) {
  const { t } = useTranslation()
  const units = unitsUpTo(parseGoDuration(String(meta.max ?? '0s')))
  const [unit, setUnit] = useState<DurationUnit>(() => bestUnit(parseGoDuration(String(value)), units))
  const [text, setText] = useDraftText(value, (v) => String(parseGoDuration(String(v)) / unitSize[unit]))
  const emit = (nextText: string, nextUnit: DurationUnit) => {
    if (nextText.trim() === '' || Number.isNaN(Number(nextText))) return
    onChange(toGoDuration(Number(nextText) * unitSize[nextUnit]))
  }
  return (
    <>
      <input
        type="number"
        min={0}
        step="any"
        inputMode="decimal"
        className={invalid ? 'invalid' : undefined}
        value={text}
        onChange={(e) => {
          setText(e.target.value)
          emit(e.target.value, unit)
        }}
      />
      <select
        aria-label={t('units.unit')}
        value={unit}
        onChange={(e) => {
          // Same value in the other unit: 120 s becomes 2 min.
          const next = e.target.value as DurationUnit
          setUnit(next)
          setText(String(parseGoDuration(String(value)) / unitSize[next]))
        }}
      >
        {units.map((u) => (
          <option key={u} value={u}>
            {t(unitLabel[u])}
          </option>
        ))}
      </select>
    </>
  )
}

function IntControl({ meta, value, invalid, onChange }: ControlProps) {
  const [text, setText] = useDraftText(value, String)
  return (
    <>
      <input
        type="number"
        step={1}
        inputMode="numeric"
        className={invalid ? 'invalid' : undefined}
        value={text}
        onChange={(e) => {
          setText(e.target.value)
          if (e.target.value.trim() !== '') onChange(Number(e.target.value))
        }}
      />
      {meta.unit && <span className="muted">{meta.unit}</span>}
    </>
  )
}

export function SettingControl(props: ControlProps) {
  const { t } = useTranslation()
  const { meta, value, invalid, onChange } = props
  switch (meta.type) {
    case 'duration':
      return <DurationControl {...props} />
    case 'int':
      return <IntControl {...props} />
    case 'enum':
      return (
        <select className={invalid ? 'invalid' : undefined} value={String(value)} onChange={(e) => onChange(e.target.value)}>
          {meta.options?.map((o) => (
            <option key={o} value={o}>
              {optionLabel(meta, o, t)}
            </option>
          ))}
        </select>
      )
    default:
      return (
        <input
          type="text"
          className={invalid ? 'invalid' : undefined}
          value={String(value)}
          onChange={(e) => onChange(e.target.value.trim())}
        />
      )
  }
}
