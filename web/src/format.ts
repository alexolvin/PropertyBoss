// Форматирование по локали (ТЗ §11: локализация форматов чисел и валют
// — по локали, не строковой конкатенацией).
// Сумма приходит целым в минорных единицах (ТЗ §5).

export function formatMoney(
  minor: number | null,
  currency: string | null,
  exponent: number,
  locale: string,
): string {
  if (minor === null || currency === null) return '—'
  const value = minor / 10 ** exponent
  try {
    return new Intl.NumberFormat(locale, {
      style: 'currency',
      currency,
      minimumFractionDigits: exponent,
      maximumFractionDigits: exponent,
    }).format(value)
  } catch {
    // Неизвестный код валюты для Intl — честный fallback, не молчаливый
    return `${new Intl.NumberFormat(locale, {
      minimumFractionDigits: exponent,
      maximumFractionDigits: exponent,
    }).format(value)} ${currency}`
  }
}

export function formatDate(iso: string | null, locale: string): string {
  if (!iso) return '—'
  return new Intl.DateTimeFormat(locale, { dateStyle: 'medium' }).format(new Date(iso))
}

export function formatDateTime(iso: string | null, locale: string): string {
  if (!iso) return '—'
  return new Intl.DateTimeFormat(locale, { dateStyle: 'medium', timeStyle: 'short' }).format(
    new Date(iso),
  )
}
