import { useTranslation } from 'react-i18next'
import { currentLanguage, languages, setLanguage, type Language } from '../i18n'

const names: Record<Language, string> = { en: 'EN', ru: 'RU' }

export function LanguageSelect() {
  const { t } = useTranslation()
  return (
    <select
      className="lang"
      aria-label={t('nav.language')}
      value={currentLanguage()}
      onChange={(e) => setLanguage(e.target.value as Language)}
    >
      {languages.map((l) => (
        <option key={l} value={l}>
          {names[l]}
        </option>
      ))}
    </select>
  )
}
