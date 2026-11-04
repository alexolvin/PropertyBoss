import { test, expect } from '@playwright/test'

// TZ UI v5 U07 — шторка фильтров на примитиве Dialog (WAI-ARIA APG):
// при открытии фокус на первом управляющем элементе; Tab/Shift+Tab
// циклят внутри; Escape закрывает; при закрытии фокус возвращается
// вызвавшей кнопке «Фильтры».
// TZ UI v5 U08 — переключение языка UI меняет documentElement.lang.

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

test('U07: фокус, Tab-цикл, Escape, возврат фокуса', async ({ page }) => {
  await page.goto('/#/list')
  await page.waitForSelector('.object-list .row, .empty')
  const filterBtn = page.locator('.filter-btn[aria-haspopup="dialog"]')
  await filterBtn.click()
  const sheet = page.locator('.sheet[role="dialog"]')
  await expect(sheet).toBeVisible()
  // При открытии — фокус на первом управляющем элементе.
  await expect(page.locator('.sheet select').first()).toBeFocused()
  // Tab от последнего элемента возвращает на первый.
  await page.locator('.sheet-actions .btn-primary').focus()
  await page.keyboard.press('Tab')
  await expect(page.locator('.sheet select').first()).toBeFocused()
  // Shift+Tab от первого элемента возвращает на последний.
  await page.keyboard.press('Shift+Tab')
  await expect(page.locator('.sheet-actions .btn-primary')).toBeFocused()
  // Escape закрывает.
  await page.keyboard.press('Escape')
  await expect(sheet).toHaveCount(0)
  // document.activeElement — кнопка «Фильтры».
  await expect(filterBtn).toBeFocused()
})

test('U08: переключение на EN меняет html lang', async ({ page }) => {
  await page.goto('/#/settings')
  await page.waitForSelector('.settings-block')
  expect(await page.evaluate(() => document.documentElement.lang)).toBe('ru')
  await page.getByLabel('Язык').selectOption('en')
  expect(await page.evaluate(() => document.documentElement.lang)).toBe('en')
})
