// TZ UI v5, Волна 2 — U17 (фото-лента + полноэкранный просмотр),
// U18 (порядок блоков по TZ v4 §6.4, включая D9 и D1), U19 (кнопка
// возврата при прямой ссылке).
//
// Приёмка:
// U17 — лента прокручивается по одному снимку (scroll-snap); счётчик
// соответствует фактическому числу; полноэкранный просмотр закрывается
// тремя способами (кнопка, Escape, тап по фону).
// U18 — объект с ОДНОЙ записью истории: блок истории не показывается
// вовсе (D9); объект без площади: строка «Не указано: …» (D1).
// U19 — при открытии объекта по прямой ссылке путь назад существует.
//
// Живой объект 946 — объект с полным фото-массивом (20 фото; TZ v4 §4.4:
// фото только у старых объектов). D9/D1 — мокапы полного объекта: нужное
// сочетание в живых данных встречается ненадёжно, а приёмка требует
// именно тест, не инспекцию одного объекта.
import { test, expect, type Page, type TestInfo } from '@playwright/test'
import fs from 'node:fs'
import path from 'node:path'

if (!fs.existsSync(path.join(process.cwd(), 'playwright.config.ts'))) {
  throw new Error('Playwright-тесты запускать из web/ (npm run test:ui)')
}

const T = '2026-09-09T12:00:00Z'

// Локаль проекта (390-light-ru → ru). Без пинга app рендерит дефолтную
// локаль (ru) во ВСЕХ проектах — en-проекты проверяли бы ru-рендер.
const isRu = (testInfo: TestInfo) => testInfo.project.name.endsWith('-ru')
function pinLocale(page: Page, testInfo: TestInfo) {
  page.addInitScript((l) => localStorage.setItem('pb.locale', l), isRu(testInfo) ? 'ru' : 'en')
}

// Полный объект для мокапов детальной (все блоки рендерятся).
function detailObject(over: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    id: 777,
    country: 'CZ',
    deal_type: 'sale',
    zone_id: 10,
    zone_name: 'Brno',
    zone_level: 'municipality',
    zone_source: 'cuzk',
    address: 'Sokolovská 43, Brno',
    area_sqm: 54,
    rooms: 3,
    property_type: 'apartment',
    seller_type: 'private',
    seller_name: null,
    // Деньги — минорные (CZK, экспонента 2): 850000000 = 8 500 000 CZK.
    price_minor: 850000000,
    currency: 'CZK',
    price_display: null,
    valuation: {
      model_version: 'v3.2',
      price_deviation: -0.023,
      null_reason: null,
      predicted_price_minor: 870000000,
      interval_low_minor: 820000000,
      interval_high_minor: 920000000,
      sample_size: 140,
      r_squared: 0.71,
      zone_fallback: false,
      computed_at: T,
    },
    not_modeled: false,
    hazard: null,
    status: 'active',
    delisted_reason: null,
    first_seen_at: T,
    last_seen_at: T,
    delisted_at: null,
    price_history: [],
    listings: [],
    ...over,
  }
}

function mockObject(page: Page, id: number, over: Record<string, unknown>) {
  void page.route(`**/api/objects/${id}`, (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify(detailObject({ id, ...over })),
    }),
  )
}

test('U17: лента фото на живом объекте — snap, счётчик, ленивые фото', async ({
  page,
}, testInfo) => {
  pinLocale(page, testInfo)
  await page.goto('/#/objects/946')
  await page.waitForSelector('.photo-feed', { timeout: 20_000 })

  const photos = page.locator('.photo-feed .photo')
  const total = await photos.count()
  expect(total, 'у живого объекта 946 нет фото').toBeGreaterThan(5)

  // Счётчик «текущее / всего» — фактическое число.
  await expect(page.locator('.photo-count')).toHaveText(`1 / ${total}`)

  // Листание по одному снимку: scroll-snap по горизонтальной оси.
  const snap = await page.locator('.photo-feed').evaluate((el) => getComputedStyle(el).scrollSnapType)
  expect(snap).toContain('x')
  expect(snap).toContain('mandatory')

  // Первые два фото — сразу, остальные — lazy (размер card, TZ v4 §4.4).
  const loadings = await photos.locator('img').evaluateAll((imgs) =>
    imgs.map((i) => i.getAttribute('loading')),
  )
  expect(loadings[0]).toBe('eager')
  expect(loadings[1]).toBe('eager')
  for (let i = 2; i < loadings.length; i++) expect(loadings[i]).toBe('lazy')

  // Прокрутка на один кадр — счётчик идёт.
  await page.locator('.photo-feed').evaluate((el) => el.scrollBy({ left: el.clientWidth }))
  await expect(page.locator('.photo-count')).toHaveText(`2 / ${total}`)
})

