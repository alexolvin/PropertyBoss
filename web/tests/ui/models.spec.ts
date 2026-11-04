import { test, expect } from '@playwright/test'

// D2 (docs/DEFECTS.md, с ТЗ v4): «Значение приклеено к подписи без
// разделителя»: «Чехия — продажа8 221», «Bazoš — Reality (…
// reality.bazos.cz)активен».
// Приёмка: зазор между именем пары/источника и числом/статусом (.pair-st)
// на одной строке > 0. До фикса — 0 px (зонд tmp_probe_d2.mjs: все 7 пар
// Модели приклеены). Пара «отклонение + n=» в списке — не D2: .row-right —
// flex-column, значения на отдельных строках (зонд tmp_devs4.mjs).

test.use({
  viewport: { width: 390, height: 844 },
  colorScheme: 'light',
  locale: 'ru-RU',
  isMobile: true,
  hasTouch: true,
})

test.beforeEach(async ({ page }) => {
  await page.addInitScript(() => localStorage.setItem('pb.locale', 'ru'))
})

// Зазор (px) от конца имени (без хвостовых пробелов — разделителя) до
// .pair-st; перенос на другую строку — не случай D2, возвращается null.
const gapBeforeSpan = (els: Element[]) =>
  els.map((el) => {
    const span = el.querySelector('.pair-st')
    if (!span) return null
    const tn = el.firstChild
    if (!tn || tn.nodeType !== Node.TEXT_NODE) return null
    const len = tn.data.replace(/\s+$/, '').length
    const r = document.createRange()
    r.setStart(tn, 0)
    r.setEnd(tn, len)
    const a = r.getBoundingClientRect()
    const b = span.getBoundingClientRect()
    if (Math.abs(b.top - a.top) > 1) return null
    return { text: el.textContent, gap: b.left - a.right }
  })

test('D2: имя пары и число/статус — с разделителем (Модели)', async ({ page }) => {
  await page.goto('/#/models')
  await page.waitForSelector('.block')
  const gaps = await page.$$eval('.pair-name', gapBeforeSpan)
  const flat = gaps.filter(Boolean) as { text: string; gap: number }[]
  // И пары, и источники — с .pair-st; хотя бы одна должна быть.
  expect(flat.length, 'на экране Модели нет .pair-name с .pair-st').toBeGreaterThan(0)
  for (const g of flat) {
    expect(g.gap, `D2: значение приклеено к имени: «${g.text}»`).toBeGreaterThan(0)
  }
})
