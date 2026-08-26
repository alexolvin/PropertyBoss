import i18n from 'i18next'
import { initReactI18next } from 'react-i18next'
import ru from './locales/ru.json'
import en from './locales/en.json'

export const LOCALES = ['ru', 'en'] as const
export type Locale = (typeof LOCALES)[number]

const saved = localStorage.getItem('pb.locale')
const initial: Locale = saved === 'ru' || saved === 'en' ? saved : 'ru'

void i18n.use(initReactI18next).init({
  resources: {
    ru: { translation: ru },
    en: { translation: en },
  },
  lng: initial,
  fallbackLng: 'en',
  interpolation: { escapeValue: false },
})

export function setLocale(l: Locale): void {
  localStorage.setItem('pb.locale', l)
  void i18n.changeLanguage(l)
}

export default i18n
