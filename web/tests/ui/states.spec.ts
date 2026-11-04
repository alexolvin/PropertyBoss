import { test, expect, type Route } from '@playwright/test'

// TZ UI v5 U09/U10 — состояния подменённым транспортом (page.route).
// Приёмка U09: три теста — задержка 500 мс → скелетон; 500 от сервера →
// причина + рабочая кнопка; офлайн → отдельный текст.
// U10: «Обновить» обновляет метку «данные на HH:MM»; метка меняется
// только после успешного ответа (источник — заголовок Date ответа
// сервера, не часы клиента).

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

test('U09: запрос дольше 300 мс — скелетон, а не «Загрузка…»', async ({ page }) => {
  const delay = (ms: number) => (route: Route) =>
    new Promise<void>((r) => setTimeout(r, ms)).then(() => route.continue())
  await page.route('**/api/meta', delay(500))
  await page.route('**/api/objects*', delay(500))
  await page.goto('/#/list')
  // Скелетон виден, пока запрос ещё летит; текст «Загрузка…» — нигде.
  await expect(page.locator('.skeleton-list')).toBeVisible({ timeout: 10_000 })
  await expect(page.locator('text=Загрузка…')).toHaveCount(0)
  await page.waitForSelector('.object-list .row, .empty')
  await expect(page.locator('.skeleton-list')).toHaveCount(0)
})

test('U09: 500 от сервера — причина и рабочая «Повторить»', async ({ page }) => {
  let failedOnce = false
  await page.route('**/api/objects*', (route) => {
    if (!failedOnce) {
      failedOnce = true
      return route.fulfill({
        status: 500,
        contentType: 'application/json',
        body: JSON.stringify({ error: 'test error' }),
      })
    }
    return route.continue()
  })
  await page.goto('/#/list')
  await expect(page.locator('main > .error')).toContainText('test error')
  const retry = page.locator('.retry-btn')
  await expect(retry).toBeVisible()
  await retry.click()
  await page.waitForSelector('.object-list .row, .empty')
  await expect(page.locator('main > .error')).toHaveCount(0)
})

test('U09: нет сети — текст, отличный от ошибки сервера', async ({ page, context }) => {
  await page.goto('/#/list')
  await page.waitForSelector('.object-list .row, .empty')
  await context.setOffline(true)
  await page.locator('.refresh-btn').click()
  const err = page.locator('main > .error')
  await expect(err).toContainText('Нет сети')
  await expect(err).not.toContainText('Ошибка:')
})

test('U10: «Обновить» меняет метку; метка меняется только после успеха', async ({ page }) => {
  const T0 = new Date(Date.UTC(2026, 8, 10, 10, 0, 0))
  const T1 = new Date(Date.UTC(2026, 8, 10, 10, 30, 0))
  const fmt = (d: Date) =>
    new Intl.DateTimeFormat('ru-RU', { hour: '2-digit', minute: '2-digit' }).format(d)
  let n = 0
  // Реальный ответ, но с подменённым заголовком Date: метка в UI обязана
  // показывать именно серверное время, не часы клиента.
  await page.route('**/api/objects*', async (route) => {
    const res = await route.fetch()
    n += 1
    const headers = { ...res.headers() }
    delete headers['date']
    headers['Date'] = (n === 1 ? T0 : T1).toUTCString()
    const body = await res.body() // APIResponse.body() — Promise<Buffer>
    await route.fulfill({ status: res.status(), headers, body })
  })
  await page.goto('/#/list')
  const asof = page.locator('.asof')
  await expect(asof).toHaveText(`данные на ${fmt(T0)}`, { timeout: 15_000 })

  // Неуспешное обновление не меняет метку.
  const boom = (route: Route) =>
    route.fulfill({
      status: 500,
      contentType: 'application/json',
      body: JSON.stringify({ error: 'boom' }),
    })
  await page.route('**/api/objects*', boom)
  await page.locator('.refresh-btn').click()
  await expect(page.locator('main > .error')).toContainText('boom')
  await expect(asof).toHaveText(`данные на ${fmt(T0)}`)
  await page.unroute('**/api/objects*', boom)

  // Успешное обновление меняет метку.
  await page.locator('.refresh-btn').click()
  await expect(asof).toHaveText(`данные на ${fmt(T1)}`, { timeout: 15_000 })
})

test('U10/D24: «Обновить» — ощутимое рабочее состояние на время запроса', async ({ page }) => {
  // Единственной обратной связью была метка «данные на HH:MM», которая
  // не меняется внутри одной минуты — клик выглядел мёртвым (D24,
  // замечание оператора 10.09): кнопка обязана показывать, что запрос
  // в полёте, и сниматься после ответа.
  let slow = false
  await page.route('**/api/objects*', async (route) => {
    if (slow) await new Promise<void>((r) => setTimeout(r, 800))
    route.continue()
  })
  await page.goto('/#/list')
  await page.waitForSelector('.object-list .row, .empty')
  const btn = page.locator('.refresh-btn')
  await expect(btn).toBeEnabled()
  slow = true
  await btn.click()
  // Пока запрос летит: рабочее состояние.
  await expect(btn).toHaveAttribute('aria-busy', 'true')
  await expect(btn).toBeDisabled()
  // После ответа: состояние снято, метка и список на месте.
  await expect(btn).not.toHaveAttribute('aria-busy')
  await expect(btn).toBeEnabled()
  await expect(page.locator('.asof')).toBeVisible()
})
