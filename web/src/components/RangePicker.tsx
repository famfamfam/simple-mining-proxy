import { useTranslation } from 'react-i18next'
import type { RangeKey } from '../lib/ranges'

interface RangePickerProps {
  keys: readonly RangeKey[]
  value: RangeKey
  onChange: (key: RangeKey) => void
}

/** Chips that choose the period of the charts. */
export function RangePicker({ keys, value, onChange }: RangePickerProps) {
  const { t } = useTranslation()
  return (
    <div className="toolbar" role="group" aria-label={t('stats.range')}>
      {keys.map((key) => (
        <button
          key={key}
          type="button"
          className={value === key ? 'chip active' : 'chip'}
          aria-pressed={value === key}
          onClick={() => onChange(key)}
        >
          {t(`stats.ranges.${key}`)}
        </button>
      ))}
    </div>
  )
}
