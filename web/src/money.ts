// Целочисленная арифметика для денег в UI (ТЗ §5): сумма — минорные единицы.
// Преобразование «основные ↔ минорные» — только целыми, без float.

const MAX_INT_PART = 13 // 10^13 × 10^2 = 10^15 < 2^53 — точность Number гарантирована

/** "1234,56" | "1234.56" | "1234" → минорные (целое). null — не распарсилось. */
export function toMinor(input: string, exponent: number): number | null {
  const s = input.trim().replace(',', '.')
  if (!/^\d+(\.\d+)?$/.test(s)) return null
  const [ip, fp = ''] = s.split('.')
  if (ip.length > MAX_INT_PART) return null
  if (fp.length > exponent) return null
  const frac = fp === '' ? 0 : Number(fp.padEnd(exponent, '0'))
  return Number(ip) * 10 ** exponent + frac
}

/** Минорные → строка основных единиц для полей ввода: 123456 → "1234.56". */
export function fromMinor(minor: number | null, exponent: number): string {
  if (minor === null) return ''
  const base = 10 ** exponent
  const ip = Math.trunc(minor / base)
  const fp = minor % base
  return exponent > 0 ? `${ip}.${String(fp).padStart(exponent, '0')}` : String(ip)
}
