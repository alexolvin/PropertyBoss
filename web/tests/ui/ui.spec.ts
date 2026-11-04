// TZ UI v5 §10 (U41/U42) — гейт-версия инвариантов на фикстурном API
// (C35 / D31). 5 маршрутов × 12 проектов. Живой корпус — ui_live.spec.ts
// (make ui-live, не гейт).
//
// Фикстура — враждебный корпус: самый длинный адрес, максимальная цена,
// total доминирует живой (99 999 > 10 530), объект без фото и без площади,
// самая длинная строка локали (hazard insufficient_history).
import { test, expect, type Page } from '@playwright/test'
import fs from 'node:fs'
import path from 'node:path'
import {
  assertNoHorizontalOverflow,
  assertTargetSizes,
  assertNoTruncation,
  assertLang,
  assertPagerSingleLine,
  runAxe,
} from './helpers'

if (!fs.existsSync(path.join(process.cwd(), 'playwright.config.ts'))) {
  throw new Error('Playwright-тесты запускать из web/ (npm run test:ui)')
}
const SHOT_ROOT = path.join(process.cwd(), '..', '.a', 'reports', 'ui')

// Замеры live-корпуса 10.09: адрес 34 символа, цена 45 149 458 400 minor,
// total 10 530. Фикстура доминирует корпус с запасом.
const HOSTILE_ADDRESS = 'HavlíčkováNabízíme, Havlíčkův Brod'
const HOSTILE_PRICE_MINOR = 45149458400
const HOSTILE_TOTAL = 99999
const T = '2026-09-09T12:00:00Z'

interface RouteDef {
  slug: string
  url: string
  wait: string
  pager?: boolean
}

const ROUTES: RouteDef[] = [
  { slug: 'objects', url: '/#/list', wait: '.object-list .row, .empty', pager: true },
  { slug: 'map', url: '/', wait: '.map-canvas, .map-page' },
  { slug: 'object', url: 'object', wait: '.obj-head' },
  { slug: 'models', url: '/#/models', wait: '.block' },
  { slug: 'calibration', url: '/#/models/calibration/CZ/sale', wait: '.calib-card' },
  { slug: 'settings', url: '/#/settings', wait: '.settings-block' },
]

function cover(n: number) {
  return {
    ord: n,
    original_url: 'https://reality.bazos.net/realty/946/',
    card_url: `/photos/946/${n}_card.webp`,
    list_url: `/photos/946/${n}_list.webp`,
  }
}

function valuation(over: Record<string, unknown> = {}) {
  return {
    model_version: 'hedonic-ridge-v5',
    price_deviation: -0.023,
    null_reason: null,
    predicted_price_minor: 870000000,
    interval_low_minor: 820000000,
    interval_high_minor: 920000000,
    sample_size: 140,
    r_squared: 0.71,
    zone_fallback: false,
    computed_at: T,
    ...over,
  }
}

function listObj(over: Record<string, unknown> = {}) {
  return {
    id: 1,
    country: 'CZ',
    deal_type: 'sale',
    zone_id: 10,
    zone_name: 'Brno',
    zone_level: 'municipality',
    zone_source: 'cuzk',
    address: 'Sokolovská 43/16, Brno, Centrum, ulice Sokolovská, 602 00',
    area_sqm: 54,
    rooms: 3,
    property_type: 'apartment',
    price_minor: 850000000,
    currency: 'CZK',
    price_display: null,
    valuation: valuation(),
    not_modeled: false,
    hazard: null,
    status: 'active',
    delisted_reason: null,
    first_seen_at: T,
    last_seen_at: T,
    delisted_at: null,
    ...over,
  }
}

const HOSTILE_HAZARD = {
  horizon_days: 30,
  probability: null,
  null_reason: 'insufficient_history',
  model_version: 'hedonic-ridge-v5',
  events_in_training: 42,
  computed_at: T,
}

