// TZ UI v5 Волна 4 — U31–U37: карта, лист, связка, поиск.
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
  map: {
    style_url: 'https://tiles.openfreemap.org/styles/positron',
    points_max: 500,
    default_lng: 14.4,
    default_lat: 50.05,
    default_zoom: 11,
    zone_color_min_n: 30,
  },
}

const MAP = {
  mode: 'points',
  total: 2,
  points: [
    { id: 1, lat: 50.051, lng: 14.441, dev_class: 'below', has_valuation: true },
    { id: 2, lat: 50.062, lng: 14.452, dev_class: 'none', has_valuation: false },
  ],
  cells: [],
  areas: [],
}

const OBJ = {
  id: 1,
  country: 'CZ',
  deal_type: 'sale',
  zone_id: 1,
  zone_name: 'Praha',
  address: 'Test 1',
  area_sqm: 54,
  rooms: 2,
  price_minor: 850000000,
  currency: 'CZK',
  price_display: null,
  valuation: { price_deviation: -0.1, interval_low_minor: 1, interval_high_minor: 2, sample_size: 10 },
  not_modeled: false,
  hazard: null,
  status: 'active',
  lat: 50.051,
  lng: 14.441,
  geom_kind: 'point',
  geo_confidence: 'street',
  first_seen_at: '2026-09-09T12:00:00Z',
  last_seen_at: '2026-09-09T12:00:00Z',
  delisted_at: null,
  delisted_reason: null,
  cover_photo: null,
}

const MIN_STYLE = {
  version: 8,
  sources: {},
  layers: [{ id: 'bg', type: 'background', paint: { 'background-color': '#f4f5f7' } }],
}

function mockMap(page: Page) {
  void page.route('**/tiles.openfreemap.org/**', (route) => {
    if (route.request().url().includes('/styles/')) {
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(MIN_STYLE) })
    }
    return route.fulfill({ status: 204, body: '' })
  })
  void page.route('**/api/**', (route) => {
    const p = new URL(route.request().url()).pathname.replace(/\/+$/, '')
    const json = (body: unknown) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(body) })
    if (p.endsWith('/api/meta')) return json(META)
    if (p.endsWith('/api/objects/map')) return json(MAP)
    if (/\/api\/objects\/\d+$/.test(p)) return json(OBJ)
    if (p.endsWith('/api/objects')) return json({ total: 2, page: 1, per_page: 50, objects: [OBJ] })
    if (p.includes('/api/zones/geojson')) return json({ type: 'FeatureCollection', features: [] })
    return route.fulfill({ status: 404, body: 'no' })
  })
}

test('U31: карта — раздел по умолчанию, canvas и легенда с процентами', async ({ page }, testInfo) => {
  pinLocale(page, testInfo)
  mockMap(page)
  await page.goto('/')
  await page.waitForSelector('.map-page', { timeout: 20_000 })
  await expect(page.locator('.map-canvas')).toBeVisible()
  await expect(page.locator('.map-legend')).toContainText('15')
  await expect(page.locator('.tabbar a').first()).toHaveClass(/active/)
})

test('U33: три положения листа — касание ручки', async ({ page }, testInfo) => {
  pinLocale(page, testInfo)
  mockMap(page)
  await page.goto('/')
  await page.waitForSelector('.sheet-handle', { timeout: 20_000 })
  const sheet = page.locator('.map-sheet')
  await expect(sheet).toHaveClass(/collapsed/)
  await page.locator('.sheet-handle').click()
  await expect(sheet).toHaveClass(/mid/)
  await page.locator('.sheet-handle').click()
  await expect(sheet).toHaveClass(/expanded/)
  await page.locator('.sheet-handle').click()
  await expect(sheet).toHaveClass(/collapsed/)
})

test('U35: метка → лист mid с карточкой', async ({ page }, testInfo) => {
  pinLocale(page, testInfo)
  mockMap(page)
  await page.goto('/')
  await page.waitForSelector('.map-pin', { timeout: 20_000 })
  await page.locator('.map-pin').first().click()
  await expect(page.locator('.map-sheet')).toHaveClass(/mid/)
  await expect(page.locator('.sheet-card')).toBeVisible()
})

test('U35: строка списка → лист mid', async ({ page }, testInfo) => {
  pinLocale(page, testInfo)
  mockMap(page)
  await page.goto('/')
  await page.waitForSelector('.sheet-handle', { timeout: 20_000 })
  await page.locator('.sheet-handle').click()
  await page.locator('.sheet-handle').click()
  await expect(page.locator('.map-sheet')).toHaveClass(/expanded/)
  await page.waitForSelector('.sheet-list .row', { timeout: 20_000 })
  await page.locator('.sheet-list .row').first().click()
  await expect(page.locator('.map-sheet')).toHaveClass(/mid/)
})

test('U36: кнопки управления ≥ 44 px', async ({ page }, testInfo) => {
  pinLocale(page, testInfo)
  mockMap(page)
  await page.goto('/')
  await page.waitForSelector('.map-ctrl', { timeout: 20_000 })
  const boxes = await page.locator('.map-ctrl').evaluateAll((els) =>
    els.map((el) => {
      const r = el.getBoundingClientRect()
      return { w: r.width, h: r.height }
    }),
  )
  for (const b of boxes) {
    expect(b.h, JSON.stringify(b)).toBeGreaterThanOrEqual(44)
  }
})

test('U37: поиск по адресу раскрывает лист', async ({ page }, testInfo) => {
  pinLocale(page, testInfo)
  mockMap(page)
  await page.goto('/')
  await page.waitForSelector('.map-search input', { timeout: 20_000 })
  await page.locator('.map-search input').fill('Test')
  await page.locator('.map-search input').press('Enter')
  await expect(page.locator('.map-sheet')).toHaveClass(/expanded/)
})
