// TZ UI v5, Волна 2 — U15 (обложка в строке списка) + U16 (пересборка
// строки по TZ v4 §6.3).
//
// Приёмка:
// U15 — CLS при прокрутке списка = 0; первые четыре обложки запрашиваются
// в сети без прокрутки (eager), с пятой — lazy; объект без фото даёт
// нейтральный плейсхолдер той же пропорции.
// U16 — не менее 4 строк на первом экране при 390 px (замерено и названо
// в аннотации); на 320/390/430 ни один ключевой элемент строки не
// обрезан; вся строка — одна ссылка, hit-area ≥ 44 px.
//
// Список — мокап: 8 объектов покрывают все состояния строки (обложка /
// плейсхолдер, уход с рынка, снижение цены, цена без площади, продавец,
// замещение зоны, отсутствие оценки). URL обложек — реальные файлы
// /photos/946/*_list.webp (у объекта 946 на сервере есть фото).
import { test, expect, type Page, type TestInfo } from '@playwright/test'
import fs from 'node:fs'
import path from 'node:path'
import { assertNoHorizontalOverflow, assertNoTruncation } from './helpers'

if (!fs.existsSync(path.join(process.cwd(), 'playwright.config.ts'))) {
  throw new Error('Playwright-тесты запускать из web/ (npm run test:ui)')
}

const T = '2026-09-09T12:00:00Z'
// C35: враждебная строка 0 — корпусный максимум адреса и цены, без площади.
const HOSTILE_ADDRESS = 'HavlíčkováNabízíme, Havlíčkův Brod'
const HOSTILE_PRICE_MINOR = 45149458400

// Локаль проекта (390-light-ru → ru). Без пинга app рендерит дефолтную
// локаль (ru) во ВСЕХ проектах — en-проекты проверяли бы ru-рендер.
const isRu = (testInfo: TestInfo) => testInfo.project.name.endsWith('-ru')
function pinLocale(page: Page, testInfo: TestInfo) {
  page.addInitScript((l) => localStorage.setItem('pb.locale', l), isRu(testInfo) ? 'ru' : 'en')
}

function cover(n: number) {
  return {
    ord: n,
    original_url: 'https://reality.bazos.net/realty/946/',
    card_url: `/photos/946/${n}_card.webp`,
    list_url: `/photos/946/${n}_list.webp`,
  }
}

// Деньги — минорные (CZK, экспонента 2): 850000000 = 8 500 000 CZK.
function valuation(over: Record<string, unknown> = {}) {
  return {
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
    ...over,
  }
}

function obj(over: Record<string, unknown> = {}) {
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

// 8 строк — все состояния, которые строка может иметь:
const LIST = {
  total: 8,
  page: 1,
  per_page: 20,
  objects: [
    obj({
      id: 1,
      address: HOSTILE_ADDRESS,
      price_minor: HOSTILE_PRICE_MINOR,
      area_sqm: null,
      rooms: 4,
      cover_photo: cover(1),
      seller_type: 'agency',
      seller_name: 'RE/MAX s.r.o.',
    }),
    obj({ id: 2, cover_photo: cover(2), price_trend: 'reduced' }),
    obj({ id: 3, cover_photo: null }), // без фото — нейтральный плейсхолдер
    obj({
      id: 4,
      cover_photo: cover(4),
      valuation: valuation({ zone_fallback: true, sample_size: 12 }),
    }),
    obj({
      id: 5,
      cover_photo: cover(5),
      status: 'delisted',
      delisted_at: T,
      delisted_reason: 'sold',
      price_trend: 'reduced', // «ушёл с рынка» имеет приоритет над трендом
    }),
    obj({ id: 6, cover_photo: cover(6), area_sqm: 54, seller_type: 'developer' }),
    obj({ id: 7, cover_photo: cover(7), seller_type: 'developer' }),
    obj({ id: 8, cover_photo: cover(8), valuation: null, seller_type: null }),
  ],
}

function mockList(page: Page): { photos: string[] } {
  const photos: string[] = []
  void page.route('**/api/objects*', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify(LIST),
    }),
  )
  page.on('request', (r) => {
    if (r.url().includes('/photos/')) photos.push(r.url())
  })
  return { photos }
}

// Сколько обложек должно грузиться сразу: строки 1–4 списка (индекс < 4),
// за вычетом строки без фото.
const withCover = LIST.objects.map((o, i) => ({ o, i })).filter((x) => x.o.cover_photo)
const EAGER_COUNT = withCover.filter((x) => x.i < 4).length
const EAGER_URLS = withCover.filter((x) => x.i < 4).map((x) => (x.o.cover_photo as { list_url: string }).list_url)

