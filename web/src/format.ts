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
  // Минимальное число знаков, сохраняющее значение (макет Этапа 2:
  // «1 250 000 Kč» без копеек у целых цен; EUR с копейками — «850,50 €»).
  const opts = { minimumFractionDigits: 0, maximumFractionDigits: exponent }
  try {
    return new Intl.NumberFormat(locale, { style: 'currency', currency, ...opts }).format(value)
  } catch {
    // Неизвестный код валюты для Intl — честный fallback, не молчаливый
    return `${new Intl.NumberFormat(locale, opts).format(value)} ${currency}`
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

/**
 * Цена за квадратный метр (ТЗ v4 §6.3: «цена и цена за квадратный метр»):
 * целые минорные / площадь, округление до целых минорных; формат — по
 * локали, единица (м²) подставляется вызывающим (локализация).
 */
export function formatPricePerSqm(
  minor: number,
  currency: string,
  exponent: number,
  locale: string,
  areaSqM: number,
  sqmLabel: string,
): string {
  if (areaSqM <= 0) return ''
  const perSqm = Math.round(minor / areaSqM)
  return `${formatMoney(perSqm, currency, exponent, locale)}/${sqmLabel}`
}

/** Доля (−0.023) → процент со знаком (−2.3 %), по локали (ТЗ §7.4). */
export function formatPercent(fraction: number, locale: string): string {
  return new Intl.NumberFormat(locale, {
    style: 'percent',
    signDisplay: 'always',
    maximumFractionDigits: 1,
  }).format(fraction)
}
