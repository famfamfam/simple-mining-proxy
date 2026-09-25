import i18n from 'i18next'
import LanguageDetector from 'i18next-browser-languagedetector'
import { initReactI18next } from 'react-i18next'
import en from './locales/en.json'
import ru from './locales/ru.json'

export const languages = ['en', 'ru'] as const
export type Language = (typeof languages)[number]

const storageKey = 'lang'

// The language is the one the operator picked (kept in localStorage) or the
// first supported one in the browser's preference list; English otherwise.
// Only an explicit choice is stored, so a browser setting keeps applying.
void i18n
  .use(LanguageDetector)
  .use(initReactI18next)
  .init({
    resources: { en: { translation: en }, ru: { translation: ru } },
    supportedLngs: languages,
    nonExplicitSupportedLngs: true,
    load: 'languageOnly',
    fallbackLng: 'en',
    detection: { order: ['localStorage', 'navigator'], lookupLocalStorage: storageKey, caches: [] },
    interpolation: { escapeValue: false },
  })

const syncDocument = (lng: string) => {
  document.documentElement.lang = lng
}
syncDocument(i18n.resolvedLanguage ?? 'en')
i18n.on('languageChanged', syncDocument)

export function currentLanguage(): Language {
  return i18n.resolvedLanguage === 'ru' ? 'ru' : 'en'
}

export function setLanguage(lng: Language) {
  localStorage.setItem(storageKey, lng)
  void i18n.changeLanguage(lng)
}

/** BCP 47 locale for numbers and dates in the current language. */
export function locale(): string {
  return currentLanguage() === 'ru' ? 'ru-RU' : 'en-GB'
}

export default i18n