test('U15: CLS = 0 при прокрутке; первые четыре обложки — eager и в сети', async ({
  page,
}, testInfo) => {
  pinLocale(page, testInfo)
  const { photos } = mockList(page)
  await page.addInitScript(() => {
    ;(window as unknown as { __cls: number }).__cls = 0
    new PerformanceObserver((list) => {
      for (const e of list.getEntries()) if (!e.hadRecentInput) window.__cls += e.value
    }).observe({ type: 'layout-shift', buffered: true })
  })
  await page.goto('/#/list')
  await page.waitForSelector('.object-list .row', { timeout: 20_000 })
  // Ожидаем, пока браузер дождётся (или откажется) в первые обложки —
  // lazy-изображения вне вьюпорта не запросим, это и есть проверка.
  await page.waitForTimeout(500)

  const loadings = await page.locator('.row img.cover-img').evaluateAll(
    (imgs) => imgs.map((i) => i.getAttribute('loading')),
  )
  expect(loadings.length, 'обложек в строках меньше, чем в фикстуре').toBe(withCover.length)
  for (let i = 0; i < EAGER_COUNT; i++) expect(loadings[i], `строка ${i + 1}: loading`).toBe('eager')
  for (let i = EAGER_COUNT; i < loadings.length; i++) expect(loadings[i], `строка ${i + 1}: loading`).toBe('lazy')

  // Первые четыре обложки запрошены в сети БЕЗ прокрутки.
  for (const url of EAGER_URLS) {
    expect(
      photos.some((u) => u.includes(url)),
      `первая обложка ${url} не запрошена без прокрутки`,
    ).toBe(true)
  }

  // Прокрутка до конца списка: сдвигов макета нет (aspect-ratio держит).
  await page.evaluate(async () => {
    for (let y = 0; y <= document.body.scrollHeight; y += 250) {
      window.scrollTo(0, y)
      await new Promise((r) => setTimeout(r, 60))
    }
  })
  await page.waitForTimeout(300)
  const cls = await page.evaluate(() => (window as unknown as { __cls: number }).__cls)
  expect(cls, `CLS при прокрутке списка ≠ 0 (U15): ${cls}`).toBe(0)
})

test('U15: объект без фото — нейтральный плейсхолдер той же пропорции', async ({
  page,
}, testInfo) => {
  pinLocale(page, testInfo)
  mockList(page)
  await page.goto('/#/list')
  await page.waitForSelector('.object-list .row', { timeout: 20_000 })

  // id 1 — hostile (другой адрес); Sokolovská начинается с id 2 → nth(1) = id 3.
  const row = page.locator('.row', { hasText: 'Sokolovská' }).nth(1)
  await expect(row.locator('img.cover-img')).toHaveCount(0)
  const ph = row.locator('.row-cover .cover-ph')
  await expect(ph).toBeVisible()
  const geo = await row.locator('.row-cover').evaluate((el) => {
    const cs = getComputedStyle(el)
    const r = el.getBoundingClientRect()
    return { aspect: cs.aspectRatio, bg: cs.backgroundColor, w: r.width, h: r.height }
  })
  expect(geo.aspect, 'плейсхолдер держит пропорцию 4/3').not.toBe('auto')
  expect(geo.bg, 'плейсхолдер — нейтральный фон, не пустота').not.toBe('rgba(0, 0, 0, 0)')
  expect(geo.w / geo.h, 'фактическая пропорция ≈ 4/3').toBeCloseTo(4 / 3, 1)
})

test('U16: строка по TZ v4 §6.3 — цена/м², площадь не указана, продавец, бейджи на обложке', async ({
  page,
}, testInfo) => {
  pinLocale(page, testInfo)
  mockList(page)
  await page.goto('/#/list')
  await page.waitForSelector('.object-list .row', { timeout: 20_000 })
  const rows = page.locator('.object-list .row')
  expect(await rows.count()).toBe(8)

  // Цена/м² — производная, где площадь есть (строка 2: 8 500 000 / 54).
  // Строка 0 — hostile, площади нет.
  const perSqm = rows.nth(1).locator('.price-sqm')
  await expect(perSqm).toContainText(/\/м²|\/m²/)

  // Цена есть, площади нет — «площадь не указана» (R7), строка 0 (C35).
  await expect(rows.nth(0).locator('.price-sqm.muted')).toHaveText(/площадь не указана|area not specified/)

  // Бейдж на обложке: статус читается без раскрытия строки (U16).
  await expect(rows.nth(4).locator('.row-cover .cover-badge')).toHaveText(/ушёл с рынка|off the market/)
  await expect(rows.nth(1).locator('.row-cover .cover-badge')).toHaveText(/цена снижена|price reduced/)

  // Продавец — свой строкой (U16), строки 1 и 6 (C35: developer на id 6).
  await expect(rows.nth(0).locator('.row-seller')).toContainText(/агентство|agency/)
  await expect(rows.nth(5).locator('.row-seller')).toContainText(/застройщик|developer/)

  // U20: замещение зоны — видимым текстом в компактной метке, строка 4.
  await expect(rows.nth(3).locator('.dev-meta-list.fallback')).toContainText(
    /зона: родительский|zone: parent/,
  )

  // Ключевые параметры — отдельной строкой, а не в адресе (E9).
  // Площадь 54 — строка 2 (id 2); строка 0 без площади.
  await expect(rows.nth(1).locator('.row-facts')).toContainText(/54/)
  await expect(rows.nth(1).locator('.row-facts')).toContainText(/комн|m²/)
  await expect(rows.nth(0).locator('.addr')).not.toContainText(/комн|room/)
})

