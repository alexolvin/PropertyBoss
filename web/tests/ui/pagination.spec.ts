import { test, expect } from '@playwright/test'
import { assertPagerSingleLine } from './helpers'

// TZ UI v5 U14 (E12) — установлено: не работала НЕ смена маршрута
// (scrollTo на hashchange в App.tsx реализован), а смена страницы
// пагинации ВНУТРИ списка: пагинация не меняет маршрут, поэтому
// скролл к началу списка при смене страницы — явный (goPage).
// Этот тест падает на дереве до U14 (scrollY остаётся на месте).
//
// TZ UI v5 U11 — приёмка: на 320 px и 360 px pager занимает одну
// строку, aria-label присутствует. 320 покрывается прогоном U41
// (assertPagerSingleLine в ui.spec); здесь — 360 + проверка, что
// контейнерный запрос переключил подписи на стрелки.

test.use({
  viewport: { width: 430, height: 932 },
  colorScheme: 'light',
  locale: 'ru-RU',
  isMobile: true,
  hasTouch: true,
})

test.beforeEach(async ({ page }) => {
  await page.addInitScript(() => localStorage.setItem('pb.locale', 'ru'))
})

test('U14: смена страницы — скролл к началу списка', async ({ page }) => {
  await page.goto('/#/list')
  await page.waitForSelector('.pager')
  const before = (await page.locator('.page-of').textContent()) ?? ''
  await page.evaluate(() => window.scrollTo(0, 600))
  expect(await page.evaluate(() => window.scrollY)).toBeGreaterThan(0)
  // Клик по «Следующая» (на первой странице «Предыдущая» — disabled).
  await page.locator('.pager button:not([disabled])').last().click()
  await expect(page.locator('.page-of')).not.toHaveText(before)
  expect(await page.evaluate(() => window.scrollY)).toBe(0)
})

test('U11: на 360 px pager в одну строку, стрелки с aria-label', async ({ page }) => {
  await page.setViewportSize({ width: 360, height: 800 })
  await page.goto('/#/list')
  await page.waitForSelector('.pager')
  // Узкий контейнер (< 400 px) — стрелки; полные подписи скрыты.
  await expect(page.locator('.pager .pager-arrow').first()).toBeVisible()
  await expect(page.locator('.pager .pager-word').first()).toBeHidden()
  // aria-label на кнопках — в обеих формах.
  await expect(page.locator('.pager button[aria-label="Предыдущая страница"]')).toHaveCount(1)
  await expect(page.locator('.pager button[aria-label="Следующая страница"]')).toHaveCount(1)
  await assertPagerSingleLine(page)
})
