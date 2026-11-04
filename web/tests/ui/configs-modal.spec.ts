// TZ UI v5, Волна 2 — U22 (правка конфигураций только в модалке,
// «было → стало» перед применением — TZ v4 §7.1, дефект D6) и U23
// (структурные мелочи: фильтр на строку, item-sub по строкам,
// спиннер поиска зон).
//
// Приёмка U22: нажатие «Изменить» открывает модальное окно (примитив
// U07) с текущими значениями; применение показывает «было → стало».
// Плюс: закрытие с несохранёнными изменениями — подтверждение;
// создание — POST; сохранение — PUT + сообщение + подсветка записи.
//
// API мокапится: одна запись (id 11, CZ/sale, CZK, экспонента 0) и
// реестр атрибутов CZ. Методы PUT/POST перехватываются для проверки
// плейлоада.
import { test, expect, type Page, type TestInfo } from '@playwright/test'
import fs from 'node:fs'
import path from 'node:path'

if (!fs.existsSync(path.join(process.cwd(), 'playwright.config.ts'))) {
  throw new Error('Playwright-тесты запускать из web/ (npm run test:ui)')
}

const T = '2026-09-08T12:30:00Z'

const CFG1 = {
  id: 11,
  source_id: 'bazos-reality',
  country: 'CZ',
  deal_type: 'sale',
  property_type: null,
  filter_attributes: { floor: 3 },
  min_area_sqm: '50',
  max_area_sqm: '100',
  min_price_minor: 20000000,
  max_price_minor: 40000000,
  currency: 'CZK',
  active: true,
  created_at: '2026-09-01T10:00:00Z',
  updated_at: T,
}

const ATTRS = [
  {
    key: 'floor',
    data_type: 'int',
    used_in_pricing: false,
    label_ru: 'Этаж',
    label_en: 'Floor',
    source_evidence: 'Базовая карта объявления',
  },
]

const isRu = (testInfo: TestInfo) => testInfo.project.name.endsWith('-ru')
function pinLocale(page: Page, testInfo: TestInfo) {
  page.addInitScript((l) => localStorage.setItem('pb.locale', l), isRu(testInfo) ? 'ru' : 'en')
}

function L(ru: boolean) {
  return ru
    ? {
        edit: 'Изменить',
        save: 'Сохранить',
        apply: 'Применить',
        newCfg: 'Новая конфигурация',
        stay: 'Остаться',
        close: 'Закрыть',
        closeText: 'Изменения не сохранены.',
        saved: 'Сохранено:',
        created: 'Создана:',
        name: 'Чехия — продажа',
        diffTitle: 'Изменения',
        filterLine: 'Этаж: 3',
        priceMax: 'Цена, макс (CZK)',
        priceMin: 'Цена, мин (CZK)',
      }
    : {
        edit: 'Edit',
        save: 'Save',
        apply: 'Apply',
        newCfg: 'New configuration',
        stay: 'Stay',
        close: 'Close',
        closeText: 'Changes have not been saved.',
        saved: 'Saved:',
        created: 'Created:',
        name: 'Czechia — sale',
        diffTitle: 'Changes',
        filterLine: 'Floor: 3',
        priceMax: 'Price, max (CZK)',
        priceMin: 'Price, min (CZK)',
      }
}

function mockConfigs(page: Page) {
  const puts: Record<string, unknown>[] = []
  const posts: Record<string, unknown>[] = []
  void page.route('**/api/search-configs', (route) => {
    const req = route.request()
    if (req.method() === 'GET') {
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify([CFG1]) })
    }
    // POST — создание
    posts.push(req.postDataJSON() as Record<string, unknown>)
    return route.fulfill({ status: 201, contentType: 'application/json', body: JSON.stringify({ id: 12 }) })
  })
  void page.route('**/api/search-configs/11', (route) => {
    puts.push(route.request().postDataJSON() as Record<string, unknown>)
    route.fulfill({ status: 204 })
  })
  void page.route('**/api/attribute-registry*', (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(ATTRS) }),
  )
  return { puts, posts }
}

test('U23: карточка — item-sub по строкам, фильтр один на строку', async ({ page }, testInfo) => {
  pinLocale(page, testInfo)
  mockConfigs(page)
  await page.goto('/#/settings')
  await page.waitForSelector('.item', { timeout: 20_000 })

  // item-sub — три структурированные строки: цена / источник / обновлена.
  // 20000000 минорных CZK (экспонента 2) = «200 000» в карточке.
  const sub = page.locator('.item .item-sub').first()
  expect(await sub.locator('> span').count(), 'item-sub — 3 строки (U23)').toBe(3)
  await expect(sub).toContainText('bazos-reality')
  await expect(sub).toContainText(/200[,\s\s  ]?000/)

  // Фильтры — по одному на строку, подпись из реестра страны (не ключ).
  const line = page.locator('.item .filter-lines li')
  expect(await line.count()).toBe(1)
  await expect(line).toHaveText(L(isRu(testInfo)).filterLine)
  await expect(page.locator('.item')).not.toContainText('floor: 3')
})