const HOSTILE_OBJECT = listObj({
  id: 1,
  address: HOSTILE_ADDRESS,
  price_minor: HOSTILE_PRICE_MINOR,
  area_sqm: null,
  cover_photo: null,
  rooms: 4,
  property_type: 'house',
  zone_name: 'Havlíčkův Brod',
  seller_type: 'private',
  seller_name: null,
  valuation: null,
  hazard: HOSTILE_HAZARD,
  price_history: [
    { price_minor: 40000000000, currency: 'CZK', change_at: '2026-08-01T12:00:00Z' },
    { price_minor: HOSTILE_PRICE_MINOR, currency: 'CZK', change_at: T },
  ],
  listings: [
    {
      source_id: 'bazos-reality',
      source_name: 'Bazos Reality',
      external_id: '946',
      source_url: 'https://reality.bazos.net/realty/946/',
    },
  ],
})

const HOSTILE_LIST = {
  total: HOSTILE_TOTAL,
  page: 1,
  per_page: 20,
  objects: [
    HOSTILE_OBJECT,
    listObj({ id: 2, cover_photo: cover(2), seller_type: 'agency', seller_name: 'RE/MAX s.r.o.' }),
    listObj({ id: 3, cover_photo: null }),
    listObj({
      id: 4,
      cover_photo: cover(4),
      valuation: valuation({ zone_fallback: true, sample_size: 12 }),
    }),
    listObj({
      id: 5,
      cover_photo: cover(5),
      status: 'delisted',
      delisted_at: T,
      delisted_reason: 'sold',
      price_trend: 'reduced',
    }),
    listObj({ id: 6, cover_photo: cover(6), area_sqm: 54, seller_type: 'developer' }),
    listObj({ id: 7, cover_photo: cover(7), seller_type: null, seller_name: null }),
    listObj({ id: 8, cover_photo: cover(8), valuation: null, seller_type: null, seller_name: null }),
  ],
}

const DECILES = Array.from({ length: 10 }, (_, i) => ({
  decile: i + 1,
  predicted: (i + 0.5) / 10,
  actual: (i + 0.5) / 10,
  n: 100,
}))

const LIQUIDITY = {
  model_version: 'liq-v1',
  status: 'published' as const,
  horizon_days: 30,
  min_events: 100,
  n_completed_events: 1200,
  brier_score: 0.21,
  c_index: 0.71,
  max_calib_dev: 0.08,
  calibration: DECILES,
  computed_at: T,
}

const SOURCE = {
  id: 'bazos-reality',
  name: 'Bazos Reality',
  domain: 'reality.bazos.net',
  country: 'CZ',
  deal_types: ['sale'],
  kind: 'simple',
  state: 'active',
  last_complete_scan_at: T,
}

const META = {
  currencies: [
    { code: 'CZK', exponent: 2 },
    { code: 'EUR', exponent: 2 },
    { code: 'USD', exponent: 2 },
  ],
  countries: ['CZ', 'IT', 'NL'],
  market_currencies: { CZ: 'CZK', IT: 'EUR', NL: 'EUR' },
  deal_types: ['sale', 'rent'],
  fx_last_rate_date: '2026-09-09',
  // commit ≠ бандлу — строка сверки-предупреждения в Настройках рендерится.
  ui_build: { commit: '0000000', built_at: T },
  map: {
    style_url: 'https://tiles.openfreemap.org/styles/positron',
    points_max: 500,
    default_lng: 15.5,
    default_lat: 49.8,
    default_zoom: 7,
    zone_color_min_n: 30,
  },
}

const MODELS = {
  pairs: [
    {
      country: 'CZ',
      deal_type: 'sale',
      object_count: HOSTILE_TOTAL,
      deviation: {
        n_valued: 6029,
        n_with_deviation: 5170,
        model_version: 'hedonic-ridge-v5',
        r_squared: 0.4664,
        sample_size: 140,
        n_zone_fallback: 86,
      },
      liquidity: LIQUIDITY,
    },
  ],
  sources: [SOURCE],
}