test('U17: полноэкранный просмотр закрывается тремя способами', async ({
  page,
}, testInfo) => {
  pinLocale(page, testInfo)
  await page.goto('/#/objects/946')
  await page.waitForSelector('.photo-feed', { timeout: 20_000 })
  const viewer = page.getByRole('dialog')

  // 1) Кнопка «Закрыть».
  await page.locator('.photo-feed .photo').first().click()
  await expect(viewer).toBeVisible()
  await expect(page.locator('.viewer-count')).toHaveText(/1 \//)
  await page.locator('.viewer-close').click()
  await expect(viewer).toBeHidden()

  // 2) Escape.
  await page.locator('.photo-feed .photo').first().click()
  await expect(viewer).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(viewer).toBeHidden()

  // 3) Тап по фону (letterbox): по счётору, не по фото и не по кнопке.
  await page.locator('.photo-feed .photo').first().click()
  await expect(viewer).toBeVisible()
  await page.locator('.viewer-count').click()
  await expect(viewer).toBeHidden()
})

test('U18: порядок блоков экрана объекта — фото, оценка, цена, …, служебные', async ({
  page,
}, testInfo) => {
  pinLocale(page, testInfo)
  await page.goto('/#/objects/946')
  await page.waitForSelector('.obj-head', { timeout: 20_000 })

  const heads = await page.locator('main section.block h2').allTextContents()
  const ru = isRu(testInfo)
  const expected = ru
    ? [
        'Фотографии',
        'Оценка',
        'Цена',
        'Уход с рынка',
        'Характеристики',
        'Расположение',
        'Продавец',
        'Объявление на площадке',
        'Служебные сведения',
      ]
    : [
        'Photos',
        'Valuation',
        'Price',
        'Market exit',
        'Attributes',
        'Location',
        'Seller',
        'Listing on the board',
        'Service information',
      ]
  expect(heads, 'порядок блоков ≠ TZ v4 §6.4 (U18)').toEqual(expected)
})

test('U18/D9: одна запись истории цены — блок истории не показывается', async ({
  page,
}, testInfo) => {
  pinLocale(page, testInfo)
  mockObject(page, 777, {
    price_history: [{ price_minor: 850000000, currency: 'CZK', change_at: T }],
  })
  await page.goto('/#/objects/777')
  await page.waitForSelector('.obj-head', { timeout: 20_000 })

  // Цена показана, но ОДНА запись — не история (D9, TZ v4 §6.4.3).
  await expect(page.locator('.price-now')).toBeVisible()
  expect(await page.locator('.hist').count(), 'блок истории при одной записи (D9)').toBe(0)
  expect(await page.locator('.hist-delta').count()).toBe(0)
})

test('U18/D9: две записи — блок истории показывается (контроль мокапа)', async ({
  page,
}, testInfo) => {
  pinLocale(page, testInfo)
  mockObject(page, 778, {
    price_history: [
      { price_minor: 880000000, currency: 'CZK', change_at: '2026-05-01T08:00:00Z' },
      { price_minor: 850000000, currency: 'CZK', change_at: T },
    ],
  })
  await page.goto('/#/objects/778')
  await page.waitForSelector('.obj-head', { timeout: 20_000 })

  expect(await page.locator('.hist').count(), 'история из двух записей не показана').toBe(1)
  expect(await page.locator('.hist-row').count()).toBe(2)
  await expect(page.locator('.hist-delta')).toBeVisible()
})

test('U18/D1: объект без площади — одна строка «Не указано: …»', async ({
  page,
}, testInfo) => {
  pinLocale(page, testInfo)
  mockObject(page, 888, { area_sqm: null, property_type: null })
  await page.goto('/#/objects/888')
  await page.waitForSelector('.obj-head', { timeout: 20_000 })

  const missing = page.locator('.not-specified')
  await expect(missing).toBeVisible()
  const text = (await missing.textContent())!
  expect(text, 'строка «Не указано: …» (D1)').toMatch(/Не указано:|Not specified:/)
  expect(text).toMatch(/Площадь|Area/)
  expect(text).toMatch(/Тип|Type/)
  // «Нет данных» построчно (старый формат) отсутствует.
  expect(text).not.toMatch(/нет данных|no data/i)
})

test('U19: прямая ссылка на объект — кнопка возврата существует', async ({ page }, testInfo) => {
  pinLocale(page, testInfo)
  await page.goto('/#/objects/946')
  await page.waitForSelector('.obj-head', { timeout: 20_000 })

  const back = page.locator('.obj-head .back-link')
  await expect(back).toBeVisible()
  expect(await back.getAttribute('href')).toBe('#/list')
  await expect(back).toContainText(isRu(testInfo) ? 'Объекты' : 'Objects')

  // Возврат ведёт на список.
  await back.click()
  await page.waitForSelector('.object-list .row, .empty', { timeout: 20_000 })
})
