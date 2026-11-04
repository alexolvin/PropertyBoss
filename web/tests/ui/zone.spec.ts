// TZ UI v5 Волна 5 — U39 карточка зоны.
import { test, expect, type Page, type TestInfo } from '@playwright/test'
import fs from 'node:fs'
import path from 'node:path'

if (!fs.existsSync(path.join(process.cwd(), 'playwright.config.ts'))) {
  throw new Error('Playwright-тесты запускать из web/ (npm run test:ui)')
}

const isRu = (testInfo: TestInfo) => testInfo.project.name.endsWith('-ru')
function pinLocale(page: Page, testInfo: TestInfo) {
  page.addInitScript((l) => localStorage.setItem('pb.locale', l), isRu(testInfo) ? 'ru' : 'en')
}

const META = {
  currencies: [{ code: 'CZK', exponent: 2 }],
  countries: ['CZ'],
  market_currencies: { CZ: 'CZK' },
  deal_types: ['sale'],
  fx_last_rate_date: null,
}

const ZONE = {
  zone: {
    id: 7,
    country: 'CZ',
    level: 'municipality',
    name: 'Havlíčkův Brod',
    source: 'cuzk',
    parent_name: 'Vysočina',
  },
  n_objects: 12,
  median_price_minor: 450000000,
  avg_price_minor: 460000000,
  avg_ppsm_minor: 8000000,
  currency: 'CZK',
  trend_down_share: null,
  trend_reason: 'insufficient_history',
  trend_n: 2,
  zone_color_min_n: 30,
}

test('U39: карточка зоны — N, медиана, средняя, м², тренд', async ({ page }, testInfo) => {
  pinLocale(page, testInfo)
  await page.route('**/api/**', (route) => {
    const p = new URL(route.request().url()).pathname
    const json = (body: unknown) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(body) })
    if (p.endsWith('/api/meta')) return json(META)
    if (p.includes('/api/zones/7')) return json(ZONE)
    return route.fulfill({ status: 404, body: 'no' })
  })
  await page.goto('/#/zones/7')
  await page.waitForSelector('.zone-card', { timeout: 20_000 })
  await expect(page.locator('.zone-card h1')).toContainText('Havlíčkův Brod')
  await expect(page.locator('.zone-card')).toContainText('12')
  await expect(page.locator('.dev-reason')).toBeVisible()
})