function mockHostileAPI(page: Page) {
  const minStyle = {
    version: 8,
    sources: {},
    layers: [{ id: 'bg', type: 'background', paint: { 'background-color': '#f4f5f7' } }],
  }
  void page.route('**/tiles.openfreemap.org/**', (route) => {
    if (route.request().url().includes('/styles/')) {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(minStyle),
      })
    }
    return route.fulfill({ status: 204, body: '' })
  })
  void page.route('**/api/**', (route) => {
    const u = new URL(route.request().url())
    const p = u.pathname.replace(/\/+$/, '') || '/'
    const json = (body: unknown) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        headers: { Date: new Date(T).toUTCString() },
        body: JSON.stringify(body),
      })
    if (p.endsWith('/api/meta')) return json(META)
    if (/\/api\/objects\/\d+$/.test(p)) return json(HOSTILE_OBJECT)
    if (p.endsWith('/api/objects')) return json(HOSTILE_LIST)
    if (p.endsWith('/api/models')) return json(MODELS)
    if (p.endsWith('/api/liquidity')) {
      return json({
        model: {
          ...LIQUIDITY,
          country: 'CZ',
          deal_type: 'sale',
          n_person_periods: 5000,
          n_params: 8,
          train_cutoff_at: T,
          n_train: 800,
          n_test: 400,
          brier_decomp: { reliability: 0.02, resolution: 0.05, uncertainty: 0.2 },
          params: {},
        },
      })
    }
    if (p.endsWith('/api/search-configs')) return json([])
    if (p.endsWith('/api/sources')) return json([SOURCE])
    if (p.endsWith('/api/attribute-registry')) return json([])
    if (p.endsWith('/api/objects/map')) {
      return json({
        mode: 'points',
        total: 2,
        points: [
          { id: 1, lat: 50.05, lng: 14.44, dev_class: 'below', has_valuation: true },
          { id: 2, lat: 50.06, lng: 14.45, dev_class: 'none', has_valuation: false },
        ],
        cells: [],
        areas: [],
      })
    }
    if (p.includes('/api/zones/geojson')) {
      return json({ type: 'FeatureCollection', features: [] })
    }
    if (/\/api\/zones\/\d+$/.test(p)) {
      return json({
        zone: { id: 1, country: 'CZ', level: 'municipality', name: 'Praha', source: 'cuzk' },
        n_objects: 10,
        median_price_minor: 500000000,
        avg_price_minor: 510000000,
        avg_ppsm_minor: 10000000,
        currency: 'CZK',
        trend_down_share: null,
        trend_reason: 'insufficient_history',
        trend_n: 0,
        zone_color_min_n: 30,
      })
    }
    if (p.endsWith('/api/zones')) {
      return json({ total: 0, page: 1, per_page: 50, zones: [], sources: [] })
    }
    return route.fallback()
  })
}

for (const r of ROUTES) {
  test(r.slug, async ({ page }, testInfo) => {
    const [width, scheme, locale] = testInfo.project.name.split('-') as [
      string,
      'light' | 'dark',
      'ru' | 'en',
    ]

    await page.addInitScript((l) => localStorage.setItem('pb.locale', l), locale)
    mockHostileAPI(page)

    if (r.url === 'object') {
      await page.goto('/#/objects/1')
    } else {
      await page.goto(r.url)
    }

    await page.waitForSelector(r.wait, { timeout: 20_000 })
    expect(await page.locator('main > .error').count(), 'маршрут в состоянии ошибки').toBe(0)

    const shot = path.join(
      SHOT_ROOT,
      new Date().toISOString().slice(0, 10),
      `${r.slug}_${width}_${scheme}_${locale}.png`,
    )
    await page.screenshot({ path: shot, fullPage: false })

    await assertNoHorizontalOverflow(page)
    await assertTargetSizes(page)
    await assertNoTruncation(page)
    await assertLang(page, locale)
    if (r.pager) await assertPagerSingleLine(page)
    await runAxe(page)
  })
}
