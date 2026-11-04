// TZ UI v5, Волна 2 — U21 (кривая калибровки под узкие экраны) +
// U20 (значение точки — не только в <title>).
//
// Приёмка U21: на 320 px горизонтального скролла внутри блока нет
// (min-width убран, кегль подписей 13 px в системе 380 единиц — на 320
// фактические ≥ 9.5 px, до фикса 8.35); значение точки достижимо
// касанием (невидимая hit-зона r=12 → строка под графиком).
//
// /api/models мокапится: пара CZ/sale с published-моделью ликвидности
// и 10 децилями — кривая рендерится только в этом состоянии.
import { test, expect, type Page, type TestInfo } from '@playwright/test'
import fs from 'node:fs'
import path from 'node:path'

if (!fs.existsSync(path.join(process.cwd(), 'playwright.config.ts'))) {
  throw new Error('Playwright-тесты запускать из web/ (npm run test:ui)')
}

const isRu = (testInfo: TestInfo) => testInfo.project.name.endsWith('-ru')

// Локаль проекта (…-ru → ru). Без пинга app рендерит дефолтную локаль (ru)
// во ВСЕХ проектах — en-проекты проверяли бы ru-рендер (D-аналог: 6 падений
// U21/U20 на «Decile 1», полученном «Дециль 1»).
function pinLocale(page: Page, testInfo: TestInfo) {
  page.addInitScript((l) => localStorage.setItem('pb.locale', l), isRu(testInfo) ? 'ru' : 'en')
}

const DECILES = Array.from({ length: 10 }, (_, i) => ({
  decile: i + 1,
  predicted: (i + 0.5) / 10,
  actual: Math.min(1, Math.max(0, (i + 0.5) / 10 + (i % 2 ? 0.03 : -0.02))),
  n: 50,
}))

const MODELS = {
  pairs: [
    {
      country: 'CZ',
      deal_type: 'sale',
      object_count: 10000,
      deviation: {
        n_valued: 5000,
        n_with_deviation: 4200,
        model_version: 'v3.2',
        r_squared: 0.71,
        sample_size: 140,
        n_zone_fallback: 12,
      },
      liquidity: {
        model_version: 'lq-v1',
        status: 'published',
        horizon_days: 30,
        min_events: 100,
        n_completed_events: 500,
        brier_score: 0.12,
        c_index: 0.8,
        max_calib_dev: 0.02,
        calibration: DECILES,
        computed_at: '2026-09-09T12:00:00Z',
      },
    },
  ],
  sources: [],
}

function mockModels(page: Page) {
  void page.route('**/api/models', (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(MODELS) }),
  )
}

test('U21: 320 px — без горизонтального скролла в блоке, кегль читаемый', async ({ page }, testInfo) => {
  if (!testInfo.project.name.startsWith('320')) return // приёмка — 320 (E18)
  mockModels(page)
  pinLocale(page, testInfo)
  await page.goto('/#/models')
  await page.waitForSelector('.calib-svg', { timeout: 20_000 })

  // Горизонтального скролла внутри .chart-wrap нет (min-width убран).
  const m = await page.locator('.chart-wrap').evaluate((el) => ({
    scrollW: el.scrollWidth,
    clientW: el.clientWidth,
  }))
  expect(m.scrollW, `горизонтальный скролл внутри .chart-wrap (U21): ${m.scrollW} > ${m.clientW}`)
    .toBeLessThanOrEqual(m.clientW)

  // Кегль подписей — экранный (не user-единицы viewBox: getComputedStyle
  // на SVG-text возвращает кегль в системе viewBox, а не на экране):
  // кегль в единицах × (фактическая ширина svg / ширина viewBox).
  // 13 ед. в 380-системе → ≥ 9.5 px на 320 (до фикса 11 ед. → 8.35 px).
  const px = await page.locator('.calib-label').first().evaluate((el) => {
    const svg = el.ownerSVGElement!
    const vb = svg.viewBox.baseVal
    const rendered = svg.getBoundingClientRect().width
    return parseFloat(getComputedStyle(el).fontSize) * (rendered / vb.width)
  })
  expect(px, `подпись кривой ${px} px < 9.5 px (U21/E18)`).toBeGreaterThan(9.5)
})

test('U21/U20: значение точки — по касанию, строкой под графиком', async ({ page }, testInfo) => {
  mockModels(page)
  pinLocale(page, testInfo)
  await page.goto('/#/models')
  await page.waitForSelector('.calib-svg', { timeout: 20_000 })

  // Hit-зоны — на все 10 точек (невидимые, r≈12).
  const hits = page.locator('.calib-hit')
  expect(await hits.count(), 'hit-зон меньше, чем децилей (U21)').toBe(10)

  // Тап по первой точке — значение строкой (не только <title>).
  await hits.nth(0).click()
  const tip = page.locator('.calib-tip')
  await expect(tip).toBeVisible()
  await expect(tip).toContainText(isRu(testInfo) ? 'Дециль 1' : 'Decile 1')
  await expect(tip).toContainText('n=50')

  // Повторный тап — значение сворачивается.
  await hits.nth(0).click()
  await expect(tip).toBeHidden()
})