test('U16: плотность — не менее 4 строк на первом экране при 390 px', async ({ page }, testInfo) => {
  const [width] = testInfo.project.name.split('-')
  if (width !== '390') return // плотность — приёмка именно 390 (TZ v4 §6.3)
  pinLocale(page, testInfo)
  mockList(page)
  await page.goto('/#/list')
  await page.waitForSelector('.object-list .row', { timeout: 20_000 })
  await page.waitForTimeout(500)

  const fullyVisible = await page.locator('.object-list .row').evaluateAll((rows) => {
    const bottom = window.innerHeight - 46 // таб-бар
    return rows.filter((r) => {
      const b = r.getBoundingClientRect()
      return b.top >= -1 && b.bottom <= bottom + 1
    }).length
  })
  testInfo.annotations.push({ description: 'u16-density-390', value: String(fullyVisible) })
  expect(fullyVisible, 'на первом экране при 390 px меньше 4 строк (U16)').toBeGreaterThanOrEqual(4)
})

test('U16: 320/390/430 — ключевые элементы не обрезаны, строка — одна ссылка ≥ 44 px', async ({
  page,
}, testInfo) => {
  pinLocale(page, testInfo)
  mockList(page)
  await page.goto('/#/list')
  await page.waitForSelector('.object-list .row', { timeout: 20_000 })

  await assertNoHorizontalOverflow(page)
  await assertNoTruncation(page)

  // Адрес — line-clamp: 2 (снимает E9), не nowrap.
  const addr = await page.locator('.object-list .addr').first().evaluate((el) => {
    const cs = getComputedStyle(el)
    return { ws: cs.whiteSpace, clamp: cs.webkitLineClamp, display: cs.display }
  })
  expect(addr.ws).not.toBe('nowrap')
  expect(addr.clamp, 'адрес ограничен двумя строками').toBe('2')

  // Ключевые элементы строки полностью внутри вьюпорта.
  const vp = page.viewportSize()!
  for (const sel of ['.row-facts', '.row-seller', '.price-sqm', '.cover-badge']) {
    const els = page.locator(`.object-list ${sel}`)
    const n = await els.count()
    for (let i = 0; i < n; i++) {
      const box = await els.nth(i).boundingBox()
      expect(box, `${sel}: элемент не рендерится`).not.toBeNull()
      if (box) {
        expect(box.x + box.width, `${sel} выходит за правый край`).toBeLessThanOrEqual(vp.width)
      }
    }
  }

  // Строка — ОДНА ссылка, hit-area ≥ 44 px по высоте.
  const row = page.locator('.object-list .row').first()
  expect(await row.evaluate((el) => el.tagName)).toBe('A')
  expect(await row.locator('a').count(), 'внутри строки нет вложенных ссылок').toBe(0)
  const box = (await row.boundingBox())!
  expect(box.height, 'hit-area строки < 44 px').toBeGreaterThanOrEqual(44)
})

// U29 (ТЗ §7): счётчик шторки пересчитывается одним запросом на изменение,
// и упразднён подход «второй запрос с per_page=1» — total берётся из того
// же ответа.
test('U29: счётчик шторки — один запрос на изменение, без per_page=1', async ({
  page,
}, testInfo) => {
  await pinLocale(page, testInfo)
  const urls: string[] = []
  void page.route('**/api/objects*', (route) => {
    const u = new URL(route.request().url())
    urls.push(route.request().url())
    // total зависит от фильтра: смена статуса меняет число в шторке.
    void route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ ...LIST, total: u.searchParams.get('status') === 'delisted' ? 3 : 8 }),
    })
  })
  await page.goto('/#/list')
  await page.waitForSelector('.object-list .row', { timeout: 20_000 })
  const base = urls.length

  // Открытие шторки — ровно ОДИН превью-запрос с per_page=50.
  await page.locator('.filter-btn[aria-haspopup="dialog"]').click()
  const sheet = page.locator('.sheet[role="dialog"]')
  await expect(sheet).toBeVisible()
  await expect.poll(() => urls.length).toBe(base + 1)
  const openUrl = new URL(urls[urls.length - 1])
  expect(openUrl.searchParams.get('per_page'), 'превью-запрос: per_page').toBe('50')
  await expect(sheet.locator('.live-count b')).toContainText('8')

  // Смена фильтра — ещё ровно ОДИН запрос, тот же per_page; число
  // обновлено тем же ответом (total), а не отдельным запросом.
  const n = urls.length
  await sheet.locator('.field select').nth(1).selectOption('delisted')
  await expect.poll(() => urls.length).toBe(n + 1)
  const changedUrl = new URL(urls[urls.length - 1])
  expect(changedUrl.searchParams.get('status')).toBe('delisted')
  expect(changedUrl.searchParams.get('per_page'), 'превью после смены: per_page').toBe('50')
  await expect(sheet.locator('.live-count b')).toContainText('3')

  // per_page=1 — ни в одном запросе (упразднённый подход).
  for (const u of urls) {
    expect(new URL(u).searchParams.get('per_page'), `per_page=1 в ${u}`).not.toBe('1')
  }
})