test('U22: «Изменить» — модалка с текущими значениями, «было → стало», PUT', async ({
  page,
}, testInfo) => {
  pinLocale(page, testInfo)
  const { puts } = mockConfigs(page)
  const s = L(isRu(testInfo))
  await page.goto('/#/settings')
  await page.waitForSelector('.item', { timeout: 20_000 })

  // Нажатие «Изменить» открывает МОДАЛЬНОЕ ОКНО (U07), не форму внизу.
  await page.getByRole('button', { name: s.edit }).click()
  const modal = page.locator('.modal')
  await expect(modal).toBeVisible()
  await expect(modal).toHaveAttribute('role', 'dialog')

  // Текущие значения подставлены (CZK, экспонента 2: 40000000 минорных
  // = «400000.00» в строке формы).
  const maxPrice = modal.getByLabel(s.priceMax)
  await expect(maxPrice).toHaveValue('400000.00')
  await expect(modal.getByLabel(s.priceMin)).toHaveValue('200000.00')

  // Меняем одну границу → список «было → стало» ровно на это поле.
  await maxPrice.fill('500000.00')
  await page.getByRole('button', { name: s.save }).click()
  const diff = page.locator('.modal .diff-list')
  await expect(diff).toBeVisible()
  const dts = diff.locator('dt')
  expect(await dts.count(), 'в диффе только изменённое поле').toBe(1)
  await expect(dts.first()).toHaveText(s.priceMax)
  await expect(diff.locator('dd').first()).toContainText('400000.00')
  await expect(diff.locator('dd').first()).toContainText('500000.00')

  // «Применить» — PUT с новым значением; модалка закрыта; запись
  // подсвечена; результат назван сообщением (U24).
  await page.getByRole('button', { name: s.apply }).click()
  await expect(modal).toBeHidden()
  expect(puts.length, 'PUT не ушёл').toBe(1)
  expect(puts[0].max_price_minor).toBe(50000000)
  expect(puts[0].min_price_minor).toBe(20000000)
  expect(puts[0].currency).toBe('CZK')
  await expect(page.locator('.form-note')).toContainText(`${s.saved} ${s.name}`)
  await expect(page.locator('.item.highlight')).toBeVisible()
})

test('U22: несохранённые изменения — подтверждение закрытия', async ({ page }, testInfo) => {
  pinLocale(page, testInfo)
  const { puts } = mockConfigs(page)
  const s = L(isRu(testInfo))
  await page.goto('/#/settings')
  await page.waitForSelector('.item', { timeout: 20_000 })

  await page.getByRole('button', { name: s.edit }).click()
  const modal = page.locator('.modal')
  await expect(modal).toBeVisible()

  // Грязим форму (переключатель «Активна») и закрываем по «×».
  await modal.locator('input[type=checkbox]').first().click()
  await modal.locator('.modal-x').click()
  await expect(modal.getByText(s.closeText)).toBeVisible()

  // «Остаться» — назад в форму со значениями.
  await page.getByRole('button', { name: s.stay }).click()
  await expect(modal.getByLabel(s.priceMax)).toHaveValue('400000.00')

  // Снова закрыть — теперь «Закрыть» (первичная кнопка подтверждения).
  await modal.locator('.modal-x').click()
  await expect(modal.getByText(s.closeText)).toBeVisible()
  await modal.locator('.form-actions button.primary').click()
  await expect(modal).toBeHidden()

  // Ничего не сохранялось.
  expect(puts.length, 'PUT ушёл при закрытии без сохранения').toBe(0)
})

test('U22: новая конфигурация — POST без диффа', async ({ page }, testInfo) => {
  pinLocale(page, testInfo)
  const { posts } = mockConfigs(page)
  const s = L(isRu(testInfo))
  await page.goto('/#/settings')
  await page.waitForSelector('.item', { timeout: 20_000 })

  await page.getByRole('button', { name: s.newCfg }).click()
  const modal = page.locator('.modal')
  await expect(modal).toBeVisible()

  const selects = modal.locator('select')
  await selects.nth(0).selectOption('CZ')
  await selects.nth(1).selectOption('sale')
  await modal.getByLabel(s.priceMin).fill('100.00')
  await modal.getByLabel(s.priceMax).fill('200.00')
  await page.getByRole('button', { name: s.save }).click()

  // Создание — без этапа «было → стало» (базы для сравнения нет).
  await expect(modal).toBeHidden()
  expect(posts.length, 'POST не ушёл').toBe(1)
  expect(posts[0].min_price_minor).toBe(10000)
  expect(posts[0].max_price_minor).toBe(20000)
  expect(posts[0].currency).toBe('CZK')
  expect(posts[0].active).toBe(true)
  await expect(page.locator('.form-note')).toContainText(`${s.created} ${s.name}`)
})

test('U23: поиск зон — спиннер в поле до ответа', async ({ page }, testInfo) => {
  pinLocale(page, testInfo)
  // Ответ задерживаем: спиннер обязан быть виден между запросом и ответом.
  void page.route('**/api/zones*', (route) => {
    setTimeout(() => {
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          total: 1,
          page: 1,
          per_page: 20,
          zones: [
            {
              id: 1,
              country: 'CZ',
              level: 'municipality',
              name: 'Brno',
              external_code: 'CZ602',
              parent_name: null,
              source: 'cuzk',
            },
          ],
          sources: [],
        }),
      })
    }, 1200)
  })
  await page.goto('/#/settings')
  await page.waitForSelector('.settings-block', { timeout: 20_000 })

  const input = page.getByRole('searchbox')
  await input.type('Bra') // ≥ 2 символов — запрос уходит
  // Спиннер — в поле (aria-busy на обёртке), не строкой «Загрузка…» под полем.
  await expect(page.locator('.zone-search.busy')).toBeVisible()
  await expect(page.locator('.zone-search')).toHaveAttribute('aria-busy', 'true')
  await expect(page.locator('.zone-result')).toHaveText(/Brno/, { timeout: 10_000 })
})
